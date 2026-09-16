package ptymanager

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/cdknorow/coral/internal/naming"
	"github.com/cdknorow/coral/internal/pulse"
	"github.com/cdknorow/coral/internal/tmux"
)

// TmuxBackend implements TerminalBackend using tmux sessions.
// Terminal output is streamed by tailing the pipe-pane log file via fsnotify.
type TmuxBackend struct {
	client *tmux.Client
	logDir string

	mu       sync.RWMutex
	sessions map[string]*tmuxSession // keyed by session name
	tails    map[string]*logTail     // keyed by session name; one tail per session
}

type tmuxSession struct {
	info    SessionInfo
	logPath string
}

// logTail manages one fsnotify-based tail goroutine per session,
// fanning out new bytes to N subscriber channels.
type logTail struct {
	mu          sync.Mutex
	subscribers map[string]chan []byte
	cancel      context.CancelFunc
	done        chan struct{}
}

// NewTmuxBackend creates a TmuxBackend wrapping the given tmux client.
func NewTmuxBackend(client *tmux.Client, logDir string) *TmuxBackend {
	return &TmuxBackend{
		client:   client,
		logDir:   logDir,
		sessions: make(map[string]*tmuxSession),
		tails:    make(map[string]*logTail),
	}
}

func (b *TmuxBackend) Spawn(name, agentType, workDir, sessionID, command string, cols, rows uint16) error {
	ctx := context.Background()
	tmuxName := naming.SessionName(agentType, sessionID)
	logPath := naming.LogFile(b.logDir, agentType, sessionID)

	// Create empty log file
	os.WriteFile(logPath, []byte{}, 0644)

	// Create tmux session
	if err := b.client.NewSession(ctx, tmuxName, workDir); err != nil {
		return fmt.Errorf("tmux new-session: %w", err)
	}

	// Setup pipe-pane logging
	b.client.PipePane(ctx, tmuxName, logPath)

	// Set pane title using tmux native command (avoids shell echo issues)
	folderName := filepath.Base(strings.TrimRight(workDir, "/"))
	paneTitle := fmt.Sprintf("%s — %s", folderName, agentType)
	b.client.SetPaneTitle(ctx, tmuxName+".0", paneTitle)

	// Launch the agent command
	if command != "" {
		b.client.SendKeysToTarget(ctx, tmuxName+".0", command)
	}

	// Track session
	b.mu.Lock()
	b.sessions[name] = &tmuxSession{
		info: SessionInfo{
			AgentName:  name,
			AgentType:  agentType,
			SessionID:  sessionID,
			WorkingDir: workDir,
			Running:    true,
		},
		logPath: logPath,
	}
	b.mu.Unlock()

	return nil
}

// SessionPID returns the tmux pane's root shell PID for process-tree identity
// resolution. Resolve the generated tmux name from stored metadata because a
// backend caller may use a different public lookup name.
func (b *TmuxBackend) SessionPID(ctx context.Context, name string) (int, error) {
	tmuxName := name
	b.mu.RLock()
	if sess := b.sessions[name]; sess != nil {
		tmuxName = naming.SessionName(sess.info.AgentType, sess.info.SessionID)
	}
	b.mu.RUnlock()
	return b.client.GetPanePID(ctx, tmuxName)
}

func (b *TmuxBackend) Kill(name string) error {
	ctx := context.Background()

	// Stop any active tail for this session
	b.stopTail(name)

	b.mu.RLock()
	sess, ok := b.sessions[name]
	b.mu.RUnlock()

	if !ok {
		return b.client.KillSession(ctx, name, "", "")
	}

	err := b.client.KillSession(ctx, name, sess.info.AgentType, sess.info.SessionID)

	b.mu.Lock()
	delete(b.sessions, name)
	b.mu.Unlock()

	return err
}

