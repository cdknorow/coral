// Package tmux provides a client for interacting with tmux sessions.
package tmux

import (
	"context"
	"errors"
	"fmt"
	"github.com/cdknorow/coral/internal/agentprio"
	"github.com/cdknorow/coral/internal/testguard"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/cdknorow/coral/internal/agentenv"
	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/inputlog"
	"github.com/cdknorow/coral/internal/logparse"
	"github.com/cdknorow/coral/internal/naming"
)

// Pane represents a tmux pane with its metadata.
type Pane struct {
	PaneTitle   string `json:"pane_title"`
	SessionName string `json:"session_name"`
	Target      string `json:"target"`
	CurrentPath string `json:"current_path"`
	SocketPath  string `json:"-"` // which tmux socket this pane was found on (empty = default)
}

// Client wraps tmux command execution.
type Client struct {
	// TmuxBin is the path to the tmux binary. Defaults to "tmux".
	// Read/write through resolveTmuxBin() so a user-installed tmux is picked
	// up without restarting the server.
	TmuxBin   string
	tmuxBinMu sync.Mutex
	// SocketPath is an explicit tmux socket path (-S flag).
	// If set, all commands use this socket for consistent session visibility
	// across different launch contexts (terminal vs app bundle).
	SocketPath string
	// FallbackToDefault enables checking the default tmux socket (no -S flag)
	// when no sessions are found on the primary socket. This provides backward
	// compatibility with sessions created before the fixed socket was introduced.
	FallbackToDefault bool
	// sessionSockets caches which socket each session was found on.
	// Populated by ListPanes, used by runForTarget to route commands.
	// Protected by socketsMu since multiple goroutines access this map.
	sessionSockets map[string]string
	socketsMu      sync.RWMutex

	// captureTargets caches the pane target CapturePane resolved for a session (#3439): a
	// capture is then one tmux exec (capture-pane) instead of two (list-panes first). Only
	// for a session id, whose tmux session name the target contains; a failed capture drops it.
	captureTargetsMu sync.Mutex
	captureTargets   map[string]string // agentName \x00 agentType \x00 sessionID -> target

	// paneStates is every pane's pid and size from one list-panes, kept for paneStateTTL
	// (#3847): the status pass asked each agent's pane for its pid and the feed each pane for
	// its size, one tmux run per agent each time.
	paneStatesMu  sync.Mutex
	paneStatesAt  time.Time
	paneStates    map[string]paneState // target (#S:#I.#P) -> state
	paneStatesGen uint64               // bumped by every command that changes a pane: a list started before it is not kept
}

// paneState is a pane's shell pid and size.
type paneState struct{ pid, cols, rows int }

// paneStateTTL is how long one list-panes answers the agents' pane pid and size questions.
const paneStateTTL = time.Second

// paneChanging are the tmux commands after which the shared pane list is stale: a pane's size
// (Coral's own resize must read back at once, v1.4.63's terminal size checks), its pid (a
// respawn) or the set of panes.
var paneChanging = map[string]bool{
	"resize-window": true, "resize-pane": true, "respawn-pane": true, "new-session": true,
	"new-window": true, "split-window": true, "kill-session": true, "kill-window": true, "kill-pane": true,
}

// forgetPaneStates drops the shared pane list after a command that changed a pane.
func (c *Client) forgetPaneStates() {
	c.paneStatesMu.Lock()
	c.paneStates = nil
	c.paneStatesGen++
	c.paneStatesMu.Unlock()
}

// resolveTmuxBin returns the path used to invoke tmux, re-checking the
// filesystem if the cached binary isn't reachable. This lets a tmux that
// was installed after the server started be picked up on the next command,
// rather than requiring a restart.
func (c *Client) resolveTmuxBin() string {
	c.tmuxBinMu.Lock()
	defer c.tmuxBinMu.Unlock()
	original := c.TmuxBin
	// Absolute path that still exists — keep using it.
	if filepath.IsAbs(c.TmuxBin) {
		_, err := os.Stat(c.TmuxBin)
		if err == nil {
			return c.TmuxBin
		}
		log.Printf("[tmux] cached absolute tmux path is not usable: path=%q err=%v", c.TmuxBin, err)
	} else if _, err := exec.LookPath(c.TmuxBin); err == nil {
		return c.TmuxBin
	} else {
		log.Printf("[tmux] cached tmux binary not found on PATH: bin=%q path=%q err=%v", c.TmuxBin, os.Getenv("PATH"), err)
	}
	// Cached path no longer works — re-search common locations.
	if p, ok := IsAvailable(); ok {
		c.TmuxBin = p
		if p != original {
			log.Printf("[tmux] resolved tmux binary: previous=%q resolved=%q path=%q", original, p, os.Getenv("PATH"))
		}
		return p
	}
	log.Printf("[tmux] tmux discovery failed; using unresolved binary: bin=%q path=%q shell=%q coral_tmux_bin=%q", c.TmuxBin, os.Getenv("PATH"), os.Getenv("SHELL"), os.Getenv("CORAL_TMUX_BIN"))
	return c.TmuxBin
}

// commonTmuxPaths are checked when tmux is not on PATH (native app bundles
// may not inherit a shell PATH that includes Homebrew).
var commonTmuxPaths = []string{
	"/opt/homebrew/bin/tmux",
	"/usr/local/bin/tmux",
	"/usr/bin/tmux",
	"/opt/local/bin/tmux",
	"/nix/var/nix/profiles/default/bin/tmux",
}

// IsAvailable reports whether tmux can be found on PATH or in a common
// install location. The returned path is the resolved binary (or "tmux"
// if it was found on PATH).
//
// Order: an explicit CORAL_TMUX_BIN override, then the tmux bundled with the
// macOS app (next to the running executable), then PATH, common install
// locations and a login shell lookup.
func IsAvailable() (string, bool) {
	if p := tmuxFromEnv(); p != "" {
		log.Printf("[tmux] discovery found tmux via CORAL_TMUX_BIN: %s", p)
		return p, true
	}
	if p, _ := bundledTmux(); p != "" {
		log.Printf("[tmux] discovery found bundled tmux: %s", p)
		return p, true
	}
	if p, err := exec.LookPath("tmux"); err == nil {
		log.Printf("[tmux] discovery found tmux on PATH: %s", p)
		return p, true
	}
	for _, p := range commonTmuxPaths {
		if executableFileExists(p) {
			log.Printf("[tmux] discovery found tmux in common path: %s", p)
			return p, true
		}
	}
	if p := tmuxFromLoginShell(); p != "" {
		log.Printf("[tmux] discovery found tmux via login shell: %s", p)
		return p, true
	}
	log.Printf("[tmux] discovery failed: path=%q shell=%q coral_tmux_bin=%q common_paths=%v", os.Getenv("PATH"), os.Getenv("SHELL"), os.Getenv("CORAL_TMUX_BIN"), commonTmuxPaths)
	return "", false
}

