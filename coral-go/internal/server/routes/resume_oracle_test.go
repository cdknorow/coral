package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/agent"
	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/logparse"
	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/ptr"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/store"
)

// resumeTerm records what a resume does to the terminal, and fails the steps a scenario says to.
type resumeTerm struct {
	*mockSessionTerminal
	mu     sync.Mutex
	ops    []string
	titles int
	fail   map[string]bool
}

func (t *resumeTerm) rec(op string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ops = append(t.ops, op)
	if t.fail[strings.Fields(op)[0]] {
		return errors.New(strings.Fields(op)[0] + " failed")
	}
	return nil
}
func (t *resumeTerm) CreateSession(ctx context.Context, name, dir string) error {
	if err := t.rec("create " + name + " " + dir); err != nil {
		return err
	}
	return t.mockSessionTerminal.CreateSession(ctx, name, dir)
}
func (t *resumeTerm) RestartPane(_ context.Context, target, dir string) error {
	return t.rec("respawn " + target + " " + dir)
}
func (t *resumeTerm) RenameSession(ctx context.Context, from, to string) error {
	if err := t.rec("rename " + from + " " + to); err != nil {
		return err
	}
	return t.mockSessionTerminal.RenameSession(ctx, from, to)
}
func (t *resumeTerm) StartLogging(_ context.Context, target, path string) error {
	return t.rec("log " + target + " " + filepath.Base(path))
}
func (t *resumeTerm) StopLogging(_ context.Context, target string) error {
	return t.rec("unlog " + target)
}
func (t *resumeTerm) ClearHistory(_ context.Context, target string) error {
	return t.rec("clear " + target)
}
func (t *resumeTerm) SetPaneTitle(context.Context, string, string) {
	t.mu.Lock()
	t.titles++
	t.mu.Unlock()
}
func (t *resumeTerm) SendToTarget(ctx context.Context, target, cmd string) error {
	if err := t.rec("send " + target); err != nil {
		return err
	}
	return t.mockSessionTerminal.SendToTarget(ctx, target, cmd)
}

// resumeBackend is a pty backend that records its spawns and kills, and fails the ones a scenario says to.
type resumeBackend struct {
	ptymanager.TerminalBackend
	mu       sync.Mutex
	ops      []string
	spawnErr bool
	killErr  error
}

func (b *resumeBackend) Spawn(name, agentType, dir, sid, _ string, _, _ uint16) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ops = append(b.ops, "spawn "+name+" "+agentType+" "+dir+" "+sid)
	if b.spawnErr {
		return errors.New("fork/exec: resource temporarily unavailable")
	}
	return nil
}
func (b *resumeBackend) Kill(name string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ops = append(b.ops, "kill "+name)
	if b.killErr != nil && !strings.Contains(b.ops[0], name) { // the old terminal's kill, not the cleanup's
		return b.killErr
	}
	return nil
}

// oracleSettingsArg is a start command's --settings file.
var oracleSettingsArg = regexp.MustCompile(`--settings ('[^']+'|\S+)`)

// oracleTempName is a temporary file's random part in a start command (its prompt, settings or hooks files):
// os.CreateTemp's random number, a uint32 in decimal of any length (a 5-digit one is 1 in 4300, so a run of
// 600 resumes met one now and then and failed, #4809's flake).
var oracleTempName = regexp.MustCompile(`_[0-9]+(\.[a-z]+)?\b`)

// The Resume outcomes the oracle must reach, each counted from the condition happening, with a floor.
var resumeOutcomes = map[string]int{
	"no session_id":                    5,
	"a terminal can't be resumed":      5,
	"not a resume id":                  5,
	"no session named":                 5,
	"not found":                        10,
	"not live":                         10,
	"archived":                         10,
	"terminal gone, no working dir":    5,
	"tmux: the live pane respawned":    20,
	"tmux: a fresh terminal":           20,
	"pty: a live one replaced":         20,
	"pty: a fresh one":                 10,
	"pty: the spawn fails":             5,
	"pty: the old one can't be ended":  5,
	"pty: the old one already gone":    5,
	"a fresh terminal can't be made":   5,
	"the start command can't be sent":  10,
	"its subscription carried":         20,
	"its subscription put back":        5,
	"no subscription: joins its board": 10,
	"its name and tags carried":        20,
	"respawn failure now refused":      5,
	"pane title now set":               20,
}

