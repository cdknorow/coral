//go:build !windows

package startup

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/store"
)

// agentCLIBinaries are the binaries the six agent types run (claude, codex, pi, and agy for agy,
// antigravity and gemini), plus the type names themselves, in case a caller runs one by name.
var agentCLIBinaries = []string{"claude", "codex", "pi", "agy", "antigravity", "gemini"}

// bootWithStubCLIs starts the real server in an isolated environment (the #4381 rules) with a
// stub for every agent CLI first on PATH, which records any call, and no CLI path configured.
// PATH holds only the stubs and the system folders, so no real agent CLI can be reached. seed
// runs on the fresh database before startup. It waits for startup and the first background
// ticks, and returns what the stubs recorded.
func bootWithStubCLIs(t *testing.T, seed func(ctx context.Context, db *store.DB, home string)) string {
	iso := t.TempDir()
	for _, k := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(k, iso)
	}
	t.Setenv("HOME", filepath.Join(iso, "home"))
	t.Setenv("USERPROFILE", filepath.Join(iso, "home"))
	home := filepath.Join(iso, "data")
	t.Setenv("CORAL_DATA_DIR", home)
	// As a CDP test server runs (run_cdp_suites.mjs): no demo workflow, no welcome, no telemetry.
	t.Setenv("CORAL_NO_DEMO_SEED", "1")
	t.Setenv("CORAL_NO_WELCOME", "1")
	t.Setenv("CORAL_TELEMETRY_DISABLED", "1")
	t.Setenv("CORAL_CLOUD_URL", "")
	require.NoError(t, os.MkdirAll(filepath.Join(iso, "home"), 0o700))
	require.NoError(t, os.MkdirAll(home, 0o700))

	stubs := filepath.Join(iso, "stub-bin")
	require.NoError(t, os.MkdirAll(stubs, 0o700))
	calls := filepath.Join(iso, "cli-calls.log")
	for _, bin := range agentCLIBinaries {
		script := "#!/bin/sh\necho \"" + bin + " $*\" >> '" + calls + "'\necho 1.0.0\n"
		require.NoError(t, os.WriteFile(filepath.Join(stubs, bin), []byte(script), 0o700))
	}
	t.Setenv("PATH", stubs+":/usr/bin:/bin:/usr/sbin:/sbin")

	if seed != nil {
		ctx := context.Background()
		db, err := store.OpenWithKey(ctx, filepath.Join(home, "sessions.db"), "")
		require.NoError(t, err)
		seed(ctx, db, home)
		require.NoError(t, db.Close())
	}

	// The server's background services run until its context ends (#4167, goleak).
	ctx, cancel := context.WithCancel(context.Background())
	var rs *RunningServer
	var cfg *config.Config
	var err error
	for try := 0; try < 5; try++ { // freePort's port can be taken before Start binds it
		cfg = config.Load(home)
		cfg.Host = "127.0.0.1"
		cfg.Port = freePort(t)
		if rs, err = Start(ctx, cfg, Options{BackendType: "pty"}); err == nil || !addrInUse(err) {
			break
		}
	}
	if err != nil {
		cancel()
	}
	require.NoError(t, err)
	t.Cleanup(func() {
		cancel()
		rs.Shutdown(5 * time.Second)
		rs.Close()
	})

	c := &http.Client{Timeout: 5 * time.Second}
	require.Eventually(t, func() bool {
		resp, err := c.Get("http://127.0.0.1:" + strconv.Itoa(cfg.Port) + "/api/health")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 30*time.Second, 50*time.Millisecond, "the server answers")
	// The first background ticks: the CLI-version backfill and the goal poll run at once, the
	// pollers' first passes within a few seconds.
	time.Sleep(6 * time.Second)

	data, err := os.ReadFile(calls)
	if os.IsNotExist(err) {
		return ""
	}
	require.NoError(t, err)
	return string(data)
}

// #4420 (Security, on QA's CDP ready-gate, #4354): a fresh Coral server with no agents and no
// CLI path set runs no agent CLI while it starts or in its first background ticks. The ready
// gate no longer counts calls made before a test server's cli_path is set, so this is what
// catches a change that probes a CLI on every boot.
func TestAFreshServerRunsNoAgentCLI(t *testing.T) {
	got := bootWithStubCLIs(t, nil)
	assert.Empty(t, strings.TrimSpace(got), "agent CLIs run at startup")
}

// The same with one session that ended (its transcript on disk): still no agent CLI.
func TestAServerWithAnEndedSessionRunsNoAgentCLI(t *testing.T) {
	got := bootWithStubCLIs(t, func(ctx context.Context, db *store.DB, home string) {
		ss := store.NewSessionStore(db)
		const sid = "00000000-0000-4000-8000-000000004420"
		require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "Ada", WorkingDir: home}))
		_, err := db.Exec(`UPDATE live_sessions SET status = 'inactive', stopped_at = ? WHERE session_id = ?`,
			time.Now().Add(-time.Hour).UTC().Format(store.ISOFormat), sid)
		require.NoError(t, err)
		transcript := filepath.Join(home, sid+".jsonl")
		require.NoError(t, os.WriteFile(transcript, []byte(`{"type":"user","message":{"content":"build the thing"}}`+"\n"+
			`{"type":"assistant","message":{"content":"built it"}}`+"\n"), 0o600))
		require.NoError(t, ss.UpsertSessionIndex(ctx, &store.SessionIndex{SessionID: sid, SourceType: "claude", SourceFile: transcript}))
	})
	assert.Empty(t, strings.TrimSpace(got), "agent CLIs run at startup")
}

// The control: an active session whose CLI version isn't recorded is asked it at boot (the
// backfill, #3573; the session is also resumed, which runs the stub once more), and the stubs
// record those calls, so the two tests above would see one.
func TestBootStubsRecordACLIRun(t *testing.T) {
	got := bootWithStubCLIs(t, func(ctx context.Context, db *store.DB, home string) {
		ss := store.NewSessionStore(db)
		require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: "00000000-0000-4000-8000-000000004421",
			AgentType: "claude", AgentName: "Bob", WorkingDir: home}))
	})
	assert.Contains(t, got, "claude --version")
}