func tmuxFromEnv() string {
	p := strings.TrimSpace(os.Getenv("CORAL_TMUX_BIN"))
	if p == "" {
		return ""
	}
	if resolved, err := exec.LookPath(p); err == nil {
		return resolved
	}
	if executableFileExists(p) {
		return p
	}
	log.Printf("[tmux] CORAL_TMUX_BIN is set but not executable: %q", p)
	return ""
}

func tmuxFromLoginShell() string {
	if runtime.GOOS == "windows" {
		return ""
	}
	shells := []string{}
	if shell := strings.TrimSpace(os.Getenv("SHELL")); shell != "" {
		shells = append(shells, shell)
	}
	shells = append(shells, "/bin/zsh", "/bin/bash", "/bin/sh")

	seen := make(map[string]bool, len(shells))
	for _, shell := range shells {
		if shell == "" || seen[shell] || !executableFileExists(shell) {
			continue
		}
		seen[shell] = true

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		out, err := exec.CommandContext(ctx, shell, "-lc", "command -v tmux").Output()
		cancel()
		if err != nil {
			log.Printf("[tmux] login shell tmux lookup failed: shell=%q err=%v", shell, err)
			continue
		}
		p := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
		if p == "" {
			continue
		}
		if resolved, err := exec.LookPath(p); err == nil {
			return resolved
		}
		if executableFileExists(p) {
			return p
		}
	}
	return ""
}

func executableFileExists(path string) bool {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return false
	}
	return st.Mode()&0111 != 0
}

// NewClient creates a new tmux Client.
// coralDir is the root data directory (e.g. ~/.coral). The tmux socket is
// placed at <coralDir>/tmux.sock. Pass "" to use CORAL_TMUX_SOCKET env var
// or fall back to ~/.coral/tmux.sock.
func NewClient(coralDir ...string) *Client {
	c := &Client{TmuxBin: "tmux", sessionSockets: make(map[string]string)}

	// Find tmux binary if not on PATH (native app may not have Homebrew paths).
	if p, ok := IsAvailable(); ok {
		c.TmuxBin = p
	}

	if len(coralDir) > 0 && coralDir[0] != "" {
		c.SocketPath = filepath.Join(coralDir[0], "tmux.sock")
	} else if sp := os.Getenv("CORAL_TMUX_SOCKET"); sp != "" {
		c.SocketPath = sp
	} else {
		home, _ := os.UserHomeDir()
		if home != "" {
			c.SocketPath = filepath.Join(home, ".coral", "tmux.sock")
		}
	}
	testguard.CheckDataPath(c.SocketPath)
	c.FallbackToDefault = defaultSocketFallback(c.SocketPath)
	log.Printf("[tmux] NewClient: tmux_bin=%q socket=%q fallback=%v path=%q shell=%q coral_tmux_bin=%q", c.TmuxBin, c.SocketPath, c.FallbackToDefault, os.Getenv("PATH"), os.Getenv("SHELL"), os.Getenv("CORAL_TMUX_BIN"))
	return c
}

// defaultSocketFallback decides whether session discovery also reads tmux's
// default socket.
//
// The merge exists so that upgrading from a Coral old enough to have used the
// default socket does not lose sight of already-running agents. That only ever
// applies to an install rooted at the default ~/.coral. For any other data
// directory there is no upgrade to serve, and the merge is actively harmful:
// a second Coral instance — its own port, its own database, its own socket —
// still enumerates the first instance's sessions, and the session endpoints
// that can kill an agent take those same names. A user with two instances can
// kill the first one's agents from a dashboard that looks entirely like their
// own.
//
// So the merge is on only for a default-rooted install, and either environment
// variable overrides that decision explicitly.
func defaultSocketFallback(socketPath string) bool {
	if config.Truthy(os.Getenv("CORAL_TMUX_NO_FALLBACK")) {
		return false
	}
	if config.Truthy(os.Getenv("CORAL_TMUX_FALLBACK")) {
		return true
	}
	return socketPath == "" || socketPath == defaultSocketPath()
}

// defaultSocketPath is the socket an install rooted at ~/.coral uses.
func defaultSocketPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".coral", "tmux.sock")
}

// maxUnixSocketPath is the sun_path limit for unix domain sockets: 104 bytes
// on macOS and the BSDs, 108 on Linux. A socket path at or over the limit
// cannot be bound at all.
func maxUnixSocketPath() int {
	if runtime.GOOS == "linux" {
		return 108
	}
	return 104
}

// CheckSocketPath reports whether a tmux socket can actually be created for
// this data directory. An over-limit path fails at bind time with a bare
// "File name too long", which previously left the server logging that it was
// using the tmux backend while nothing tmux-related could work.
func CheckSocketPath(coralDir string) error {
	path := filepath.Join(coralDir, "tmux.sock")
	if limit := maxUnixSocketPath(); len(path) >= limit {
		return fmt.Errorf("tmux socket path is %d bytes, over this platform's %d-byte limit: %s",
			len(path), limit, path)
	}
	return nil
}

// ListPanes returns all tmux panes with their titles, session names, and targets.
// When FallbackToDefault is set it also reads tmux's default socket and merges
// the result, so an upgrade from a Coral that used the default socket still
// sees its running agents. See defaultSocketFallback for when that is enabled;
// it is off by default for any non-default data directory, because merging
// there lets one Coral instance discover and kill another's agents.
func (c *Client) ListPanes(ctx context.Context) ([]Pane, error) {
	panes := c.listPanesOnSocket(ctx, c.SocketPath)

	if c.FallbackToDefault && c.SocketPath != "" && defaultServerMayRun() {
		fallbackPanes := c.listPanesOnSocket(ctx, "")
		// Deduplicate by session name (prefer primary socket)
		seen := make(map[string]bool)
		for _, p := range panes {
			seen[p.SessionName] = true
		}
		for _, p := range fallbackPanes {
			if !seen[p.SessionName] {
				panes = append(panes, p)
			}
		}
	}

	// Cache which socket each session lives on. The map is rebuilt from this
	// listing so ended sessions do not accumulate; a session missing from it
	// is routed to the primary socket with the default socket as fallback.
	sockets := make(map[string]string, len(panes))
	for _, p := range panes {
		sockets[p.SessionName] = p.SocketPath
	}
	c.socketsMu.Lock()
	c.sessionSockets = sockets
	c.socketsMu.Unlock()

	return panes, nil
}

