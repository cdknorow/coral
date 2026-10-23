package ptymanager

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/subgentic/coral/internal/logsafe"
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
	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/safego"
	"github.com/subgentic/coral/internal/terminalinput"
	"github.com/subgentic/coral/internal/vtscreen"
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
	ended       bool            // the process has ended and its subscribers were closed (guarded by mu)
	cols, rows  uint16          // the PTY's size as last set (guarded by mu)

	// Ring buffer of recent output for snapshot on reconnect
	ringMu          sync.Mutex
	clearResetUntil time.Time // while the time is ahead, the next ESC[3J restarts the replay ring, once (armReplayClear); the zero time is not armed
	ring            []byte
	ringMax         int
	// screen is the terminal's own state, kept as the output arrives (#6481): a TUI that repaints only what changes leaves nothing of what it drew once in the tail the ring keeps.
	screen *vtscreen.Screen

	// bracketedPaste is the application's last DECSET/DECRST 2004 state seen in
	// its output; bpCarry holds a possible sequence split across reads (only
	// readLoop touches it).
	bracketedPaste atomic.Bool
	bpCarry        []byte

	// cursorKeysApp is the application's DECCKM state (DECSET 1): the arrow keys are then sent as ESC O x. ckCarry
	// holds a possible sequence split across reads (only readLoop touches it).
	cursorKeysApp atomic.Bool
	ckCarry       []byte

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
		log.Printf("Warning: could not open log file %s: %v", logsafe.Dir(logPath), err)
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
		screen:      vtscreen.NewScreen(int(cols), int(rows)),
	}

	// Start output reader goroutine
	go func() { defer safego.Recover("ptymanager/session:s.readLoop"); s.readLoop() }()
	// A ConPTY output pipe does not reach end of file when the child exits (only when the pseudo console is closed), so readLoop alone would leave a viewer on a
	// dead terminal. When the process is done, give its last output a moment to arrive, then close the subscribers.
	go func() {
		defer safego.Recover("ptymanager/session:s.watchExit")
		<-s.proc.Done()
		time.Sleep(exitDrain)
		s.closeSubscribers()
	}()

	return s, nil
}

// exitDrain is how long the last output of an ended process is given to reach its viewers before they are told the terminal closed.
const exitDrain = 300 * time.Millisecond

// readLoop reads PTY output and fans out to log file and subscribers.
// When the PTY closes (EOF), the log file is closed here to prevent FD leaks
// if the session exits naturally without kill() being called.
func (s *session) readLoop() {
	defer s.closeLog()
	// The process ended on its own (the shell's exit): the subscribers' channels close, so a viewer is told the terminal is closed (the stream sends
	// terminal_closed) instead of staying open on a dead terminal. kill closes them too; closing is once per subscriber.
	defer s.closeSubscribers()
	buf := make([]byte, 32*1024)
	for {
		n, err := s.proc.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])

			// Write to log file (for status display, FTS indexing)
			s.writeLog(data)

			s.trackBracketedPaste(data)
			s.trackCursorKeys(data)
			s.screen.Write(data)

			// Store in ring buffer for snapshot
			s.ringMu.Lock()
			s.ring = append(s.ring, data...)
			s.resetRingAtArmedClear(len(data))
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