// #4809: Resume takes Restart's terminal and launch steps (resumeResolve and resumeCommand, then restartTerminal
// and restartLaunch, then resumeRecord). On random resumes (agent types, stored session states, a live or gone
// terminal, tmux or pty, failing steps, with and without a board subscription), the old Resume and the new one
// answer the same and leave the same terminal, backend, store and board behind. Two changes are intended, from
// Restart's step: a live tmux pane whose respawn or rename fails is refused (the old Resume typed the command into
// it anyway), and a tmux terminal's title is set. Those are checked as the new rule, not compared.
func TestResumeAgreesWithBefore4809(t *testing.T) {
	prevSettle := paneRespawnSettle
	t.Cleanup(func() { paneRespawnSettle = prevSettle })
	paneRespawnSettle = 0
	prevOut := log.Writer()
	log.SetOutput(&bytes.Buffer{})
	t.Cleanup(func() { log.SetOutput(prevOut) })
	prevLog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	t.Cleanup(func() { slog.SetDefault(prevLog) })
	// Each seed has its own handlers, stores and id sequence (h.newUUID), so the 600 seeds run in parallel (#4982: 32 s of the
	// package, one after another), inside one group so the floors are read after every seed has finished.
	var mu sync.Mutex
	counts := map[string]int{}
	t.Run("seeds", func(t *testing.T) {
		for seed := int64(1); seed <= 600; seed++ {
			t.Run(fmt.Sprint(seed), func(t *testing.T) {
				t.Parallel()
				own := map[string]int{}
				runResumeOracle(t, seed, own)
				mu.Lock()
				for k, v := range own {
					counts[k] += v
				}
				mu.Unlock()
			})
		}
	})
	t.Logf("compared, per outcome: %v", counts)
	for name, floor := range resumeOutcomes {
		require.GreaterOrEqual(t, counts[name], floor, "%q went (almost) unexercised", name)
	}
}

type resumeSide struct {
	h    *SessionsHandler
	term *resumeTerm
	back *resumeBackend
	ss   *store.SessionStore
	mux  http.Handler
}