// defaultServerMayRun reports whether tmux's default socket may have a server: false only
// when its socket file is not there, so the fallback listing (a tmux run on every ListPanes,
// #3728) is skipped on a computer with no default tmux server. Inside tmux ($TMUX), or when the
// path can't be worked out or checked, it says yes, and the listing runs as before.
func defaultServerMayRun() bool {
	if os.Getenv("TMUX") != "" || runtime.GOOS == "windows" {
		return true
	}
	dir := os.Getenv("TMUX_TMPDIR")
	if dir == "" {
		dir = "/tmp"
	}
	_, err := os.Lstat(filepath.Join(dir, fmt.Sprintf("tmux-%d", os.Getuid()), "default"))
	return !errors.Is(err, fs.ErrNotExist)
}

func (c *Client) listPanesOnSocket(ctx context.Context, socketPath string) []Pane {
	// pane_title is LAST because it is free-form text controlled by the agent
	// process and can itself contain the '|' separator (Codex, for example, sets
	// "[ ! ] Action Required | coral" while waiting on approval). With the title
	// last and SplitN(…, 4), any extra '|' stays inside the title field instead
	// of shifting the session name and making the agent look dead.
	args := []string{"list-panes", "-a", "-F", "#{session_name}|#S:#I.#P|#{pane_current_path}|#{pane_title}"}
	if socketPath != "" {
		args = append(socketArgs(socketPath), args...)
	}
	if runObserver != nil {
		runObserver([]string{"list-panes"})
	}
	bin := c.resolveTmuxBin()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = tmuxEnv(bin)
	out, err := cmd.Output()
	noteTmuxResult(err, bin, socketPath)
	if err != nil {
		return nil
	}
	return parsePaneList(string(out), socketPath)
}

// parsePaneList parses the output of list-panes formatted as
// "session|target|path|title". The title is the final field and may contain
// additional '|' characters.
func parsePaneList(out, socketPath string) []Pane {
	var panes []Pane
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 4)
		if len(parts) != 4 {
			continue
		}
		panes = append(panes, Pane{
			SessionName: parts[0],
			Target:      parts[1],
			CurrentPath: parts[2],
			PaneTitle:   parts[3],
			SocketPath:  socketPath,
		})
	}
	return panes
}

// ErrTerminalGone means the agent's own terminal (its session-id pane) no longer exists.
// Nothing is sent to, read from or killed in any other pane in its place.
var ErrTerminalGone = errors.New("the agent's terminal is gone")

// FindPane finds the tmux pane of the agent with sessionID (or the exact session name
// "<type>-<id>" as agentName); nil when there is none.
func (c *Client) FindPane(ctx context.Context, agentName string, agentType string, sessionID string) (*Pane, error) {
	panes, err := c.ListPanes(ctx)
	if err != nil {
		return nil, err
	}

	// An exact session name ("claude-<uuid>", as board nudges, the notifier and the
	// launcher pass) names one session: treat it as that session's id.
	if sessionID == "" {
		if _, sid := logparse.ParseSessionName(agentName); sid != "" {
			sessionID = sid
		}
	}

	// With a session id, only that session's own pane will do. If it is gone, the agent's
	// terminal is gone: never fall back to a name match, which picks whichever pane happens
	// to share a folder, a name prefix or a substring (#3211: input, prompt answers and kills
	// for one agent reached another agent's pane).
	if sessionID != "" {
		// Exactly the "<type>-<sid>" session (#3214): the session id is the name's whole UUID,
		// never a substring of it, whatever the caller's id looks like.
		for i := range panes {
			if IsSessionNameFor(panes[i].SessionName, sessionID) {
				return &panes[i], nil
			}
		}
		return nil, nil
	}

	// Without a session id there is no pane to find (#3687). The old fallback matched a bare
	// name against pane titles, session names and folders by substring; an agent sets its own
	// pane's title (OSC 2), so it could be picked as another agent's pane for peek, send and
	// capture, and teammates share folders and name prefixes. No route passes a bare name any
	// more (#3214); one that does gets nothing, and the log says which name.
	warnBareNameLookup("tmux", agentName)
	return nil, nil
}

// IsSessionNameFor reports whether a terminal session name is exactly "<type>-<sessionID>"
// (agent types have no hyphen), case-insensitively. A session id that is only a substring
// of the name, such as a prefix of the UUID, does not match (#3214).
func IsSessionNameFor(sessionName, sessionID string) bool {
	name, sid := strings.ToLower(sessionName), strings.ToLower(sessionID)
	if sid == "" || !strings.HasSuffix(name, "-"+sid) {
		return false
	}
	agentType := name[:len(name)-len(sid)-1]
	return agentType != "" && !strings.Contains(agentType, "-")
}

// bareNameWarned keeps the deprecation warning to once per name per process.
var bareNameWarned sync.Map

// warnBareNameLookup logs that a terminal was looked up by bare name, without a session id
// (#3214). Every route now names the session, so this marks an internal caller to fix.
func warnBareNameLookup(backend, name string) {
	if _, seen := bareNameWarned.LoadOrStore(backend+"\x00"+name, true); !seen {
		log.Printf("[%s] deprecated: terminal %q looked up by bare name without a session id; pass the session id", backend, name)
	}
}

// WarnBareNameLookup is warnBareNameLookup for the other terminal backends.
func WarnBareNameLookup(backend, name string) { warnBareNameLookup(backend, name) }

// FindPaneTarget returns the tmux target address for a given agent.
func (c *Client) FindPaneTarget(ctx context.Context, agentName, agentType, sessionID string) (string, error) {
	pane, err := c.FindPane(ctx, agentName, agentType, sessionID)
	if err != nil {
		return "", err
	}
	if pane == nil {
		return "", nil
	}
	return pane.Target, nil
}

