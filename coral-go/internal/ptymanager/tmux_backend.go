package ptymanager

import (
	"context"
	"fmt"
	"github.com/subgentic/coral/internal/logsafe"
	"github.com/subgentic/coral/internal/terminalinput"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/subgentic/coral/internal/logparse"
	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/safego"
	"github.com/subgentic/coral/internal/tmux"
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
	lagged      map[string]bool // subscribers cut off for falling behind (#4161; guarded by mu)
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
	DropHeld(name, "spawned") // nothing held for the old process is typed into a new one (#3431)
	ctx := context.Background()
	tmuxName := naming.SessionName(agentType, sessionID)
	logPath := naming.LogFile(b.logDir, agentType, sessionID)
	if logPath == "" {
		// naming.LogFile refuses an unsafe agent type or session id: no session is made that
		// would pipe to '' and run unlogged (#3602, Lead's NIT).
		return fmt.Errorf("spawn %s: %w for agent type %q, session %q", name, errNoLogPath, agentType, sessionID)
	}

	// Create (or empty) the log: this user only, never through a link (#3601, W17)
	resetLog(logPath)

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
		if err := b.client.SendKeysToTarget(ctx, terminalinput.Internal, tmuxName+".0", command); err != nil {
			log.Printf("[launch] %s: the start command could not be typed into its terminal: %v", name, err) // the terminal stays at its shell (#5171)
		}
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
	DropHeld(name, "killed") // nothing held for the old process is typed into a new one (#3431)
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
	DropHeld(name, "restarted") // nothing held for the old process is typed into a new one (#3431)
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
		if err := b.client.SendKeysToTarget(ctx, terminalinput.Internal, pane.Target, command); err != nil {
			log.Printf("[launch] %s: the start command could not be typed into its restarted terminal: %v", name, err) // the terminal stays at its shell (#5171)
		}
	}

	return nil
}

// SendInput writes data to the session's terminal as o's input.
func (b *TmuxBackend) SendInput(o terminalinput.Origin, name string, data []byte) error {
	return b.sendInput(context.Background(), o, name, data)
}

// SendHumanInput writes a person's keystrokes from the dashboard terminal: as SendInput, and
// logged by their length only (terminalinput.Person, #60).
func (b *TmuxBackend) SendHumanInput(name string, data []byte) error {
	return b.sendInput(context.Background(), terminalinput.Person, name, data)
}

func (b *TmuxBackend) sendInput(ctx context.Context, o terminalinput.Origin, name string, data []byte) error {

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
		return fmt.Errorf("%w: pane %q not found", ErrTerminalGone, name)
	}

	return b.client.GoneAfter(ctx, name, agentType, sessionID, b.client.SendTerminalInputToTarget(ctx, o, target, string(data)))
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
		return fmt.Errorf("%w: pane %q not found", ErrTerminalGone, name)
	}
	return b.client.GoneAfter(ctx, name, agentType, sessionID, b.client.ResizePaneTarget(ctx, target, int(cols), int(rows)))
}

// PaneSize reports the session's pane size from tmux itself, so a resize by any client (another
// dashboard, a phone, a tmux attach) is seen (#3422).
func (b *TmuxBackend) PaneSize(name string) (int, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

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
	return b.client.PaneSizeForAgent(ctx, name, agentType, sessionID)
}

// OutputStamp is the size and modification time of the session's output log, which pipe-pane
// appends everything the pane prints to: unchanged, the pane has printed nothing since.
func (b *TmuxBackend) OutputStamp(agentType, sessionID string) (string, bool) {
	if agentType == "" || sessionID == "" || b.logDir == "" {
		return "", false
	}
	fi, err := os.Stat(naming.LogFile(b.logDir, agentType, sessionID))
	if err != nil {
		return "", false
	}
	return fmt.Sprintf("%d:%d", fi.Size(), fi.ModTime().UnixNano()), true
}

