package ptymanager

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cdknorow/coral/internal/agentenv"
	"github.com/cdknorow/coral/internal/inputlog"
	"github.com/cdknorow/coral/internal/naming"
)

// session represents a single running PTY session.
type session struct {
	name       string
	agentType  string
	sessionID  string
	workingDir string

	proc ptyProcess
	// logFile is written by readLoop and closed by whichever ends first, readLoop at
	// EOF or kill: logMu guards it (writeLog, closeLog).
	logMu   sync.Mutex
	logFile *os.File
	logPath string

	mu          sync.Mutex
	subscribers map[string]chan []byte
	cols, rows  uint16 // the PTY's size as last set (guarded by mu)

	// Ring buffer of recent output for snapshot on reconnect
	ringMu  sync.Mutex
	ring    []byte
	ringMax int

	// bracketedPaste is the application's last DECSET/DECRST 2004 state seen in
	// its output; bpCarry holds a possible sequence split across reads (only
	// readLoop touches it).
	bracketedPaste atomic.Bool
	bpCarry        []byte

	// promptSem serializes whole prompts (paste, delay, Enter) to this session.
	promptSemOnce sync.Once
	promptSem     chan struct{}
}

// newSession creates and starts a new PTY session.
func newSession(name, agentType, workDir, sessionID, command string, cols, rows uint16) (*session, error) {
	parts := parseCommand(command)
	if len(parts) == 0 {
		// Default to a shell if no command specified
		parts = defaultShell()
	}

	// Coral's own server settings never reach the agent (#3389).
	env := append(agentenv.Filter(os.Environ()),
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		fmt.Sprintf("CORAL_SESSION_NAME=%s", name),
	)

	// Set up log file (in the data folder's logs folder, which startup sets; #3601). An unsafe
	// agent type or session id gets no log path: refused before the process starts (#3602).
	logPath := naming.LogFile(naming.DefaultLogDir(), agentType, sessionID)
	if logPath == "" {
		return nil, fmt.Errorf("session %s: %w for agent type %q, session %q", name, errNoLogPath, agentType, sessionID)
	}

	proc, err := startPTYProcess(parts[0], parts[1:], workDir, env, cols, rows)
	if err != nil {
		return nil, fmt.Errorf("startPTYProcess: %w", err)
	}

	logFile, err := openLogAppend(logPath)
	if err != nil {
		log.Printf("Warning: could not open log file %s: %v", logPath, err)
	}

	s := &session{
		name:        name,
		agentType:   agentType,
		sessionID:   sessionID,
		workingDir:  workDir,
		proc:        proc,
		logFile:     logFile,
		logPath:     logPath,
		subscribers: make(map[string]chan []byte),
		cols:        cols,
		rows:        rows,
		ring:        make([]byte, 0, 256*1024),
		ringMax:     256 * 1024, // 256KB ring buffer
	}

	// Start output reader goroutine
	go s.readLoop()

	return s, nil
}

// readLoop reads PTY output and fans out to log file and subscribers.
// When the PTY closes (EOF), the log file is closed here to prevent FD leaks
// if the session exits naturally without kill() being called.
func (s *session) readLoop() {
	defer s.closeLog()
	buf := make([]byte, 32*1024)
	for {
		n, err := s.proc.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])

			// Write to log file (for status display, FTS indexing)
			s.writeLog(data)

			s.trackBracketedPaste(data)

			// Store in ring buffer for snapshot
			s.ringMu.Lock()
			s.ring = append(s.ring, data...)
			if len(s.ring) > s.ringMax {
				s.ring = s.ring[len(s.ring)-s.ringMax:]
			}
			s.ringMu.Unlock()

			// Fan out to WebSocket subscribers (non-blocking)
			s.mu.Lock()
			for _, ch := range s.subscribers {
				select {
				case ch <- data:
				default:
					// Subscriber slow — drop frame
				}
			}
			s.mu.Unlock()
		}
		if err != nil {
			if err != io.EOF {
				log.Printf("PTY read error for %s: %v", s.name, err)
			}
			return
		}
	}
}

// writeLog appends PTY output to the log file, if it is still open.
func (s *session) writeLog(data []byte) {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	if s.logFile != nil {
		s.logFile.Write(data)
	}
}

// closeLog closes the log file once, whichever of readLoop and kill gets here first.
func (s *session) closeLog() {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	if s.logFile != nil {
		s.logFile.Close()
		s.logFile = nil
	}
}

// sendInput writes raw bytes to the PTY (terminal input).
func (s *session) sendInput(data []byte) error {
	_, err := s.writeFull(data)
	return err
}