// SendKeys sends a command to a tmux pane. Multi-line text is wrapped in bracket paste.
func (c *Client) SendKeys(ctx context.Context, agentName, command, agentType, sessionID string) error {
	target, err := c.FindPaneTarget(ctx, agentName, agentType, sessionID)
	if err != nil {
		return err
	}
	if target == "" {
		return fmt.Errorf("%w: pane %q not found in any tmux session", ErrTerminalGone, agentName)
	}

	return c.SendKeysToTarget(ctx, target, command)
}

// ErrEnterWithheld is returned when a send typed its text but withheld the
// final Enter because the WithBeforeEnter check failed (#3431).
var ErrEnterWithheld = errors.New("the text was typed but its Enter was withheld: a prompt opened")

type beforeEnterKey struct{}

// WithBeforeEnter installs a check run just before a send's final Enter (after
// its text or paste). If it reports false, the Enter is not sent and the send
// returns ErrEnterWithheld: the automated-input gate uses it so a permission
// prompt that opens between the text and the Enter doesn't take the Enter.
func WithBeforeEnter(ctx context.Context, ok func(context.Context) bool) context.Context {
	return context.WithValue(ctx, beforeEnterKey{}, ok)
}

// EnterAllowed runs ctx's WithBeforeEnter check (true when there is none).
func EnterAllowed(ctx context.Context) bool { return enterAllowed(ctx) }

// enterAllowed runs ctx's WithBeforeEnter check (true when there is none).
func enterAllowed(ctx context.Context) bool {
	ok, _ := ctx.Value(beforeEnterKey{}).(func(context.Context) bool)
	return ok == nil || ok(ctx)
}

// sendKeysSettle is the pause between typing a command and its Enter.
var sendKeysSettle = 300 * time.Millisecond

// SetSendKeysSettleForTest shortens the pause SendKeysToTarget takes before
// Enter and returns a func that restores it. Call it from TestMain, before
// any test runs: the value is read without a lock.
func SetSendKeysSettleForTest(d time.Duration) (restore func()) {
	old := sendKeysSettle
	sendKeysSettle = d
	return func() { sendKeysSettle = old }
}

// SendKeysToTarget sends a command to a specific tmux target.
// Multi-line text uses tmux paste-buffer -p which respects the pane's
// bracketed paste mode — if the shell has it enabled, tmux wraps the
// paste in \e[200~...\e[201~; otherwise it sends raw text.
func (c *Client) SendKeysToTarget(ctx context.Context, target, command string) error {
	originalCommandLen := len(command)
	cleanup := func() {}
	if len(command) > inlineCommandLimit {
		var err error
		command, cleanup, err = commandInvocation(command)
		if err != nil {
			return err
		}
	}
	var sendErr error
	if strings.Contains(command, "\n") {
		sendErr = c.pasteToTarget(ctx, target, command)
	} else {
		// "--": text starting with '-' ("- fix the tests", "--help") is typed, not read as
		// tmux options (#3690).
		_, sendErr = c.run(ctx, "send-keys", "-t", target, "-l", "--", command)
	}
	if sendErr != nil {
		// No Enter after a failed send: it would submit whatever was already in the agent's
		// input instead of this text (#3690).
		cleanup()
		log.Printf("[tmux] send-keys error (target=%s, cmd_len=%d): %v", target, originalCommandLen, sendErr)
		return fmt.Errorf("send-keys failed: %w", sendErr)
	}

	// Brief pause for tmux to deliver keystrokes
	time.Sleep(sendKeysSettle)

	if !enterAllowed(ctx) {
		cleanup()
		return ErrEnterWithheld
	}

	if _, err := c.run(ctx, "send-keys", "-t", target, "Enter"); err != nil {
		cleanup()
		return fmt.Errorf("send Enter failed: %w", err)
	}
	return nil
}

// pasteToTarget loads text into a tmux buffer and pastes it into the target pane.
// Uses paste-buffer -p which respects the pane's bracketed paste mode setting.
func (c *Client) pasteToTarget(ctx context.Context, target, text string) error {
	inputlog.Record(target, inputlog.Paste, text)
	// Load text into tmux buffer via stdin
	args := []string{"load-buffer", "-"}
	socketPath := c.SocketPath
	if socketPath != "" {
		args = append(socketArgs(socketPath), args...)
	}
	bin := c.resolveTmuxBin()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = tmuxEnv(bin)
	cmd.Stdin = strings.NewReader(text)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("load-buffer failed: %w", err)
	}

	// Paste buffer into the target pane (-p = bracket paste aware)
	if _, err := c.run(ctx, "paste-buffer", "-t", target, "-p"); err != nil {
		return fmt.Errorf("paste-buffer failed: %w", err)
	}
	return nil
}