// writeInput is the ONE function that writes input to the session's process (a census test fails on any other, #5706). It records the
// write as o's input, then writes data once and reports a short write (fewer bytes than requested with a nil error) as io.ErrShortWrite
// instead of ignoring it. It returns how many bytes reached the PTY. What is recorded is what tmux logs for the same input (#60 step 4):
// a paste (pasted is not nil) as a paste of the cleaned text, whatever markers surround it on the wire; anything else as typed input
// (terminalinput.RecordInput: text, an Enter or other key, an escape sequence). o is a parameter, not a value in a context: a caller must say
// whose input it is.
func (s *session) writeInput(o terminalinput.Origin, data []byte, pasted *string) (int, error) {
	if pasted != nil {
		terminalinput.Record(o, s.name, terminalinput.Paste, *pasted)
	} else {
		terminalinput.RecordInput(o, s.name, string(data))
	}
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
	s.screen.Resize(int(cols), int(rows))
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
	if s.ended { // the process has already ended: this viewer is told so at once (a closed channel), not left waiting
		s.mu.Unlock()
		close(ch)
		return ch
	}
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

// closeSubscribers closes every subscriber's channel and forgets them.
func (s *session) closeSubscribers() {
	s.mu.Lock()
	s.ended = true
	for id, ch := range s.subscribers {
		close(ch)
		delete(s.subscribers, id)
	}
	s.mu.Unlock()
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

// clearScrollback is the sequence that erases a terminal's scrollback (ESC[3J). A quiet launch script prints it first (cmdhandoff.QuietShellScript, #5911).
var clearScrollback = []byte("\x1b[3J")

// defaultReplayClearWindow is how long after a quiet launch is typed its script's ESC[3J may restart the replay ring.
const defaultReplayClearWindow = 30 * time.Second

// armReplayClear lets the NEXT ESC[3J this session prints, within window, restart the replay ring, once: Coral arms it when it types a quiet launch line (#5911). Output is
// never trusted to reset the replay by itself: a program in the terminal, an agent included, that prints ESC[3J must not be able to erase what came before it from what a person is
// shown on reconnect.
func (s *session) armReplayClear(window time.Duration) {
	s.ringMu.Lock()
	s.clearResetUntil = time.Now().Add(window)
	s.ringMu.Unlock()
}

// resetRingAtArmedClear, called with the ring holding the n bytes just read, restarts the ring at the clear in them when the session is armed and the window is open, and disarms it
// (the first clear is the script's; a later one, or one when not armed, changes nothing). An armed session whose window has passed is disarmed without effect.
func (s *session) resetRingAtArmedClear(n int) {
	if s.clearResetUntil.IsZero() {
		return
	}
	if time.Now().After(s.clearResetUntil) {
		s.clearResetUntil = time.Time{}
		return
	}
	if ring, ok := restartRingAtClear(s.ring, n); ok {
		s.ring = ring
		s.clearResetUntil = time.Time{}
	}
}

// restartRingAtClear drops what was in the replay ring before the last clear-scrollback sequence in the newly appended n bytes (or one that straddles them), keeping the sequence
// itself, so a terminal opened later is replayed what the screen shows now, not the short line that started the agent (#5911). ok is false, and the ring as it was, when there is none.
func restartRingAtClear(ring []byte, n int) (out []byte, ok bool) {
	start := len(ring) - n - len(clearScrollback) + 1
	if start < 0 {
		start = 0
	}
	i := bytes.LastIndex(ring[start:], clearScrollback)
	if i < 0 {
		return ring, false
	}
	kept := copy(ring, ring[start+i:])
	return ring[:kept], true
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
		terminalinput.RecordInput(terminalinput.Internal, s.name, "\x03") // a key, as tmux sends (and logs) it (#60 step 4)
		if n, ok := s.proc.(stopNamer); ok {
			n.setStopName(s.name) // so the second Ctrl-C and the exit line of the polite stop are recorded too
		}
	}
	s.proc.Terminate()

	// Wait for graceful exit
	started := time.Now()
	select {
	case <-s.proc.Done():
		// The shell is gone; what it started may not be (an agent in a group of its own, its helpers): they get the rest of the grace, then SIGKILL (#6125).
		if r, ok := s.proc.(interface{ reapTree(time.Duration) }); ok {
			r.reapTree(max(grace-time.Since(started), 0))
		}
	case <-time.After(grace):
		// Force kill
		s.proc.ForceKill()
		<-s.proc.Done()
		if r, ok := s.proc.(interface{ reapTree(time.Duration) }); ok {
			r.reapTree(0)
		}
	}

	// Close PTY handle
	s.proc.Close()

	// Close log file (may already be closed if readLoop did it on natural exit)
	s.closeLog()

	// Close all subscriber channels
	s.closeSubscribers()

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

// trackCursorKeys records the application's DECCKM state from the private-mode sequences in its output
// (ESC [ ? ... h to set, l to reset; mode 1 is the cursor keys), the last one winning.
func (s *session) trackCursorKeys(data []byte) {
	buf := append(s.ckCarry, data...)
	for i := 0; i+3 < len(buf); i++ {
		if buf[i] != 0x1b || buf[i+1] != '[' || buf[i+2] != '?' {
			continue
		}
		j := i + 3
		for j < len(buf) && (buf[j] == ';' || (buf[j] >= '0' && buf[j] <= '9')) {
			j++
		}
		if j >= len(buf) || (buf[j] != 'h' && buf[j] != 'l') {
			continue
		}
		for _, p := range strings.Split(string(buf[i+3:j]), ";") {
			if p == "1" {
				s.cursorKeysApp.Store(buf[j] == 'h')
			}
		}
		i = j
	}
	// An unfinished "ESC [ ? 1 ;" at the end waits for the next read.
	keep := min(len(buf), 24)
	tail := buf[len(buf)-keep:]
	if at := bytes.LastIndexByte(tail, 0x1b); at >= 0 {
		s.ckCarry = append([]byte(nil), tail[at:]...)
	} else {
		s.ckCarry = nil
	}
}