func (b *TmuxBackend) Restart(name, command string) error {
	ctx := context.Background()

	// Stop tail during respawn
	b.stopTail(name)

	b.mu.RLock()
	sess, ok := b.sessions[name]
	b.mu.RUnlock()
	if !ok {
		return fmt.Errorf("session %q not found", name)
	}

	pane, err := b.client.FindPane(ctx, name, sess.info.AgentType, sess.info.SessionID)
	if err != nil || pane == nil {
		return fmt.Errorf("pane not found for %q", name)
	}

	// Close pipe-pane, respawn, re-establish
	b.client.ClosePipePane(ctx, pane.Target)
	if err := b.client.RespawnPane(ctx, pane.Target, sess.info.WorkingDir); err != nil {
		return err
	}

	time.Sleep(500 * time.Millisecond)
	b.client.PipePane(ctx, pane.Target, sess.logPath)

	if command != "" {
		b.client.SendKeysToTarget(ctx, pane.Target, command)
	}

	return nil
}

func (b *TmuxBackend) SendInput(name string, data []byte) error {
	ctx := context.Background()

	b.mu.RLock()
	sess, ok := b.sessions[name]
	b.mu.RUnlock()

	if !ok {
		sess = b.recoverSession(name)
	}

	agentType, sessionID := "", ""
	if sess != nil {
		agentType = sess.info.AgentType
		sessionID = sess.info.SessionID
	}

	target, err := b.client.FindPaneTarget(ctx, name, agentType, sessionID)
	if err != nil {
		return err
	}
	if target == "" {
		return fmt.Errorf("pane %q not found", name)
	}

	return b.client.SendTerminalInputToTarget(ctx, target, string(data))
}

func (b *TmuxBackend) Resize(name string, cols, rows uint16) error {
	ctx := context.Background()

	b.mu.RLock()
	sess, ok := b.sessions[name]
	b.mu.RUnlock()

	if !ok {
		sess = b.recoverSession(name)
	}

	agentType, sessionID := "", ""
	if sess != nil {
		agentType = sess.info.AgentType
		sessionID = sess.info.SessionID
	}

	target, err := b.client.FindPaneTarget(ctx, name, agentType, sessionID)
	if err != nil {
		return err
	}
	if target == "" {
		return fmt.Errorf("pane %q not found", name)
	}
	return b.client.ResizePaneTarget(ctx, target, int(cols), int(rows))
}

// recoverSession discovers a session from live tmux panes and registers it
// in the in-memory map. This handles the post-restart case where tmux sessions
// survive but TmuxBackend's map is empty. Returns the recovered session or nil.
func (b *TmuxBackend) recoverSession(name string) *tmuxSession {
	ctx := context.Background()
	panes, err := b.client.ListPanes(ctx)
	if err != nil {
		return nil
	}

	for _, pane := range panes {
		agentType, sessionID := pulse.ParseSessionName(pane.SessionName)
		if agentType == "" || sessionID == "" {
			continue
		}

		// Match by tmux session name (which is what routes pass as "name")
		if pane.SessionName != name {
			continue
		}

		logPath := naming.LogFile(b.logDir, agentType, sessionID)

		// Ensure log file exists
		if _, err := os.Stat(logPath); os.IsNotExist(err) {
			os.WriteFile(logPath, []byte{}, 0644)
		}

		// Reset pipe-pane: close any stale pipe from a previous server
		// instance, then re-establish to ensure output goes to our log file.
		b.client.ClosePipePane(ctx, pane.SessionName)
		b.client.PipePane(ctx, pane.SessionName, logPath)

		agentName := filepath.Base(strings.TrimRight(pane.CurrentPath, "/"))
		if agentName == "" {
			agentName = sessionID[:8]
		}

		sess := &tmuxSession{
			info: SessionInfo{
				AgentName:  agentName,
				AgentType:  agentType,
				SessionID:  sessionID,
				WorkingDir: pane.CurrentPath,
				Running:    true,
			},
			logPath: logPath,
		}

		b.mu.Lock()
		b.sessions[name] = sess
		b.mu.Unlock()

		log.Printf("Recovered tmux session %q (type=%s, id=%s)", name, agentType, sessionID)
		return sess
	}
	return nil
}