// writeFull writes data once and reports a short write (fewer bytes than
// requested with a nil error) as io.ErrShortWrite instead of ignoring it. It
// returns how many bytes reached the PTY.
func (s *session) writeFull(data []byte) (int, error) {
	inputlog.Record(s.name, inputlog.Raw, string(data))
	n, err := s.proc.Write(data)
	if err == nil && n < len(data) {
		err = io.ErrShortWrite
	}
	return n, err
}

// resize changes the PTY window size.
func (s *session) resize(cols, rows uint16) error {
	if err := s.proc.Resize(cols, rows); err != nil {
		return err
	}
	s.mu.Lock()
	s.cols, s.rows = cols, rows
	s.mu.Unlock()
	return nil
}

// size is the PTY's size as last set.
func (s *session) size() (uint16, uint16) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cols, s.rows
}

// subscribe registers a subscriber for terminal output.
func (s *session) subscribe(id string) <-chan []byte {
	ch := make(chan []byte, 256)
	s.mu.Lock()
	s.subscribers[id] = ch
	s.mu.Unlock()
	return ch
}

// unsubscribe removes a subscriber.
func (s *session) unsubscribe(id string) {
	s.mu.Lock()
	if ch, ok := s.subscribers[id]; ok {
		close(ch)
		delete(s.subscribers, id)
	}
	s.mu.Unlock()
}

// replayBytes returns a copy of the ring buffer for reconnect replay.
func (s *session) replayBytes() []byte {
	s.ringMu.Lock()
	defer s.ringMu.Unlock()
	limit := ReplayBytes()
	data := s.ring
	if len(data) > limit {
		data = data[len(data)-limit:]
	}
	out := make([]byte, len(data))
	copy(out, data)
	return out
}

// kill terminates the session and all child processes.
func (s *session) kill() error {
	// Graceful termination
	if terminateTypesCtrlC {
		// On Windows, Terminate types Ctrl-C into the console (pty_windows.go).
		inputlog.Record(s.name, inputlog.Raw, "\x03")
	}
	s.proc.Terminate()

	// Wait for graceful exit (5s timeout)
	select {
	case <-s.proc.Done():
		// Clean exit
	case <-time.After(5 * time.Second):
		// Force kill
		s.proc.ForceKill()
		<-s.proc.Done()
	}

	// Close PTY handle
	s.proc.Close()

	// Close log file (may already be closed if readLoop did it on natural exit)
	s.closeLog()

	// Close all subscriber channels
	s.mu.Lock()
	for id, ch := range s.subscribers {
		close(ch)
		delete(s.subscribers, id)
	}
	s.mu.Unlock()

	return nil
}

// isRunning returns true if the process is still alive.
func (s *session) isRunning() bool {
	select {
	case <-s.proc.Done():
		return false
	default:
		return true
	}
}

// parseCommand splits a shell command string into parts.
// Handles simple quoting but delegates complex cases to the platform shell.
func parseCommand(cmd string) []string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return nil
	}
	// If the command contains shell metacharacters, wrap via platform shell
	if strings.ContainsAny(cmd, "|&;`$(){}[]<>!~*?#") || strings.Contains(cmd, "&&") {
		return shellWrap(cmd)
	}
	return strings.Fields(cmd)
}

// defaultShell returns the platform default shell command.
// Respects CORAL_SHELL env var for user shell preference, then
// falls back to platform defaults.
func defaultShell() []string {
	// User override via CORAL_SHELL (e.g. "pwsh.exe", "/usr/bin/zsh", "bash")
	if cs := os.Getenv("CORAL_SHELL"); cs != "" {
		return []string{cs}
	}
	if runtime.GOOS == "windows" {
		// Prefer PowerShell if available (more capable for CLI tools)
		for _, ps := range []string{"pwsh.exe", "powershell.exe"} {
			if _, err := exec.LookPath(ps); err == nil {
				return []string{ps}
			}
		}
		return []string{"cmd.exe"}
	}
	if sh := os.Getenv("SHELL"); sh != "" {
		return []string{sh}
	}
	return []string{"/bin/sh"}
}

var (
	bracketedPasteOn  = []byte("\x1b[?2004h")
	bracketedPasteOff = []byte("\x1b[?2004l")
)

// trackBracketedPaste records whether the application enabled bracketed paste,
// using the last enable/disable sequence in the output stream.
func (s *session) trackBracketedPaste(data []byte) {
	buf := append(s.bpCarry, data...)
	on, off := bytes.LastIndex(buf, bracketedPasteOn), bytes.LastIndex(buf, bracketedPasteOff)
	switch {
	case on > off:
		s.bracketedPaste.Store(true)
	case off > on:
		s.bracketedPaste.Store(false)
	}
	keep := len(bracketedPasteOn) - 1
	if len(buf) < keep {
		keep = len(buf)
	}
	s.bpCarry = append([]byte(nil), buf[len(buf)-keep:]...)
}
