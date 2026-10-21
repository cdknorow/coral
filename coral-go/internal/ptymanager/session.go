package ptymanager

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/subgentic/coral/internal/agentenv"
	"github.com/subgentic/coral/internal/inputlog"
	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/safego"
	"github.com/subgentic/coral/internal/winshell"
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
	lagged      map[string]bool // subscribers cut off for falling behind (#4161; guarded by mu)
	cols, rows  uint16          // the PTY's size as last set (guarded by mu)

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
		sh, err := defaultShell()
		if err != nil {
			return nil, err
		}
		parts = sh
	}

	env := sessionEnv(name)

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
		lagged:      make(map[string]bool),
		cols:        cols,
		rows:        rows,
		ring:        make([]byte, 0, 256*1024),
		ringMax:     256 * 1024, // 256KB ring buffer
	}

	// Start output reader goroutine
	go func() { defer safego.Recover("ptymanager/session:s.readLoop"); s.readLoop() }()

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

			// Fan out to WebSocket subscribers (non-blocking; one that fell behind is cut off)
			s.mu.Lock()
			fanOut(s.subscribers, s.lagged, data)
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
	return s.writeFullCtx(context.Background(), data)
}

// writeFullCtx is writeFull for input sent with ctx: a person's own (inputlog.Human) is logged by
// its length only (#60).
func (s *session) writeFullCtx(ctx context.Context, data []byte) (int, error) {
	inputlog.RecordCtx(ctx, s.name, inputlog.Raw, string(data))
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
	delete(s.lagged, id)
	s.mu.Unlock()
	return ch
}

// takeLagged reports, once, whether subscriber id was cut off for falling behind.
func (s *session) takeLagged(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	lagged := s.lagged[id]
	delete(s.lagged, id)
	return lagged
}

// unsubscribe removes a subscriber.
func (s *session) unsubscribe(id string) {
	s.mu.Lock()
	if ch, ok := s.subscribers[id]; ok {
		close(ch)
		delete(s.subscribers, id)
	}
	delete(s.lagged, id)
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
func (s *session) kill() error { return s.killWithin(5 * time.Second) }

// killWithin ends the session: SIGTERM, then a force kill if the process is still there after grace.
func (s *session) killWithin(grace time.Duration) error {
	// Graceful termination
	if terminateTypesCtrlC {
		// On Windows, Terminate types Ctrl-C into the console (pty_windows.go).
		inputlog.Record(s.name, inputlog.Raw, "\x03")
	}
	s.proc.Terminate()

	// Wait for graceful exit
	select {
	case <-s.proc.Done():
		// Clean exit
	case <-time.After(grace):
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

// sessionEnv is the environment of the agent named name. Coral's own server settings never reach the agent
// (#3389).
func sessionEnv(name string) []string {
	env := append(agentenv.Filter(os.Environ()),
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		fmt.Sprintf("CORAL_SESSION_NAME=%s", name),
	)
	// Ours replace the server's own entry of the same name (a block with the name twice is read by its first).
	extra := platformAgentEnv()
	kept := env[:0:0]
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		dup := false
		for _, e := range extra {
			if n, _, _ := strings.Cut(e, "="); strings.EqualFold(n, name) {
				dup = true
			}
		}
		if !dup {
			kept = append(kept, kv)
		}
	}
	return append(kept, extra...)
}

// parseCommand splits a shell command string into parts.
// Handles simple quoting but delegates complex cases to the platform shell.
func parseCommand(cmd string) []string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return nil
	}
	// Shell syntax (metacharacters, and quotes or a backslash, which a split on spaces would
	// break: "sh -c 'sleep 60'" became sh, -c, 'sleep, 60' and the shell exited at once, #4013)
	// goes to the platform shell; plain words are run as they are.
	if strings.ContainsAny(cmd, "|&;`$(){}[]<>!~*?#'\"\\") {
		return shellWrap(cmd)
	}
	return strings.Fields(cmd)
}

// errNoPowerShell: a Windows agent starts only through PowerShell (#3692), and none is installed.
var errNoPowerShell = errors.New("Coral starts Windows agents through PowerShell, and neither pwsh.exe nor powershell.exe was found on PATH: install PowerShell to launch agents")

var cmdShellIgnored sync.Once

// defaultShell returns the shell an agent's terminal starts (its launch line is typed into it).
// CORAL_SHELL picks it, except cmd.exe: Coral's launch line (the prompt argument, the long-command
// handoff) is PowerShell syntax on Windows, so cmd.exe is never an agent's shell (#3692) and a
// CORAL_SHELL naming it is ignored, logged once. On Windows the default is PowerShell, and with
// none installed the launch is refused rather than started in a shell it can't run in.
func defaultShell() ([]string, error) {
	sh, ignored, err := pickShell(runtime.GOOS, os.Getenv("CORAL_SHELL"), os.Getenv("SHELL"), exec.LookPath)
	if ignored {
		cmdShellIgnored.Do(func() {
			log.Printf("[pty] CORAL_SHELL=%q is ignored for agents: Coral starts Windows agents through PowerShell, since cmd.exe can't run their launch line", os.Getenv("CORAL_SHELL"))
		})
	}
	return sh, err
}

// pickShell is defaultShell's rule, with the environment and PATH lookup passed in.
func pickShell(goos, coralShell, userShell string, lookPath func(string) (string, error)) (shell []string, cmdIgnored bool, err error) {
	if coralShell != "" {
		base := strings.TrimSuffix(strings.ToLower(filepath.Base(strings.ReplaceAll(coralShell, `\`, "/"))), ".exe")
		switch {
		case base == "cmd":
			cmdIgnored = true
		case goos == "windows" && winshell.IsPowerShell(coralShell):
			// Found by winshell.Pick below, by its absolute path (#26 G1A).
		default:
			return []string{coralShell}, false, nil
		}
	}
	if goos == "windows" {
		if ps := winshell.Pick(coralShell, lookPath); ps != "" {
			return []string{ps}, cmdIgnored, nil
		}
		return nil, cmdIgnored, errNoPowerShell
	}
	if userShell != "" {
		return []string{userShell}, cmdIgnored, nil
	}
	return []string{"/bin/sh"}, cmdIgnored, nil
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