// Attach registers a subscriber for live terminal output streamed from the
// pipe-pane log file. Starts a tail goroutine for the session if one isn't
// already running. Never returns a nil channel for a live session.
func (b *TmuxBackend) Attach(name, subscriberID string) (<-chan []byte, error) {
	b.mu.RLock()
	sess, ok := b.sessions[name]
	b.mu.RUnlock()
	if !ok {
		sess = b.recoverSession(name)
		if sess == nil {
			return nil, fmt.Errorf("session %q not found", name)
		}
	}

	logPath := sess.logPath

	// Self-heal: if the log file is missing, recreate and restart pipe-pane
	if _, err := os.Stat(logPath); os.IsNotExist(err) {
		os.WriteFile(logPath, []byte{}, 0644)
		ctx := context.Background()
		tmuxName := naming.SessionName(sess.info.AgentType, sess.info.SessionID)
		b.client.PipePane(ctx, tmuxName, logPath)
	}

	ch := make(chan []byte, 256)

	b.mu.Lock()
	tail, exists := b.tails[name]
	if !exists {
		tail = b.startTail(name, logPath)
		b.tails[name] = tail
	}
	b.mu.Unlock()

	tail.mu.Lock()
	tail.subscribers[subscriberID] = ch
	tail.mu.Unlock()

	return ch, nil
}

// Unsubscribe removes a subscriber. When the last subscriber leaves,
// the tail goroutine is stopped.
func (b *TmuxBackend) Unsubscribe(name, subscriberID string) {
	b.mu.RLock()
	tail, ok := b.tails[name]
	b.mu.RUnlock()
	if !ok {
		return
	}

	tail.mu.Lock()
	if ch, exists := tail.subscribers[subscriberID]; exists {
		close(ch)
		delete(tail.subscribers, subscriberID)
	}
	remaining := len(tail.subscribers)
	tail.mu.Unlock()

	if remaining == 0 {
		b.stopTail(name)
	}
}

// Replay reads the last ReplayBytes() of the pipe-pane log file.
// Falls back to capture-pane if the log file is empty (e.g. after server
// restart before pipe-pane has produced new output).
func (b *TmuxBackend) Replay(name string) ([]byte, error) {
	b.mu.RLock()
	sess, ok := b.sessions[name]
	b.mu.RUnlock()
	if !ok {
		sess = b.recoverSession(name)
		if sess == nil {
			return nil, fmt.Errorf("session %q not found", name)
		}
	}

	data, err := readTail(sess.logPath, ReplayBytes())
	if err == nil && len(data) > 0 {
		return data, nil
	}

	// Fallback: log file empty or missing — use capture-pane as emergency seed
	ctx := context.Background()
	content, capErr := b.client.CapturePaneRawTarget(ctx, name+".0", 200)
	if capErr == nil && content != "" {
		return []byte(content), nil
	}

	return data, err
}

func (b *TmuxBackend) ListSessions() []SessionInfo {
	ctx := context.Background()
	panes, err := b.client.ListPanes(ctx)
	if err != nil {
		return nil
	}

	seen := make(map[string]bool)
	var sessions []SessionInfo

	for _, pane := range panes {
		agentType, sessionID := pulse.ParseSessionName(pane.SessionName)
		if agentType == "" || sessionID == "" {
			continue
		}
		if seen[sessionID] {
			continue
		}
		seen[sessionID] = true

		agentName := filepath.Base(strings.TrimRight(pane.CurrentPath, "/"))
		if agentName == "" {
			agentName = sessionID[:8]
		}

		sessions = append(sessions, SessionInfo{
			AgentName:  agentName,
			AgentType:  agentType,
			SessionID:  sessionID,
			WorkingDir: pane.CurrentPath,
			Running:    true,
		})
	}

	return sessions
}

func (b *TmuxBackend) IsRunning(name string) bool {
	ctx := context.Background()
	b.mu.RLock()
	sess, ok := b.sessions[name]
	b.mu.RUnlock()

	if !ok {
		return false
	}

	pane, _ := b.client.FindPane(ctx, name, sess.info.AgentType, sess.info.SessionID)
	return pane != nil
}

