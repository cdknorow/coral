//go:build !windows

package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
	"github.com/subgentic/coral/internal/tmux"
)

// #6141: an agent started on tmux carries CORAL_PTY_RUN=<tag> in its environment on every path that starts its shell (launch, restart, wake), the same tag the sessions database holds, so Kill can find what
// the agent started and then detached. A real tmux on a private socket, and a stand-in agent CLI that writes the tag it was started with to a file.

type tmuxCoralRig struct {
	h        *SessionsHandler
	ss       *store.SessionStore
	terminal *ptymanager.TmuxSessionTerminal
	backend  *ptymanager.TmuxBackend
	dataDir  string
	started  string // the file the stand-in agent appends its tag to, one line per start
}

func newTmuxCoralRig(t *testing.T, helperScript string) *tmuxCoralRig {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	dataDir, err := os.MkdirTemp(shortTempBase(), "rtg")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dataDir) })
	sock := filepath.Join(dataDir, "t.sock")
	smoke := sock + ".smoke"
	if out, err := exec.Command("tmux", "-S", smoke, "new-session", "-d", "-s", "coral-smoke", "sleep 1").CombinedOutput(); err != nil {
		exec.Command("tmux", "-S", smoke, "kill-server").Run()
		t.Skipf("tmux cannot create sessions here: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	exec.Command("tmux", "-S", smoke, "kill-server").Run()
	client := tmux.NewClient(dataDir)
	client.SocketPath = sock
	client.FallbackToDefault = false
	t.Cleanup(func() { exec.Command("tmux", "-S", sock, "kill-server").Run() })

	cfg := config.Load(dataDir)
	cfg.LogDir = filepath.Join(dataDir, "logs")
	require.NoError(t, os.MkdirAll(cfg.LogDir, 0o700))
	db, err := storetest.Open(t, filepath.Join(dataDir, "s.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	ss := store.NewSessionStore(db)
	ctx := context.Background()
	started := filepath.Join(dataDir, "started")
	cli := filepath.Join(dataDir, "fake-claude")
	require.NoError(t, os.WriteFile(cli, []byte("#!/bin/sh\ncase \"$1\" in --version|-v|auth) exit 0;; esac # Coral's own version and sign-in probes: not an agent starting\necho \"$CORAL_PTY_RUN\" >> '"+started+"'\n"+helperScript+"exec sleep 600\n"), 0o755))
	require.NoError(t, ss.SetSetting(ctx, "cli_path_claude", cli))
	backend := ptymanager.NewTmuxBackend(client, cfg.LogDir)
	t.Cleanup(func() { backend.Close() })
	terminal := ptymanager.NewTmuxSessionTerminal(client)
	ptymanager.SetRunTagLookup(func(sid string) string { return ss.RunTagOf(context.Background(), sid) })
	h := NewSessionsHandler(db, cfg, backend, terminal, nil)
	return &tmuxCoralRig{h: h, ss: ss, terminal: terminal, backend: backend, dataDir: dataDir, started: started}
}

func (r *tmuxCoralRig) tags(t *testing.T, n int) []string {
	t.Helper()
	var lines []string
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		b, _ := os.ReadFile(r.started)
		lines = strings.Fields(string(b))
		if len(lines) >= n {
			break
		}
	}
	require.GreaterOrEqual(t, len(lines), n, "the agent started %d times", n)
	return lines
}

func (r *tmuxCoralRig) post(path string, body string) *httptest.ResponseRecorder {
	rt := chi.NewRouter()
	rt.Post("/api/sessions/live/{name}/restart", r.h.Restart)
	rt.Post("/api/sessions/live/{sessionID}/sleep", r.h.SleepSession)
	rt.Post("/api/sessions/live/{sessionID}/wake", r.h.WakeSession)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, req)
	return rec
}

func TestATmuxAgentCarriesItsTagOnLaunchWakeAndRestart(t *testing.T) {
	r := newTmuxCoralRig(t, "")
	ctx := context.Background()

	// launch
	res, err := r.h.launchSession(ctx, launchSpec{WorkDir: r.dataDir, AgentType: "claude"})
	require.NoError(t, err)
	sid := res["session_id"].(string)
	tag := r.ss.RunTagOf(ctx, sid)
	require.Regexp(t, `^[0-9a-f]{32}$`, tag, "a random 128-bit tag is kept with the session")
	assert.Equal(t, []string{tag}, r.tags(t, 1), "launch: the agent started with the tag the database holds")

	// sleep, then wake: the same session, the same tag
	require.Equal(t, http.StatusOK, r.post("/api/sessions/live/"+sid+"/sleep", "").Code)
	require.Equal(t, http.StatusOK, r.post("/api/sessions/live/"+sid+"/wake", "").Code)
	got := r.tags(t, 2)
	assert.Equal(t, []string{tag, tag}, got, "wake: the same tag")
	assert.Equal(t, tag, r.ss.RunTagOf(ctx, sid))

	// restart: a new session row continues the old one and carries the same tag
	rec := r.post("/api/sessions/live/"+naming.SessionName("claude", sid)+"/restart", `{"agent_type":"claude","session_id":"`+sid+`"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got = r.tags(t, 3)
	assert.Equal(t, []string{tag, tag, tag}, got, "restart: the same tag, though the agent is a new run")
	all, err := r.ss.GetAllLiveSessions(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, all)
	for _, ls := range all {
		assert.Equal(t, tag, r.ss.RunTagOf(ctx, ls.SessionID), "every live row of the agent, the new one included, has the tag")
	}
}

// An agent started on the built-in terminal is not given one: its processes carry the terminal's own tag.
func TestAPtyAgentIsNotGivenATmuxTag(t *testing.T) {
	_, h, _, ss := setupSessionsTestServer(t)
	h.backend = &ptymanager.PTYBackend{}
	assert.Empty(t, h.runTagFor(context.Background(), "aaaaaaaa-6141-4000-8000-000000000001", "", ""))
	h.backend = &ptymanager.TmuxBackend{}
	assert.Regexp(t, `^[0-9a-f]{32}$`, h.runTagFor(context.Background(), "aaaaaaaa-6141-4000-8000-000000000002", "", ""))
	_ = ss
}

// The point of the tag: a helper the agent started and that then detached (a session of its own, reparented to PID 1, ignoring the hangup and SIGTERM) is ended by Kill, which finds it by the tag in its
// environment. A node helper, because macOS does not show the environment of Apple's own binaries.
func TestKillOnTmuxEndsAHelperThatDetached(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is needed for a helper that is not one of Apple's own binaries")
	}
	dir, err := os.MkdirTemp(shortTempBase(), "hlp")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	pidFile := filepath.Join(dir, "helper.pid")
	child := `process.on("SIGTERM",()=>{});process.on("SIGHUP",()=>{});require("fs").writeFileSync(process.argv[1],String(process.pid));setTimeout(()=>{},600000)`
	spawner := `const {spawn}=require("child_process");spawn(process.execPath,["-e",process.argv[2],process.argv[1]],{detached:true,stdio:"ignore"}).unref()`
	r := newTmuxCoralRig(t, "'"+node+"' -e '"+spawner+"' '"+pidFile+"' '"+child+"'\n")
	ctx := context.Background()
	res, err := r.h.launchSession(ctx, launchSpec{WorkDir: r.dataDir, AgentType: "claude"})
	require.NoError(t, err)
	sid := res["session_id"].(string)

	var pid int
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end) && pid == 0; time.Sleep(50 * time.Millisecond) {
		if b, err := os.ReadFile(pidFile); err == nil {
			pid = atoiOr0(strings.TrimSpace(string(b)))
		}
	}
	require.Greater(t, pid, 1, "the helper reported its pid")
	rememberStart(pid)
	t.Cleanup(func() { killIfStillRecorded(pid) })
	time.Sleep(500 * time.Millisecond) // its parent has exited: it belongs to PID 1 now

	require.NoError(t, r.terminal.KillSession(ctx, naming.SessionName("claude", sid), "claude", sid))
	assert.True(t, gonePID(pid, 10*time.Second), "the helper that detached was ended with the agent")
}

// The resume path builds its command in resumeCommand (the same parameters as launch, restart and wake): on tmux the command exports the tag of the session it continues; on the built-in terminal it does not.
func TestResumeCommandExportsTheTagOfTheSessionItContinues(t *testing.T) {
	_, h, _, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	require.NoError(t, ss.SetSetting(ctx, "cli_path_claude", stubCLI(t)))
	const old = "cccccccc-6141-4000-8000-000000000001"
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: old, AgentType: "claude", AgentName: "a", WorkingDir: t.TempDir()}))
	require.NoError(t, ss.SetRunTagIfEmpty(ctx, old, "0123456789abcdef0123456789abcdef"))
	plan := func() *restartPlan {
		return &restartPlan{agentType: "claude", stored: &store.LiveSession{SessionID: old}, workdir: t.TempDir(), newSessionID: "cccccccc-6141-4000-8000-000000000002", newSessionName: "claude-cccccccc-6141-4000-8000-000000000002", displayName: "a"}
	}
	h.backend = &ptymanager.TmuxBackend{}
	cmd, fail := h.resumeCommand(ctx, plan(), agent.GetAgent("claude"), old)
	require.Nil(t, fail)
	assert.Contains(t, cmd, "CORAL_PTY_RUN='0123456789abcdef0123456789abcdef'", "resume: the tag of the session it continues, exported")
	h.backend = &ptymanager.PTYBackend{}
	cmd, fail = h.resumeCommand(ctx, plan(), agent.GetAgent("claude"), old)
	require.Nil(t, fail)
	assert.NotContains(t, cmd, "CORAL_PTY_RUN", "the built-in terminal has a tag of its own")
}

// One Kill must never end another live agent's helpers: a tag is handed on only while no other live session holds it. A restart or resume of an agent keeps its tag (the old row is the one being replaced); a launch
// that continues a conversation another LIVE agent is running gets a fresh one.
func TestATagIsNotSharedBetweenTwoLiveAgents(t *testing.T) {
	_, h, _, ss := setupSessionsTestServer(t)
	h.backend = &ptymanager.TmuxBackend{}
	ctx := context.Background()
	const a, b, c, fresh = "dddddddd-6141-4000-8000-00000000000a", "dddddddd-6141-4000-8000-00000000000b", "dddddddd-6141-4000-8000-00000000000c", "dddddddd-6141-4000-8000-00000000000d"
	for _, id := range []string{a, b} {
		require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: id, AgentType: "claude", AgentName: "x", WorkingDir: "/tmp"}))
	}
	tagA := h.runTagFor(ctx, a, "", "")
	require.NoError(t, ss.SetRunTagIfEmpty(ctx, a, tagA))
	require.NotEmpty(t, tagA)

	// resume or restart of A: A is the row being replaced: the new session carries A's tag
	assert.Equal(t, tagA, h.runTagFor(ctx, c, a, ""), "restart or resume of an agent keeps its tag")
	// a launch that continues A's conversation while A is still live and not being replaced: a fresh tag
	other := h.runTagFor(ctx, fresh, "", a)
	assert.NotEmpty(t, other)
	assert.NotEqual(t, tagA, other, "two live agents never share a tag")
	// the agent being replaced is not the only holder: another live row carries the same tag, so not even a restart shares it
	require.NoError(t, ss.SetRunTagIfEmpty(ctx, b, tagA))
	assert.NotEqual(t, tagA, h.runTagFor(ctx, c, a, ""), "another live agent holds it: a fresh tag")
}
