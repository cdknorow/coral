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

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/store"
)

// agentCLIBinaries are the binaries the six agent types run (claude, codex, pi, and agy for agy,
// antigravity and gemini), plus the type names themselves, in case a caller runs one by name.
var agentCLIBinaries = []string{"claude", "codex", "pi", "agy", "antigravity", "gemini"}

// bootWithStubCLIs starts the real server in an isolated environment (the #4381 rules) with a
// stub for every agent CLI first on PATH, which records any call, and no CLI path configured.
// PATH holds only the stubs and the system folders, so no real agent CLI can be reached. seed
// runs on the fresh database before startup. It waits for startup and the first background
// ticks, and returns what the stubs recorded.
// bootObserve is how long a boot that must run NO agent CLI is watched after the server answers: the first background ticks (the CLI-version backfill and the goal poll run
// at once, the pollers' first passes within a few seconds) all fall inside it.
const bootObserve = 6 * time.Second

func bootWithStubCLIs(t *testing.T, seed func(ctx context.Context, db *store.DB, home string)) string {
	return bootStubCLIs(t, seed, bootObserve, false)
}

// bootWithStubCLIsNoWatch boots the same server and returns once it answers: for a test that checks what Start itself did and does not watch for a CLI run (#5591: two of them
// used to sit out the 6 s watch for nothing).
func bootWithStubCLIsNoWatch(t *testing.T, seed func(ctx context.Context, db *store.DB, home string)) {
	bootStubCLIs(t, seed, 0, false)
}

// bootWithStubCLIsUntilRun is the control: it returns as soon as a stub has recorded a call (at most 15 s), where the others watch a fixed window for none.
func bootWithStubCLIsUntilRun(t *testing.T, seed func(ctx context.Context, db *store.DB, home string)) string {
	return bootStubCLIs(t, seed, 15*time.Second, true)
}

func bootStubCLIs(t *testing.T, seed func(ctx context.Context, db *store.DB, home string), observe time.Duration, untilRun bool) string {
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
	if untilRun {
		deadline := time.Now().Add(observe)
		for time.Now().Before(deadline) {
			if b, err := os.ReadFile(calls); err == nil && len(strings.TrimSpace(string(b))) > 0 {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
	} else if observe > 0 {
		time.Sleep(observe) // the watch for a CLI run that must not happen: nothing to wait on, so a fixed window
	}

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
	got := bootWithStubCLIsUntilRun(t, func(ctx context.Context, db *store.DB, home string) {
		ss := store.NewSessionStore(db)
		require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: "00000000-0000-4000-8000-000000004421",
			AgentType: "claude", AgentName: "Bob", WorkingDir: home}))
	})
	assert.Contains(t, got, "claude --version")
}