func (b *TmuxBackend) LogPath(name string) string {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if sess, ok := b.sessions[name]; ok {
		return sess.logPath
	}
	return ""
}

func (b *TmuxBackend) Close() error {
	// Stop all tails
	b.mu.Lock()
	for name := range b.tails {
		if tail, ok := b.tails[name]; ok {
			tail.cancel()
			<-tail.done
		}
	}
	b.tails = make(map[string]*logTail)
	b.mu.Unlock()
	return nil
}

// ── Tail goroutine management ───────────────────────────────────────────

// startTail opens the log file, seeks to EOF, and installs an fsnotify watcher
// synchronously before spawning the read-loop goroutine. Doing the setup
// inline (rather than inside the goroutine) guarantees that any bytes written
// to the log after startTail returns are observed: if the seek-to-EOF ran
// asynchronously, a write racing with goroutine startup could land before the
// seek and be skipped. Caller must hold b.mu write lock.
func (b *TmuxBackend) startTail(name, logPath string) *logTail {
	ctx, cancel := context.WithCancel(context.Background())
	tail := &logTail{
		subscribers: make(map[string]chan []byte),
		cancel:      cancel,
		done:        make(chan struct{}),
	}

	f, err := os.Open(logPath)
	if err != nil {
		log.Printf("tail: open %s: %v", logPath, err)
		cancel()
		close(tail.done)
		return tail
	}
	offset, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		log.Printf("tail: seek %s: %v", logPath, err)
		f.Close()
		cancel()
		close(tail.done)
		return tail
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("tail: fsnotify init: %v", err)
		f.Close()
		cancel()
		close(tail.done)
		return tail
	}
	if err := watcher.Add(logPath); err != nil {
		log.Printf("tail: watch %s: %v", logPath, err)
		watcher.Close()
		f.Close()
		cancel()
		close(tail.done)
		return tail
	}

	go func() {
		defer close(tail.done)
		defer watcher.Close()
		defer f.Close()
		b.tailLoop(ctx, name, tail, f, offset, watcher)
	}()

	return tail
}

// stopTail cancels and cleans up the tail for a session.
func (b *TmuxBackend) stopTail(name string) {
	b.mu.Lock()
	tail, ok := b.tails[name]
	if ok {
		delete(b.tails, name)
	}
	b.mu.Unlock()

	if ok {
		tail.cancel()
		<-tail.done

		// Close remaining subscriber channels
		tail.mu.Lock()
		for id, ch := range tail.subscribers {
			close(ch)
			delete(tail.subscribers, id)
		}
		tail.mu.Unlock()
	}
}

// tailLoop is the core tail goroutine: reads new bytes from the already-open
// file whenever fsnotify fires a Write event, and fans them out to subscribers.
// Setup (open, seek, watcher Add) happens in startTail so that writes racing
// with goroutine startup are not missed.
func (b *TmuxBackend) tailLoop(ctx context.Context, name string, tail *logTail, f *os.File, offset int64, watcher *fsnotify.Watcher) {
	buf := make([]byte, 32*1024)

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if event.Op&fsnotify.Write == 0 {
				continue
			}
			// Read all new bytes
			for {
				n, readErr := f.ReadAt(buf, offset)
				if n > 0 {
					data := make([]byte, n)
					copy(data, buf[:n])
					offset += int64(n)

					tail.mu.Lock()
					for _, ch := range tail.subscribers {
						select {
						case ch <- data:
						default:
							// Slow subscriber — drop frame
						}
					}
					tail.mu.Unlock()
				}
				if readErr != nil || n < len(buf) {
					break
				}
			}
		case watchErr, ok := <-watcher.Errors:
			if !ok {
				return
			}
			log.Printf("tail: watcher error for %s: %v", name, watchErr)
		}
	}
}

// readTail reads the last n bytes from a file.
func readTail(path string, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}

	size := info.Size()
	readSize := int64(n)
	if readSize > size {
		readSize = size
	}

	buf := make([]byte, readSize)
	_, err = f.ReadAt(buf, size-readSize)
	if err != nil && err != io.EOF {
		return nil, err
	}
	return buf, nil
}