// SendRawKeys sends raw tmux key names (e.g. "BTab", "Escape") to a pane.
func (c *Client) SendRawKeys(ctx context.Context, agentName string, keys []string, agentType, sessionID string) error {
	target, err := c.FindPaneTarget(ctx, agentName, agentType, sessionID)
	if err != nil {
		return err
	}
	if target == "" {
		return fmt.Errorf("%w: pane %q not found in any tmux session", ErrTerminalGone, agentName)
	}

	for _, key := range keys {
		if _, err := c.run(ctx, "send-keys", "-t", target, key); err != nil {
			return fmt.Errorf("send-keys %q failed: %w", key, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil
}

// CapturePane captures the current content of a tmux pane.
func (c *Client) CapturePane(ctx context.Context, agentName string, lines int, agentType, sessionID string) (string, error) {
	return c.capturePane(ctx, agentName, lines, agentType, sessionID)
}

// CapturePaneStyled is CapturePane with each cell's style kept as SGR sequences
// (capture-pane -e): the automated-input gate tells Claude's faint prompt suggestion
// from text a person typed by it (#3468).
func (c *Client) CapturePaneStyled(ctx context.Context, agentName string, lines int, agentType, sessionID string) (string, error) {
	return c.capturePane(ctx, agentName, lines, agentType, sessionID, "-e")
}

func (c *Client) capturePane(ctx context.Context, agentName string, lines int, agentType, sessionID string, flags ...string) (string, error) {
	key := agentName + "\x00" + agentType + "\x00" + sessionID
	args := func(target string) []string {
		return append([]string{"capture-pane", "-t", target, "-p", fmt.Sprintf("-S-%d", lines)}, flags...)
	}
	if sessionID != "" {
		if cached, ok := c.captureTarget(key); ok {
			if out, err := c.run(ctx, args(cached)...); err == nil {
				return out, nil
			}
			c.setCaptureTarget(key, "") // the pane is gone or moved: look it up again
		}
	}
	target, err := c.FindPaneTarget(ctx, agentName, agentType, sessionID)
	if err != nil {
		return "", err
	}
	if target == "" {
		return "", nil
	}
	out, err := c.run(ctx, args(target)...)
	if err != nil {
		return "", nil
	}
	if sessionID != "" {
		c.setCaptureTarget(key, target)
	}
	return out, nil
}

// captureTargetsMax caps the target cache; it starts over when full (many short-lived agents).
const captureTargetsMax = 256

func (c *Client) captureTarget(key string) (string, bool) {
	c.captureTargetsMu.Lock()
	defer c.captureTargetsMu.Unlock()
	t, ok := c.captureTargets[key]
	return t, ok
}

// setCaptureTarget caches target for key, or forgets key when target is "".
func (c *Client) setCaptureTarget(key, target string) {
	c.captureTargetsMu.Lock()
	defer c.captureTargetsMu.Unlock()
	if target == "" {
		delete(c.captureTargets, key)
		return
	}
	if c.captureTargets == nil || len(c.captureTargets) >= captureTargetsMax {
		c.captureTargets = map[string]string{}
	}
	c.captureTargets[key] = target
}

// forgetCaptureTargets drops the cached targets of a session that is being killed.
func (c *Client) forgetCaptureTargets(sessionID string) {
	if sessionID == "" {
		return
	}
	c.captureTargetsMu.Lock()
	defer c.captureTargetsMu.Unlock()
	for k := range c.captureTargets {
		if strings.HasSuffix(k, "\x00"+sessionID) {
			delete(c.captureTargets, k)
		}
	}
}

// CapturePaneTarget captures pane content by target address.
func (c *Client) CapturePaneTarget(ctx context.Context, target string, lines int) (string, error) {
	stdout, err := c.run(ctx, "capture-pane", "-t", target, "-p", fmt.Sprintf("-S-%d", lines))
	if err != nil {
		return "", nil
	}
	return stdout, nil
}

// CapturePaneRawTarget captures pane content with ANSI sequences preserved.
// If visibleOnly is true, captures only the visible viewport (no scrollback).
// This is needed for TUI apps (vim, nano) that use the alternate screen buffer.
func (c *Client) CapturePaneRawTarget(ctx context.Context, target string, lines int, visibleOnly ...bool) (string, error) {
	args := []string{"capture-pane", "-t", target, "-p", "-e"}
	if len(visibleOnly) == 0 || !visibleOnly[0] {
		args = append(args, fmt.Sprintf("-S-%d", lines))
	}
	stdout, err := c.run(ctx, args...)
	if err != nil {
		return "", nil
	}
	return stdout, nil
}

// DisplayMessage runs tmux display-message to query pane state variables.
// Returns the formatted output string.
func (c *Client) DisplayMessage(ctx context.Context, target, format string) (string, error) {
	return c.run(ctx, "display-message", "-t", target, "-p", format)
}

// DisplayForAgent is DisplayMessage on an agent's own pane, found through the pane-target
// cache capturePane keeps (#3728): one tmux run while the pane stays where it was, instead of
// listing every pane (twice with the default-socket fallback) first. It returns ErrTerminalGone
// when the pane is gone.
func (c *Client) DisplayForAgent(ctx context.Context, agentName, agentType, sessionID, format string) (string, error) {
	key := agentName + "\x00" + agentType + "\x00" + sessionID
	if sessionID != "" {
		if cached, ok := c.captureTarget(key); ok {
			if out, err := c.DisplayMessage(ctx, cached, format); err == nil {
				return out, nil
			}
			c.setCaptureTarget(key, "") // the pane is gone or moved: look it up again
		}
	}
	target, err := c.FindPaneTarget(ctx, agentName, agentType, sessionID)
	if err != nil {
		return "", err
	}
	if target == "" {
		return "", fmt.Errorf("%w: pane %q not found in any tmux session", ErrTerminalGone, agentName)
	}
	out, err := c.DisplayMessage(ctx, target, format)
	if err != nil {
		return "", err
	}
	if sessionID != "" {
		c.setCaptureTarget(key, target)
	}
	return out, nil
}

// allPaneStates is every pane on the primary socket by target, from one list-panes at most
// every paneStateTTL.
func (c *Client) allPaneStates(ctx context.Context) map[string]paneState {
	c.paneStatesMu.Lock()
	if c.paneStates != nil && time.Since(c.paneStatesAt) < paneStateTTL {
		defer c.paneStatesMu.Unlock()
		return c.paneStates
	}
	gen := c.paneStatesGen
	c.paneStatesMu.Unlock()
	out, err := c.run(ctx, "list-panes", "-a", "-F", "#S:#I.#P|#{pane_pid}|#{pane_width}|#{pane_height}")
	if err != nil {
		return nil
	}
	states := map[string]paneState{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(line, "|")
		if len(f) != 4 {
			continue
		}
		pid, e1 := strconv.Atoi(f[1])
		cols, e2 := strconv.Atoi(f[2])
		rows, e3 := strconv.Atoi(f[3])
		if e1 != nil || e2 != nil || e3 != nil {
			continue
		}
		states[f[0]] = paneState{pid: pid, cols: cols, rows: rows}
	}
	c.paneStatesMu.Lock()
	if c.paneStatesGen == gen { // no pane changed while it ran: keep it for the next callers
		c.paneStates, c.paneStatesAt = states, time.Now()
	}
	c.paneStatesMu.Unlock()
	return states
}

// agentPaneState is the agent's pane state from the shared list, when its pane target is known
// (the capture cache) and the list has it; otherwise false, and the caller asks the pane itself.
func (c *Client) agentPaneState(ctx context.Context, agentName, agentType, sessionID string) (paneState, bool) {
	if sessionID == "" {
		return paneState{}, false
	}
	target, ok := c.captureTarget(agentName + "\x00" + agentType + "\x00" + sessionID)
	if !ok || target == "" {
		return paneState{}, false
	}
	st, ok := c.allPaneStates(ctx)[target]
	return st, ok
}

// PaneSizeForAgent is the agent's pane size: from the shared pane list when it has the pane,
// else from the pane itself. It returns ErrTerminalGone when the pane is gone.
func (c *Client) PaneSizeForAgent(ctx context.Context, agentName, agentType, sessionID string) (cols, rows int, err error) {
	if st, ok := c.agentPaneState(ctx, agentName, agentType, sessionID); ok && st.cols > 0 && st.rows > 0 {
		return st.cols, st.rows, nil
	}
	out, err := c.DisplayForAgent(ctx, agentName, agentType, sessionID, "#{pane_width} #{pane_height}")
	if err != nil {
		return 0, 0, err
	}
	if _, err := fmt.Sscanf(strings.TrimSpace(out), "%d %d", &cols, &rows); err != nil || cols <= 0 || rows <= 0 {
		return 0, 0, fmt.Errorf("pane size %q: unexpected tmux output", strings.TrimSpace(out))
	}
	return cols, rows, nil
}

// KillSession kills the tmux session for a given agent.
func (c *Client) KillSession(ctx context.Context, agentName, agentType, sessionID string) error {
	if err := c.killTmuxSession(ctx, agentName, agentType, sessionID); err != nil {
		return err
	}

	// Clean up log and settings files
	if sessionID != "" {
		logPath := naming.LogFile(naming.DefaultLogDir(), agentType, sessionID)
		os.Remove(logPath)

		settingsFile := filepath.Join(os.TempDir(), fmt.Sprintf("coral_settings_%s.json", sessionID))
		os.Remove(settingsFile)
	}

	return nil
}

// KillSessionOnly kills a tmux session without cleaning up log/settings files.
// Used by sleep to preserve state for later wake.
func (c *Client) KillSessionOnly(ctx context.Context, agentName, agentType, sessionID string) error {
	return c.killTmuxSession(ctx, agentName, agentType, sessionID)
}

// killTmuxSession finds and kills a tmux session by agent identity.
func (c *Client) killTmuxSession(ctx context.Context, agentName, agentType, sessionID string) error {
	c.forgetCaptureTargets(sessionID)
	pane, err := c.FindPane(ctx, agentName, agentType, sessionID)
	if err != nil {
		return err
	}
	if pane == nil {
		return fmt.Errorf("%w: pane %q not found in any tmux session", ErrTerminalGone, agentName)
	}

	_, err = c.run(ctx, "kill-session", "-t", pane.SessionName)
	if err != nil {
		return fmt.Errorf("kill-session failed: %w", err)
	}
	return nil
}

// ResizePane resizes a tmux pane width.
func (c *Client) ResizePane(ctx context.Context, agentName string, columns int, agentType, sessionID string) error {
	target, err := c.FindPaneTarget(ctx, agentName, agentType, sessionID)
	if err != nil {
		return err
	}
	if target == "" {
		return fmt.Errorf("%w: pane %q not found in any tmux session", ErrTerminalGone, agentName)
	}

	_, err = c.run(ctx, "resize-window", "-t", target, "-x", fmt.Sprintf("%d", columns))
	return err
}

// HasSession checks if a tmux session with the given name exists.
func (c *Client) HasSession(ctx context.Context, name string) bool {
	_, err := c.run(ctx, "has-session", "-t", name)
	return err == nil
}

// NewSession creates a new detached tmux session.
// It automatically disables bracketed paste mode to prevent '00~' characters
// from being prepended to commands sent via send-keys.
func (c *Client) NewSession(ctx context.Context, name, workDir string) error {
	log.Printf("[tmux] NewSession requested: name=%q work_dir=%q tmux_bin=%q socket=%q path=%q", name, workDir, c.TmuxBin, c.SocketPath, os.Getenv("PATH"))
	c.scrubServerEnv(ctx)
	out, err := c.run(ctx, "new-session", "-d", "-P", "-F", "#{pane_pid}", "-s", name, "-c", workDir)
	if err != nil {
		return err
	}
	// The pane and everything started in it run at the agents' lower priority
	// (#3437); the tmux server itself keeps the server's.
	applyAgentPriority(out)
	c.run(ctx, "set-option", "-t", name, "history-limit", "50000")
	return nil
}

// applyAgentPriority lowers the priority of the pane process whose pid tmux
// printed (#3437). A renice in the pane's first moments can be lost while
// tmux's freshly forked child is still setting up (seen on macOS, about 1 in
// 15), so it is applied again shortly after, to the pane and anything it has
// started by then, so a child forked before the renice isn't missed.
func applyAgentPriority(out string) {
	pid, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return
	}
	agentprio.Apply(pid)
	time.AfterFunc(agentPriorityRecheck, func() { agentprio.ApplyTree(pid) })
}

// agentPriorityRecheck is when the pane's priority is applied a second time.
var agentPriorityRecheck = 150 * time.Millisecond

// LowerExistingPanes lowers the priority of every pane already running on this
// client's own socket, and their children, once at startup: a tmux server from
// before #3437 started them at normal priority. Only our socket, never the
// user's default server: the list goes straight to our socket, without run's
// fallback, so when Coral's tmux server isn't running yet (after a reboot) it
// lowers nothing rather than the user's own panes.
func (c *Client) LowerExistingPanes(ctx context.Context) int {
	if c.SocketPath == "" {
		return 0
	}
	out, err := c.runOnSocket(ctx, c.SocketPath, "list-panes", "-a", "-F", "#{pane_pid}")
	if err != nil {
		return 0
	}
	n := 0
	for _, f := range strings.Fields(out) {
		if pid, err := strconv.Atoi(f); err == nil {
			n += agentprio.ApplyTree(pid)
		}
	}
	return n
}

// scrubServerEnv keeps Coral's server-only settings out of new agent panes (#3389). A tmux
// server started by Coral already has the agent environment (tmuxEnv), but one that was
// already running (started by an older Coral, or by the user on the same socket) keeps the
// environment it started with, and new panes get its global environment. Each such
// CORAL_ variable is marked to be removed before a new pane starts (set-environment -g -r).
// No server yet: nothing to do, new-session starts one with the filtered environment.
func (c *Client) scrubServerEnv(ctx context.Context) {
	if c.SocketPath != "" && !c.FallbackToDefault {
		if _, err := os.Stat(c.SocketPath); err != nil {
			return // no server on this socket yet (and no log line for asking)
		}
	}
	out, err := c.run(ctx, "show-environment", "-g")
	if err != nil {
		return
	}
	identity := map[string]bool{}
	for _, k := range agentenv.IdentityVars {
		identity[k] = true
	}
	have := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		name, value, _ := strings.Cut(strings.TrimSpace(line), "=")
		have[name] = value
		// "-NAME" is already marked for removal; CORAL_TMUX_BIN is set on purpose (tmuxEnv).
		// An agent identity is never the server's (#3421): removed like a server setting.
		if !strings.HasPrefix(name, "CORAL_") || name == "CORAL_TMUX_BIN" || (agentenv.Keep(name) && !identity[name]) {
			continue
		}
		if _, err := c.run(ctx, "set-environment", "-g", "-r", name); err != nil {
			log.Printf("[tmux] could not keep %s out of new panes: %v", name, err)
		}
	}
	// Where the server and its data are: this Coral's own values (startup set them, #3421),
	// not those of whatever started the tmux server.
	for _, k := range agentenv.LocationVars {
		v, set := os.LookupEnv(k)
		old, had := have[k]
		switch {
		case set && (!had || old != v):
			if _, err := c.run(ctx, "set-environment", "-g", "--", k, v); err != nil { // "--": hygiene, as for send-keys
				log.Printf("[tmux] could not set %s for new panes: %v", k, err)
			}
		case !set && had:
			if _, err := c.run(ctx, "set-environment", "-g", "-r", k); err != nil {
				log.Printf("[tmux] could not keep %s out of new panes: %v", k, err)
			}
		}
	}
}

// SetEnvironment sets an environment variable for a tmux session.
func (c *Client) SetEnvironment(ctx context.Context, session, key, value string) error {
	_, err := c.run(ctx, "set-environment", "-t", session, "--", key, value) // "--": hygiene (tmux stops at the name; #3690)
	return err
}

// PipePane sets up pipe-pane logging for a tmux session.
func (c *Client) PipePane(ctx context.Context, target, logFile string) error {
	_, err := c.run(ctx, "pipe-pane", "-t", target, "-o", pipePaneCommand(logFile))
	return err
}

// pipePaneCommand is the shell command tmux runs for pipe-pane: the log path as one quoted word,
// so an apostrophe in the data folder (/home/o'brien) can't end the quote and run the rest of
// the name (#3930, Security's #3927 finding 4).
func pipePaneCommand(logFile string) string {
	return "cat >> " + shellQuote(logFile)
}

// ClosePipePane closes the existing pipe-pane for a target (kills the cat process).
func (c *Client) ClosePipePane(ctx context.Context, target string) error {
	_, err := c.run(ctx, "pipe-pane", "-t", target)
	return err
}

// RenameSession renames a tmux session.
func (c *Client) RenameSession(ctx context.Context, oldName, newName string) error {
	_, err := c.run(ctx, "rename-session", "-t", oldName, newName)
	return err
}

// RespawnPane kills the running process in a pane and spawns a fresh shell.
func (c *Client) RespawnPane(ctx context.Context, target, workDir string) error {
	args := []string{"respawn-pane", "-k", "-t", target}
	if workDir != "" {
		args = append(args, "-c", workDir)
	}
	if _, err := c.run(ctx, args...); err != nil {
		return err
	}
	if out, err := c.DisplayMessage(ctx, target, "#{pane_pid}"); err == nil {
		applyAgentPriority(out) // #3437
	}
	return nil
}

// ClearHistory clears the tmux pane scrollback history.
func (c *Client) ClearHistory(ctx context.Context, target string) error {
	_, err := c.run(ctx, "clear-history", "-t", target)
	return err
}

// SendTerminalInputToTarget sends raw terminal input data to a resolved tmux target.
// Handles control characters, escape sequences, and multi-line text.
func (c *Client) SendTerminalInputToTarget(ctx context.Context, target, data string) error {
	// Control character map
	ctrlMap := map[string]string{
		"\r":   "Enter",
		"\x7f": "BSpace",
		"\x1b": "Escape",
		"\t":   "Tab",
	}

	// Single control character
	if len(data) == 1 {
		if key, ok := ctrlMap[data]; ok {
			_, err := c.run(ctx, "send-keys", "-t", target, key)
			return err
		}
		// Ctrl+<letter> (0x01-0x1a)
		b := data[0]
		if b >= 1 && b <= 26 {
			key := fmt.Sprintf("C-%c", b+96)
			_, err := c.run(ctx, "send-keys", "-t", target, key)
			return err
		}
	}

	// Escape sequences (arrow keys, function keys, etc.)
	if len(data) > 0 && data[0] == '\x1b' {
		hexArgs := []string{"send-keys", "-t", target}
		for _, b := range []byte(data) {
			hexArgs = append(hexArgs, "-H", fmt.Sprintf("%02x", b))
		}
		_, err := c.run(ctx, hexArgs...)
		return err
	}

	// Text that ends in a line break ("ls\r"): the Enter is a key press, not part of the
	// text. Pasted, the \r stays inside the bracketed paste and zsh (and any readline
	// shell that honours bracketed paste) takes it as pasted text: the command sits on
	// the line, unsubmitted. Send the text, then Enter as a key.
	if body := strings.TrimRight(data, "\r\n"); body != data {
		if body != "" {
			if err := c.SendTerminalInputToTarget(ctx, target, body); err != nil {
				return err
			}
		}
		enters := strings.Count(data[len(body):], "\r")
		if enters == 0 {
			enters = 1 // a bare \n submits too
		}
		for i := 0; i < enters; i++ {
			if _, err := c.run(ctx, "send-keys", "-t", target, "Enter"); err != nil {
				return err
			}
		}
		return nil
	}

	// Multi-line text: use tmux paste-buffer -p (bracket paste aware)
	if strings.ContainsAny(data, "\n\r") {
		return c.pasteToTarget(ctx, target, data)
	}

	// Single-line literal text ("--": a leading '-' is text, not a tmux option, #3690)
	_, err := c.run(ctx, "send-keys", "-t", target, "-l", "--", data)
	return err
}

// AttachCommand returns the full tmux attach command string including -S flag
// if a custom socket path is configured. Used for display and osascript.
//
// It names the binary this client actually uses: plain "tmux" for a tmux on
// PATH (unchanged output), otherwise its quoted absolute path, with the
// bundled terminfo directory prefixed when the bundled tmux is selected, so a
// pasted command attaches with the same tmux that created the session.
func (c *Client) AttachCommand(sessionName string) string {
	bin := c.attachBinary()
	prefix := ""
	if terminfoDir, bundled := isBundledBinary(c.resolveTmuxBin()); bundled && terminfoDir != "" {
		// Keep the user's whole list; add the bundle only if it is not there.
		dirs := terminfoDir
		if existing := os.Getenv("TERMINFO_DIRS"); existing != "" {
			dirs = existing
			if !containsPathEntry(existing, terminfoDir) {
				dirs = existing + ":" + terminfoDir
			}
		}
		prefix = "TERMINFO_DIRS=" + quoteIfNeeded(dirs) + " "
	}
	if c.SocketPath != "" {
		return fmt.Sprintf("%s%s -S %s attach -t %s", prefix, bin, quoteIfNeeded(c.SocketPath), quoteIfNeeded(sessionName))
	}
	return fmt.Sprintf("%s%s attach -t %s", prefix, bin, quoteIfNeeded(sessionName))
}

// attachBinary is how the attach command names tmux: bare "tmux" when the
// selected tmux is just "tmux" (or found as "tmux" on PATH), else its quoted path.
func (c *Client) attachBinary() string {
	bin := c.resolveTmuxBin()
	if bin == "" || bin == "tmux" {
		return "tmux"
	}
	if p, err := exec.LookPath("tmux"); err == nil && sameFile(p, bin) {
		return "tmux"
	}
	return quoteIfNeeded(bin)
}

// safeShellWord is the set of values that need no quoting in a shell command.
var safeShellWord = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// quoteIfNeeded leaves plain words as they are (so existing attach commands
// read the same) and passes everything else, including spaces, quotes, newlines
// and any other shell metacharacter, through the always-quoting shellQuote.
func quoteIfNeeded(v string) string {
	// "=", "%" and "@" are plain inside a word, not at its start ("=curl" is curl's path in zsh;
	// #3930, Security's #3927 finding 3).
	if safeShellWord.MatchString(v) && strings.IndexByte("=%@", v[0]) < 0 {
		return v
	}
	return shellQuote(v)
}

// SetPaneTitle sets a tmux pane's title using select-pane -T.
// This is a native tmux command that doesn't go through the shell,
// avoiding printf echo issues.
func (c *Client) SetPaneTitle(ctx context.Context, target, title string) {
	c.run(ctx, "select-pane", "-t", target, "-T", title)
}

// GetPanePID returns the shell PID running in the first pane of the given session.
// PanePIDs is the process of every pane on the socket (#3444).
func (c *Client) PanePIDs(ctx context.Context) ([]int, error) {
	out, err := c.run(ctx, "list-panes", "-a", "-F", "#{pane_pid}")
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if pid, err := strconv.Atoi(strings.TrimSpace(line)); err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

func (c *Client) GetPanePID(ctx context.Context, sessionName string) (int, error) {
	out, err := c.run(ctx, "list-panes", "-t", sessionName, "-F", "#{pane_pid}")
	if err != nil {
		return 0, err
	}
	line := strings.TrimSpace(strings.Split(out, "\n")[0])
	pid, err := strconv.Atoi(line)
	if err != nil {
		return 0, fmt.Errorf("invalid pane pid %q: %w", line, err)
	}
	return pid, nil
}

// ResizePaneTarget resizes a tmux pane by target address (both width and height).
func (c *Client) ResizePaneTarget(ctx context.Context, target string, columns, rows int) error {
	args := []string{"resize-window", "-t", target, "-x", fmt.Sprintf("%d", columns)}
	if rows > 0 {
		args = append(args, "-y", fmt.Sprintf("%d", rows))
	}
	_, err := c.run(ctx, args...)
	return err
}

// run executes a tmux command. If the args target a session (-t flag),
// it routes to the correct socket using the session cache.
// runObserver, when set (tests only), sees every tmux command run and listPanesOnSocket issue. It is
// read without a lock: a test sets it before starting anything that runs tmux and clears it after.
var runObserver func(args []string)

func (c *Client) run(ctx context.Context, args ...string) (string, error) {
	recordSendKeys(args)
	if runObserver != nil {
		runObserver(args)
	}
	if len(args) > 0 && paneChanging[args[0]] {
		// After it has run, so a list taken while it ran isn't kept either (the generation).
		defer c.forgetPaneStates()
	}
	// Check if this command targets a session — route via cache
	if c.FallbackToDefault && c.SocketPath != "" {
		for i, arg := range args {
			if arg == "-t" && i+1 < len(args) {
				target := args[i+1]
				session := sessionFromTarget(target)
				c.socketsMu.RLock()
				socket, ok := c.sessionSockets[session]
				c.socketsMu.RUnlock()
				if ok && socket != c.SocketPath {
					return c.runOnSocket(ctx, socket, args...)
				}
				break
			}
		}
	}
	// Default: use primary socket, fallback on error
	out, err := c.runOnSocket(ctx, c.SocketPath, args...)
	if err != nil && c.FallbackToDefault && c.SocketPath != "" {
		if fallbackOut, fallbackErr := c.runOnSocket(ctx, "", args...); fallbackErr == nil {
			return fallbackOut, nil
		}
	}
	return out, err
}

// runOnSocket runs a tmux command on a specific socket. Empty socketPath uses the default.
func (c *Client) runOnSocket(ctx context.Context, socketPath string, args ...string) (string, error) {
	if socketPath != "" {
		args = append(socketArgs(socketPath), args...)
	}
	bin := c.resolveTmuxBin()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = tmuxEnv(bin)
	out, err := cmd.Output()
	noteTmuxResult(err, bin, socketPath)
	if err != nil {
		log.Printf("[tmux] exec failed: bin=%q socket=%q args=%q err=%v stderr=%q", bin, socketPath, args, err, execStderr(err))
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// execStderrMax caps how much of tmux's stderr a failure log line carries.
const execStderrMax = 300

// execStderr returns what tmux wrote to stderr before failing, as one capped line,
// so an exec failure explains itself ("server exited unexpectedly", "no server
// running", "duplicate session"). cmd.Output keeps it in the ExitError.
func execStderr(err error) string {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return ""
	}
	s := strings.Join(strings.Fields(string(ee.Stderr)), " ")
	if len(s) > execStderrMax {
		cut := execStderrMax
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut-- // never split a character
		}
		s = s[:cut] + "…"
	}
	return s
}

// sessionFromTarget extracts the session name from a tmux target (e.g. "name.0" → "name").
// A leading "=" (exact match, as in has-session -t =name) is not part of the name.
func sessionFromTarget(target string) string {
	target = strings.TrimPrefix(target, "=")
	if i := strings.IndexAny(target, ".:"); i > 0 {
		return target[:i]
	}
	return target
}
