package tmux

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/subgentic/coral/internal/logparse"
)

// paneSnapshot is one list-panes: the panes, and each pane's state by target (#S:#I.#P).
type paneSnapshot struct {
	at     time.Time
	panes  []Pane
	states map[string]paneState
}

// paneState is a pane's shell pid and size.
type paneState struct{ pid, cols, rows int }

// paneStateTTL is how long one list-panes answers every pane question (the panes, a pane's pid
// and size); a command that changes a pane drops it at once.
const paneStateTTL = time.Second

// DisplayPaneListAge is how old a pane list may be for a caller that only discovers or shows
// agents (#4346): the session list and team view, machine health. With every caller held to
// paneStateTTL, callers arriving at different moments ran a list-panes every 1.3 s between them.
const DisplayPaneListAge = 5 * time.Second

type paneListMaxAgeKey struct{}

// WithPaneListMaxAge lets the calls made with ctx answer from the shared pane list up to d old,
// instead of paneStateTTL. A pane Coral starts, kills, resizes or renames drops the list at once
// whatever the age, so only changes made outside Coral (a pane that exits on its own, an agent's
// own title) show up to d late. Never for a caller about to type into a pane or decide whether
// its agent has exited.
func WithPaneListMaxAge(ctx context.Context, d time.Duration) context.Context {
	return context.WithValue(ctx, paneListMaxAgeKey{}, d)
}

// paneListMaxAge is how old a shared pane list the calls made with ctx accept.
func paneListMaxAge(ctx context.Context) time.Duration {
	if d, ok := ctx.Value(paneListMaxAgeKey{}).(time.Duration); ok && d > paneStateTTL {
		return d
	}
	return paneStateTTL
}

// paneChanging are the tmux commands after which the shared pane list is stale: a pane's size
// (Coral's own resize must read back at once, v1.4.63's terminal size checks), its pid (a
// respawn), its title or name, or the set of panes. Changes Coral doesn't make (an agent's own
// title) show within paneStateTTL.
var paneChanging = map[string]bool{
	"resize-window": true, "resize-pane": true, "respawn-pane": true, "new-session": true,
	"new-window": true, "split-window": true, "kill-session": true, "kill-window": true, "kill-pane": true,
	// Since #4049 the shared list also carries each pane's title, session name and place: Coral's
	// own title (select-pane -T), a rename or a moved pane reads back at once too.
	"select-pane": true, "rename-session": true, "rename-window": true, "respawn-window": true,
	"swap-pane": true, "move-pane": true, "join-pane": true, "break-pane": true,
}

// forgetPaneStates drops the shared pane list after a command that changed a pane.
func (c *Client) forgetPaneStates() {
	c.paneStatesMu.Lock()
	c.snap = nil
	c.paneStatesGen++
	c.paneStatesMu.Unlock()
}

// ListPanes returns all tmux panes with their titles, session names, and targets.
// When FallbackToDefault is set it also reads tmux's default socket and merges
// the result, so an upgrade from a Coral that used the default socket still
// sees its running agents. See defaultSocketFallback for when that is enabled;
// it is off by default for any non-default data directory, because merging
// there lets one Coral instance discover and kill another's agents.
func (c *Client) ListPanes(ctx context.Context) ([]Pane, error) {
	var panes []Pane
	if s := c.paneSnapshot(ctx); s != nil {
		panes = append(panes, s.panes...) // a copy: callers may change their slice
	}

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
	countExec("list-panes")
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

// ForgetBareNameWarning forgets that the warning was logged for name, on any backend, once
// its session ended (#4057): the memo would otherwise keep every name ever looked up.
func ForgetBareNameWarning(name string) {
	bareNameWarned.Range(func(k, _ any) bool {
		if _, n, _ := strings.Cut(k.(string), "\x00"); n == name {
			bareNameWarned.Delete(k)
		}
		return true
	})
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

// snapshotFormat lists every pane once with all that the shared readers need. The title is LAST:
// it is free-form text the agent sets and can contain the '|' separator (see listPanesOnSocket).
const snapshotFormat = "#{session_name}|#S:#I.#P|#{pane_pid}|#{pane_width}|#{pane_height}|#{pane_current_path}|#{pane_title}"

// paneSnapshot is the primary socket's panes from one list-panes at most every paneStateTTL (or
// the age ctx allows, WithPaneListMaxAge), shared by every caller (#4049); nil when tmux couldn't
// list them.
func (c *Client) paneSnapshot(ctx context.Context) *paneSnapshot {
	c.paneStatesMu.Lock()
	if c.snap != nil && time.Since(c.snap.at) < paneListMaxAge(ctx) {
		defer c.paneStatesMu.Unlock()
		return c.snap
	}
	gen := c.paneStatesGen
	c.paneStatesMu.Unlock()
	args := []string{"list-panes", "-a", "-F", snapshotFormat}
	if c.SocketPath != "" {
		args = append(socketArgs(c.SocketPath), args...)
	}
	if runObserver != nil {
		runObserver([]string{"list-panes"})
	}
	bin := c.resolveTmuxBin()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = tmuxEnv(bin)
	countExec("list-panes")
	out, err := cmd.Output()
	noteTmuxResult(err, bin, c.SocketPath)
	if err != nil {
		return nil
	}
	s := &paneSnapshot{at: time.Now(), states: map[string]paneState{}}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.SplitN(strings.TrimSpace(line), "|", 7)
		if len(f) != 7 {
			continue
		}
		s.panes = append(s.panes, Pane{SessionName: f[0], Target: f[1], CurrentPath: f[5], PaneTitle: f[6], SocketPath: c.SocketPath})
		pid, e1 := strconv.Atoi(f[2])
		cols, e2 := strconv.Atoi(f[3])
		rows, e3 := strconv.Atoi(f[4])
		if e1 == nil && e2 == nil && e3 == nil {
			s.states[f[1]] = paneState{pid: pid, cols: cols, rows: rows}
		}
	}
	c.paneStatesMu.Lock()
	if c.paneStatesGen == gen { // no pane changed while it ran: keep it for the next callers
		c.snap = s
	}
	c.paneStatesMu.Unlock()
	return s
}

// PaneRoot is one pane of the primary socket: its session's name and the pid of the shell in it.
type PaneRoot struct {
	SessionName string
	PID         int
}

// PaneRoots is every pane's session name and shell pid from the shared snapshot (one list-panes at most every paneStateTTL, or the age ctx allows); nil when tmux could not list them.
func (c *Client) PaneRoots(ctx context.Context) []PaneRoot {
	s := c.paneSnapshot(ctx)
	if s == nil {
		return nil
	}
	out := make([]PaneRoot, 0, len(s.panes))
	for _, p := range s.panes {
		if st, ok := s.states[p.Target]; ok && st.pid > 1 {
			out = append(out, PaneRoot{SessionName: p.SessionName, PID: st.pid})
		}
	}
	return out
}

// allPaneStates is every pane on the primary socket by target, from the shared snapshot.
func (c *Client) allPaneStates(ctx context.Context) map[string]paneState {
	if s := c.paneSnapshot(ctx); s != nil {
		return s.states
	}
	return nil
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