func runResumeOracle(t *testing.T, seed int64, counts map[string]int) {
	ctx := context.Background()
	rng := rand.New(rand.NewSource(seed))
	cur := fmt.Sprintf("4809%04d-0000-4000-8000-000000000001", seed%10000)
	resumeID := "4809aaaa-0000-4000-8000-00000000000a"
	storedType := []string{"claude", "claude", "codex", "agy"}[rng.Intn(4)]
	reqType := []string{"", storedType, storedType, storedType, "terminal", "codex"}[rng.Intn(6)]
	registered, status, archived, sleeping := rng.Intn(12) != 0, "active", rng.Intn(14) == 0, rng.Intn(5) == 0
	if rng.Intn(12) == 0 {
		status = "stopped"
	}
	workdir := []string{"/tmp/proj-a", "/tmp/proj-a", ""}[rng.Intn(3)]
	onPTY, hasPane, hasSub := rng.Intn(2) == 0, rng.Intn(3) != 0, rng.Intn(2) == 0
	board, display := []string{"", "team"}[rng.Intn(2)], []string{"", "Builder"}[rng.Intn(2)]
	prompt := []string{"", "do the work"}[rng.Intn(2)]
	tools := []*string{nil, store.MarshalFlags([]string{"Read", "Bash"})}[rng.Intn(2)]
	nameForm := rng.Intn(10)
	fail := map[string]bool{}
	for _, op := range []string{"create", "respawn", "rename", "send"} {
		if rng.Intn(7) == 0 || (op == "create" && rng.Intn(3) == 0) { // a create only matters for a fresh tmux terminal
			fail[op] = true
		}
	}
	spawnErr := rng.Intn(5) == 0
	var killErr error
	switch rng.Intn(8) {
	case 0:
		killErr = errors.New("operation not permitted")
	case 1:
		killErr = fmt.Errorf("%w: session not found", ptymanager.ErrTerminalGone)
	}
	body := map[string]any{"session_id": resumeID, "agent_type": reqType}
	switch rng.Intn(14) {
	case 0:
		delete(body, "session_id")
	case 1:
		body["session_id"] = "../../etc"
	}
	if rng.Intn(2) == 0 {
		body["current_session_id"] = cur
	}
	raw, _ := json.Marshal(body)

	var sides [2]resumeSide
	var results [2]string
	for i := range sides {
		_, h, mock, ss := setupSessionsTestServer(t)
		term := &resumeTerm{mockSessionTerminal: mock, fail: fail}
		back := &resumeBackend{spawnErr: spawnErr, killErr: killErr}
		h.terminal, h.backend = term, back
		oldName := naming.SessionName(storedType, cur)
		if registered {
			ls := &store.LiveSession{SessionID: cur, AgentType: storedType, AgentName: "proj", WorkingDir: workdir,
				Prompt: ptr.NonEmpty(prompt), BoardName: ptr.NonEmpty(board), DisplayName: ptr.NonEmpty(display), Tools: tools}
			if onPTY {
				pty := "pty"
				ls.Backend = &pty
			}
			require.NoError(t, ss.RegisterLiveSession(ctx, ls))
			if status != "active" {
				_, err := h.db.ExecContext(ctx, "UPDATE live_sessions SET status = ?, stopped_at = ? WHERE session_id = ?", status, "2026-10-18T10:00:00Z", cur)
				require.NoError(t, err)
			}
			if archived {
				_, err := h.db.ExecContext(ctx, "UPDATE live_sessions SET is_archived = 1 WHERE session_id = ?", cur)
				require.NoError(t, err)
			}
			if sleeping {
				require.NoError(t, ss.SetSessionSleeping(ctx, cur, true))
			}
			require.NoError(t, ss.SetDisplayName(ctx, cur, "Builder"))
			tag, err := ss.CreateTag(ctx, "frontend", "")
			require.NoError(t, err)
			require.NoError(t, ss.AddSessionTag(ctx, cur, tag.ID))
		}
		if hasPane {
			mock.addSession(oldName, "/tmp/proj-pane")
		}
		if hasSub {
			_, err := h.bs.Subscribe(ctx, "team", "Builder", "Builder", oldName, nil, "all", true)
			require.NoError(t, err)
		}
		n := 0
		h.newUUID = func() string { n++; return fmt.Sprintf("48090000-0000-4000-8000-%012d", seed*10+int64(n)) }
		mux := chi.NewRouter()
		if i == 0 {
			mux.Post("/api/sessions/live/{name}/resume", h.resumeBefore4809)
		} else {
			mux.Post("/api/sessions/live/{name}/resume", h.Resume)
		}
		target := oldName
		switch nameForm {
		case 0:
			target = cur
		case 1:
			target = "some-name"
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/sessions/live/"+target+"/resume", bytes.NewReader(raw))
		req = req.WithContext(context.WithValue(req.Context(), callerKindKey{}, callerDashCookie))
		mux.ServeHTTP(rec, req)
		sides[i] = resumeSide{h, term, back, ss, mux}
		results[i] = fmt.Sprintf("%d %s", rec.Code, rec.Body.String())
	}
	label := fmt.Sprintf("seed %d: %s stored %s registered %v status %s archived %v sleeping %v wd %q pty %v pane %v sub %v name %d fail %v spawnErr %v killErr %v",
		seed, raw, storedType, registered, status, archived, sleeping, workdir, onPTY, hasPane, hasSub, nameForm, fail, spawnErr, killErr)

	// What each side left behind.
	newID := fmt.Sprintf("48090000-0000-4000-8000-%012d", seed*10+1)
	state := func(s resumeSide, newName string) string {
		var b, b2 strings.Builder // b2: the settings files' contents
		for _, id := range []string{cur, newID} {
			ls, _ := s.ss.GetLiveSession(ctx, id)
			if ls != nil {
				fmt.Fprintf(&b, "live %s type=%s name=%s wd=%s resume=%s prompt=%s board=%s sleeping=%d status=%s;", id, ls.AgentType, ls.AgentName, ls.WorkingDir,
					ptr.Value(ls.ResumeFromID), ptr.Value(ls.Prompt), ptr.Value(ls.BoardName), ls.IsSleeping, ls.Status)
			}
			dn, _ := s.ss.GetDisplayName(ctx, id)
			tags, _ := s.ss.GetSessionTags(ctx, id)
			fmt.Fprintf(&b, "dn %s=%s tags=%d;", id, ptr.Value(dn), len(tags))
		}
		subs, _ := s.h.bs.ListSubscribers(ctx, "team")
		for _, sub := range subs {
			fmt.Fprintf(&b, "sub %s %s %s %s %d;", sub.SubscriberID, sub.JobTitle, sub.SessionName, sub.ReceiveMode, sub.CanPeek)
		}
		fmt.Fprintf(&b, "term %q backend %q", s.term.ops, s.back.ops)
		for _, cmd := range s.term.sentCommands() { // its temporary files' names are random
			if m := oracleSettingsArg.FindStringSubmatch(cmd); m != nil { // Claude's settings file: its allowed tools, hooks
				if b, err := os.ReadFile(strings.Trim(m[1], "'\"")); err == nil {
					fmt.Fprintf(&b2, ";settings %s", strings.ReplaceAll(string(b), s.h.cfg.CoralDir(), "<coral>"))
				}
			}
			cmd = strings.ReplaceAll(cmd, s.h.cfg.CoralDir(), "<coral>") // each side has its own data folder
			fmt.Fprintf(&b, ";cmd %s", oracleTempName.ReplaceAllString(cmd, "<tmp>"))
		}
		// Each side's own folders, as they are also written: the data folder JSON-escaped in a settings file
		// (backslashes on Windows) and the stand-in CLI, which stubCLI makes per side on Windows (#28).
		dir := s.h.cfg.CoralDir()
		st, _ := s.ss.GetSettings(ctx)
		oneSide := strings.NewReplacer(strings.Trim(jsonString(dir), `"`), "<coral>", dir, "<coral>")
		if cli := st["cli_path_claude"]; cli != "" {
			oneSide = strings.NewReplacer(strings.Trim(jsonString(dir), `"`), "<coral>", dir, "<coral>", cli, "<cli>")
		}
		return oneSide.Replace(b.String() + b2.String())
	}
	newName := ""
	for _, at := range []string{"claude", "codex", "agy"} {
		if strings.Contains(results[1], naming.SessionName(at, newID)) {
			newName = naming.SessionName(at, newID)
		}
	}
	tmuxLive := hasPane && !onPTY && registered && status == "active" && !archived
	if refused := strings.Contains(results[1], "respawn failed") || strings.Contains(results[1], "rename failed"); refused {
		// Intended: the old Resume went on with a pane whose respawn or rename failed (and typed into it); Restart's
		// step refuses it. Only a live tmux pane gets there, and the old Resume never said so.
		require.True(t, tmuxLive && (fail["respawn"] || fail["rename"]), label)
		require.True(t, strings.HasPrefix(results[1], "500"), label+": "+results[1])
		require.NotContains(t, results[0], "respawn failed", label)
		require.NotContains(t, results[0], "rename failed", label)
		counts["respawn failure now refused"]++
		return
	}
	norm := func(s string) string {
		return strings.NewReplacer("pty launch command failed: ", "", "launch command failed: ", "").Replace(s)
	}
	require.Equal(t, norm(results[0]), norm(results[1]), label)
	require.Equal(t, state(sides[0], newName), state(sides[1], newName), label)

	r := results[1]
	switch {
	case strings.Contains(r, "session_id_required"):
		counts["no session named"]++
	case strings.Contains(r, "session_id is required"):
		counts["no session_id"]++
	case strings.Contains(r, "Resume not supported for terminal"):
		counts["a terminal can't be resumed"]++
	case strings.Contains(r, "not a valid resume id"):
		counts["not a resume id"]++
	case strings.Contains(r, "session_not_found"):
		counts["not found"]++
	case strings.Contains(r, "session_not_live"):
		counts["not live"]++
	case strings.Contains(r, "session_archived"):
		counts["archived"]++
	case strings.Contains(r, "terminal_gone"):
		counts["terminal gone, no working dir"]++
	case strings.Contains(r, "pty spawn failed"):
		counts["pty: the spawn fails"]++
	case strings.Contains(r, "pty resume failed"):
		counts["pty: the old one can't be ended"]++
		require.Contains(t, fmt.Sprint(sides[1].back.ops), "kill "+newName, "the new terminal is ended too")
	case strings.Contains(r, "create failed"):
		counts["a fresh terminal can't be made"]++
	case strings.Contains(r, "send failed"):
		counts["the start command can't be sent"]++
		if hasSub {
			counts["its subscription put back"]++
		}
	}
	if !strings.HasPrefix(r, "200") {
		return
	}
	pty := onPTY && registered
	switch {
	case pty && hasPane && errors.Is(killErr, ptymanager.ErrTerminalGone):
		counts["pty: the old one already gone"]++
	case pty && hasPane:
		counts["pty: a live one replaced"]++
	case pty:
		counts["pty: a fresh one"]++
	case hasPane:
		counts["tmux: the live pane respawned"]++
	default:
		counts["tmux: a fresh terminal"]++
	}
	if !pty && sides[1].term.titles > 0 {
		counts["pane title now set"]++
	}
	st := state(sides[1], newName)
	if hasSub && strings.Contains(st, "sub Builder Builder "+newName) {
		counts["its subscription carried"]++
	}
	if !hasSub && board != "" {
		counts["no subscription: joins its board"]++
	}
	if strings.Contains(st, "dn "+newID+"=Builder tags=1") {
		counts["its name and tags carried"]++
	}
}

// resumeBefore4809 is Resume as it was before #4809 (its 500 ms pane settle through the same seam as the new code).
func (h *SessionsHandler) resumeBefore4809(w http.ResponseWriter, r *http.Request) {
	defer h.kickWS() // the feed shows the change now, not on the next tick
	name := chi.URLParam(r, "name")
	var body struct {
		SessionID        string `json:"session_id"`
		AgentType        string `json:"agent_type"`
		CurrentSessionID string `json:"current_session_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.SessionID == "" {
		errBadRequest(w, "session_id is required")
		return
	}

	agentType := body.AgentType
	if agentType == "" {
		agentType = at.Claude
	}
	if agentType == at.Terminal { // a plain shell has no conversation to resume (and no agent CLI to type)
		errBadRequest(w, "Resume not supported for terminal")
		return
	}
	if !agent.IsResumeID(agentType, body.SessionID) {
		errBadRequest(w, "session_id is not a valid resume id") // #3582: it goes into file paths
		return
	}
	agentImpl := agent.GetAgent(agentType)
	if !agentImpl.SupportsResume() {
		errBadRequest(w, fmt.Sprintf("Resume not supported for %s", agentType))
		return
	}

	ctx := r.Context()

	targetSID := body.CurrentSessionID
	if targetSID == "" {
		if _, sid := logparse.ParseSessionName(name); sid != "" {
			targetSID = sid
		} else if _, err := uuid.Parse(name); err == nil {
			targetSID = name
		}
	}

	if namesNoSession(name, targetSID) {
		writeSessionIDRequired(w)
		return
	}

	ls, err := h.ss.GetLiveSession(ctx, targetSID)
	if err != nil {
		slog.Warn("failed to validate live session metadata", "session_id", targetSID, "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "Agent metadata is temporarily unavailable; try again.",
			"code":  "agent_metadata_unavailable",
		})
		return
	}
	if ls == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "live session not found",
			"code":  "session_not_found",
		})
		return
	}
	if ls.Status != "active" || ls.StoppedAt != nil {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "session is not live",
			"code":  "session_not_live",
		})
		return
	}
	if ls.IsArchived == 1 {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "session is archived",
			"code":  "session_archived",
		})
		return
	}

	body.CurrentSessionID = targetSID

	// The agent's own terminal, or (#3213) when it is gone a fresh one in the agent's
	// working directory: never another agent's pane.
	var workdir string
	pane, _ := h.terminal.FindSession(ctx, name, agentType, targetSID)
	if pane == nil {
		if ls.WorkingDir != "" {
			workdir = ls.WorkingDir
		} else {
			writeTerminalError(w, ptymanager.ErrTerminalGone)
			return
		}
	} else {
		workdir = pane.CurrentPath
	}

	slog.Info("resuming agent", "agent_name", name, "session_id", body.SessionID, "agent_type", agentType)

	// Prepare resume files
	agent.TryPrepareResume(agentImpl, body.SessionID, workdir)

	newSessionID := h.uuid()
	newSessionName := naming.SessionName(agentType, newSessionID)
	newLogPath := naming.LogFile(h.cfg.LogDir, agentType, newSessionID)

	// Load stored fields from current session
	storedPrompt := ptr.Value(ls.Prompt)
	storedSleeping := ls.IsSleeping == 1
	storedAgentType := agentType
	if ls.AgentType != "" {
		storedAgentType = ls.AgentType
	}
	storedBoard := ptr.Value(ls.BoardName)
	storedBoardServer := ptr.Value(ls.BoardServer)
	storedDisplayName := ptr.Value(ls.DisplayName)
	storedBoardType := ptr.Value(ls.BoardType)
	storedTools := store.UnmarshalFlags(ls.Tools)

	role := naming.SubscriberID(storedDisplayName, agentType)

	userSettings, serr := h.launchSettings(ctx)
	if serr != nil { // never resume the agent on the CLI found on PATH (#4350)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": serr.Error()})
		return
	}

	// The launch command is built before the terminal is touched: when it can't be built
	// (#3601 W12), the resume fails with the old agent still running.
	cmd, err := launchCommand(agentImpl, agent.LaunchParams{
		SessionID:       newSessionID,
		SessionName:     newSessionName,
		ProtocolPath:    h.protocolPath(),
		ResumeSessionID: body.SessionID,
		WorkingDir:      workdir,
		CLIPath:         agent.ResolveCLIPath(userSettings, agentType), // Settings > CLI paths (#3205)
		BoardName:       storedBoard,
		BoardLabel:      h.boardLabel(storedBoard),
		Role:            role,
		Prompt:          storedPrompt,
		PromptOverrides: promptOverrides(userSettings),
		BoardType:       storedBoardType,
		Tools:           storedTools,
		PermissionMode:  userSettings["default_permission_mode"],
		CoralDir:        h.cfg.CoralDir(),
		CoralHost:       h.cfg.AgentHost(),
		CoralPort:       h.cfg.Port,
	})
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}

	target := fmt.Sprintf("%s:0.0", newSessionName)
	switch {
	case h.restartsOnPTY(ls):
		// On pty, as Restart (#4767): a new pty terminal under the new name, then a live one ended (pty has no rename,
		// and no pty session is named "<name>:0.0" or "<name>.0"); the start command goes to the session by name.
		if err := h.backend.Spawn(newSessionName, agentType, workdir, newSessionID, "", 200, 50); err != nil {
			errInternalServer(w, fmt.Sprintf("pty spawn failed: %v", err))
			return
		}
		if pane != nil { // ended once its replacement runs: a spawn that fails leaves the agent running
			if err := h.backend.Kill(pane.SessionName); err != nil && !errors.Is(err, ptymanager.ErrTerminalGone) {
				h.backend.Kill(newSessionName)
				errInternalServer(w, fmt.Sprintf("pty resume failed: %v", err))
				return
			}
		}
		if pb, ok := h.backend.(*ptymanager.PTYBackend); ok && !pb.WaitReady(newSessionName, 5*time.Second) {
			log.Printf("[launch] pty shell not ready after 5s, sending anyway: %s", newSessionName)
		}
		target = newSessionName
	case pane == nil:
		ptymanager.ResetAgentLog(newLogPath) // 0600, never through a link (#3601, W17)
		if err := h.terminal.CreateSession(ctx, newSessionName, workdir); err != nil {
			errInternalServer(w, err.Error())
			return
		}
		target = newSessionName + ".0"
		h.terminal.StartLogging(ctx, newSessionName, newLogPath)
	default:
		h.terminal.StopLogging(ctx, pane.Target)
		h.terminal.RestartPane(ctx, pane.Target, pane.CurrentPath)
		h.terminal.RenameSession(ctx, pane.SessionName, newSessionName)
		time.Sleep(paneRespawnSettle) // the copy uses the same seam as the new code
		h.terminal.ClearHistory(ctx, target)
		ptymanager.ResetAgentLog(newLogPath) // 0600, never through a link (#3601, W17)
		h.terminal.StartLogging(ctx, target, newLogPath)
	}
	// The agent's board subscription (job title, receive mode, peek), carried to the new session
	// as restart does (#3216), before the process starts (see Restart: its first request carries
	// the new token and needs the new name on the board).
	var resumeSubscription *board.Subscriber
	if h.bs != nil && body.CurrentSessionID != "" {
		resumeSubscription, _ = h.bs.GetSubscriptionBySessionName(ctx, naming.SessionName(storedAgentType, body.CurrentSessionID))
	}
	if sub := resumeSubscription; sub != nil {
		// As Restart does: an agent resumed without its board is worse than a resume that failed.
		if _, err := h.bs.Subscribe(ctx, sub.Project, sub.SubscriberID, sub.JobTitle, newSessionName,
			sub.OriginServer, sub.ReceiveMode, sub.CanPeek == 1); err != nil {
			errInternalServer(w, err.Error())
			return
		}
		writeBoardStateFile(newSessionName, sub.Project, sub.JobTitle, h.cfg)
	}
	if err := sendLaunchCommand(ctx, h.terminal, target, cmd); err != nil {
		h.putSubscriptionBack(ctx, resumeSubscription)
		errInternalServer(w, err.Error())
		return
	}

	// Capture shell PID
	var resumePID int
	if tmuxTerm, ok := h.terminal.(*ptymanager.TmuxSessionTerminal); ok {
		if pid, err := tmuxTerm.Client().GetPanePID(ctx, newSessionName); err == nil {
			resumePID = pid
		}
	}

	h.chatMeta.forget(body.CurrentSessionID, newSessionID)
	h.ss.ReplaceLiveSession(ctx, body.CurrentSessionID, &store.LiveSession{
		SessionID:    newSessionID,
		AgentType:    agentType,
		AgentName:    filepath.Base(strings.TrimRight(workdir, "/")),
		WorkingDir:   workdir,
		ResumeFromID: ptr.NonEmpty(body.SessionID),
		Prompt:       ptr.NonEmpty(storedPrompt),
		BoardName:    ptr.NonEmpty(storedBoard),
		BoardServer:  ptr.NonEmpty(storedBoardServer),
		BoardType:    ptr.NonEmpty(storedBoardType),
		PID:          resumePID,
	})
	h.noteLaunchedHooks(ctx, newSessionID, agentType) // #3665

	// The agent keeps its name and tags, like a restart (#3216).
	if body.CurrentSessionID != "" {
		h.ss.MigrateDisplayName(ctx, body.CurrentSessionID, newSessionID)
		h.ss.MigrateSessionTags(ctx, body.CurrentSessionID, newSessionID)
	}
	// The new row inherits the sleeping flag; the resumed agent is running now, as
	// after a restart. Left asleep, team wake and the board notifier skipped it
	// until the server restarted (#3273).
	h.ss.SetSessionSleeping(ctx, newSessionID, false)
	if storedSleeping && storedBoard != "" && h.boardHandler != nil {
		h.boardHandler.SetPaused(storedBoard, false)
	}

	// Re-subscribed above, before the launch (subscriber_id, the role, is stable, so the cursor
	// persists); a resume with no subscription joins its board now.
	if resumeSubscription == nil && storedBoard != "" {
		h.setupBoardAndPrompt(newSessionID, newSessionName, agentType, storedBoard, storedDisplayName)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "session_id": newSessionID, "session_name": newSessionName,
	})
}