// AgentPIDs is the process of every pane on Coral's tmux socket (#3444): one tmux exec.
func (b *TmuxBackend) AgentPIDs() []int {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// For machine health's process totals: a pane list a few seconds old will do (#4346).
	pids, err := b.client.PanePIDs(tmux.WithPaneListMaxAge(ctx, tmux.DisplayPaneListAge))
	if err != nil {
		return nil
	}
	return pids
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
		agentType, sessionID := logparse.ParseSessionName(pane.SessionName)
		if agentType == "" || sessionID == "" {
			continue
		}

		// Match by tmux session name (which is what routes pass as "name")
		if pane.SessionName != name {
			continue
		}

		logPath := naming.LogFile(b.logDir, agentType, sessionID)
		if logPath == "" {
			// Not a name Coral makes: left alone, never re-piped to '' (#3602, Lead's NIT).
			log.Printf("[tmux] not recovering %s: %v", pane.SessionName, errNoLogPath)
			return nil
		}

		// Reset pipe-pane: close any stale pipe from a previous server
		// instance, then re-establish to ensure output goes to our log file.
		// In between, an agent started by an older install has its log moved
		// out of the shared temp dir, scrollback included (#3601, W17).
		b.client.ClosePipePane(ctx, pane.SessionName)
		migrateTempLog(logPath)
		if _, err := os.Lstat(logPath); os.IsNotExist(err) {
			createLog(logPath)
		}
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
			return nil, fmt.Errorf("session %q not found: %w", name, ErrTerminalGone)
		}
	}

	logPath := sess.logPath

	// Self-heal: if the log file is missing, recreate and restart pipe-pane
	if _, err := os.Lstat(logPath); os.IsNotExist(err) {
		createLog(logPath)
		ctx := context.Background()
		tmuxName := naming.SessionName(sess.info.AgentType, sess.info.SessionID)
		b.client.PipePane(ctx, tmuxName, logPath)
	}

	ch := make(chan []byte, 256)

	b.mu.Lock()
	tail, exists := b.tails[name]
	if !exists {
		tail = b.startTail(name, naming.SessionName(sess.info.AgentType, sess.info.SessionID), logPath)
		b.tails[name] = tail
	}
	b.mu.Unlock()

	tail.mu.Lock()
	tail.subscribers[subscriberID] = ch
	delete(tail.lagged, subscriberID)
	tail.mu.Unlock()

	return ch, nil
}

// Lagged implements LagReporter (#4161).
func (b *TmuxBackend) Lagged(name, subscriberID string) bool {
	b.mu.RLock()
	tail, ok := b.tails[name]
	b.mu.RUnlock()
	if !ok {
		return false
	}
	tail.mu.Lock()
	defer tail.mu.Unlock()
	lagged := tail.lagged[subscriberID]
	delete(tail.lagged, subscriberID)
	return lagged
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
	delete(tail.lagged, subscriberID)
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
		agentType, sessionID := logparse.ParseSessionName(pane.SessionName)
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
func (b *TmuxBackend) startTail(name, tmuxName, logPath string) *logTail {
	ctx, cancel := context.WithCancel(context.Background())
	tail := &logTail{
		subscribers: make(map[string]chan []byte),
		lagged:      make(map[string]bool),
		cancel:      cancel,
		done:        make(chan struct{}),
	}

	f, err := os.Open(logPath)
	if err != nil {
		log.Printf("tail: open %s: %v", logsafe.Dir(logPath), err)
		cancel()
		close(tail.done)
		return tail
	}
	offset, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		log.Printf("tail: seek %s: %v", logsafe.Dir(logPath), err)
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
		log.Printf("tail: watch %s: %v", logsafe.Dir(logPath), err)
		watcher.Close()
		f.Close()
		cancel()
		close(tail.done)
		return tail
	}

	go func() {
		defer safego.Recover("ptymanager/tmux_backend:557")
		defer close(tail.done)
		defer watcher.Close()
		defer f.Close()
		b.tailLoop(ctx, name, tail, f, offset, watcher)
	}()
	go func() {
		defer safego.Recover("ptymanager/tmux_backend:b.watchSession")
		b.watchSession(ctx, name, tmuxName, tail)
	}()

	return tail
}

// tailSessionCheckEvery is how often an attached terminal's tmux session is
// checked for existence; tailSessionMisses consecutive misses end the stream.
var (
	tailSessionCheckEvery = time.Second
	tailSessionMisses     = 2
)

// watchSession ends an attach stream once its tmux session is gone (#3218): the
// log file outlives a killed session, so the tail alone would never end, the
// dashboard would never get terminal_closed and would keep sending input into
// nothing. Two consecutive misses are required, so one failed tmux call does
// not end a live stream.
func (b *TmuxBackend) watchSession(ctx context.Context, name, tmuxName string, tail *logTail) {
	t := time.NewTicker(tailSessionCheckEvery)
	defer t.Stop()
	misses := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if b.client.HasSession(ctx, "="+tmuxName) {
			misses = 0
			continue
		}
		if ctx.Err() != nil {
			return
		}
		if misses++; misses < tailSessionMisses {
			continue
		}
		log.Printf("[tmux] session %s is gone; ending its terminal stream", tmuxName)
		b.stopTailIf(name, tail)
		return
	}
}

// stopTailIf stops the tail for name only if it is still this one (a restart may
// have replaced it).
func (b *TmuxBackend) stopTailIf(name string, tail *logTail) {
	b.mu.RLock()
	current := b.tails[name]
	b.mu.RUnlock()
	if current == tail {
		b.stopTail(name)
	}
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
					fanOut(tail.subscribers, tail.lagged, data) // one that fell behind is cut off
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
