//go:build !windows

package startup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/cdknorow/coral/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4937 part 2: end to end, a dev build pointed at a fake capture endpoint through the dev-only
// CORAL_TELEMETRY_TEST_SINK seam (#4935) sends install and app_opened with the safe standard properties
// and nothing that identifies anyone; with the opt-out it sends nothing. It never reaches the real
// PostHog (the sink is a local httptest server) or the real ~/.coral (an isolated HOME and data dir).

type capturedEvent struct {
	event, apiKey, distinctID string
	props                     map[string]any
}

func telemetryModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the working directory")
		}
		dir = parent
	}
}

// telemetryRun is one start of the dev binary: what the fake sink captured, whether the server answered,
// and the isolated paths, so values can be checked for them.
type telemetryRun struct {
	events []capturedEvent
	up     bool
	iso    string
	home   string
}

// allowedEventProps are the property keys an install-time event may carry: the standard properties
// (events.StandardProperties). Anything else on install, app_opened,
// upgrade or installation_active_week is a new thing sent about a user and must be added here on purpose.
var allowedEventProps = map[string]bool{
	"version": true, "edition": true, "os": true, "arch": true, "platform": true,
	"schema_version": true, "run_id": true, "entrypoint": true,
}

func TestTelemetrySinkE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs a dev coral binary")
	}
	bin := filepath.Join(t.TempDir(), "coral")
	buildDevCoral(t, bin)

	// run starts the dev binary against a fresh fake sink and returns the events captured within the deadline.
	// seed, when set, writes settings into the data dir's database before the server starts.
	run := func(t *testing.T, extraEnv []string, seed func(ctx context.Context, ss *store.SessionStore)) telemetryRun {
		var mu sync.Mutex
		var events []capturedEvent
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			var p struct {
				APIKey     string         `json:"api_key"`
				Event      string         `json:"event"`
				DistinctID string         `json:"distinct_id"`
				Properties map[string]any `json:"properties"`
			}
			if json.Unmarshal(body, &p) == nil && p.Event != "" {
				mu.Lock()
				events = append(events, capturedEvent{p.Event, p.APIKey, p.DistinctID, p.Properties})
				mu.Unlock()
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		iso := t.TempDir()
		// The dev binary probes `go`, which writes a read-only module cache under this HOME; make the tree
		// writable before t.TempDir removes it, so the test leaves nothing behind. Runs before the removal (LIFO).
		t.Cleanup(func() {
			_ = filepath.WalkDir(iso, func(p string, _ os.DirEntry, err error) error {
				if err == nil {
					_ = os.Chmod(p, 0o755)
				}
				return nil
			})
		})
		home := filepath.Join(iso, "home")
		data := filepath.Join(iso, "data")
		stubs := filepath.Join(iso, "stub-bin")
		for _, d := range []string{home, data, stubs} {
			require.NoError(t, os.MkdirAll(d, 0o700))
		}
		for _, b := range []string{"claude", "codex", "pi", "agy", "antigravity", "gemini", "tmux"} {
			require.NoError(t, os.WriteFile(filepath.Join(stubs, b), []byte("#!/bin/sh\necho 1.0.0\n"), 0o700))
		}
		if seed != nil {
			ctx := context.Background()
			db, err := store.OpenWithKey(ctx, filepath.Join(data, "sessions.db"), "")
			require.NoError(t, err)
			seed(ctx, store.NewSessionStore(db))
			require.NoError(t, db.Close())
		}
		port := freePort(t)
		cmd := exec.Command(bin, "--host", "127.0.0.1", "--port", strconv.Itoa(port), "--no-browser", "--cloud-url", "off")
		cmd.Env = append([]string{
			"HOME=" + home, "USERPROFILE=" + home,
			"TMPDIR=" + iso,
			"CORAL_DATA_DIR=" + data,
			"CORAL_NO_DEMO_SEED=1", "CORAL_NO_WELCOME=1",
			"CORAL_CLOUD_URL=",
			"CORAL_TELEMETRY_TEST_SINK=" + srv.URL + "/capture/",
			"PATH=" + stubs + ":/usr/bin:/bin:/usr/sbin:/sbin",
		}, extraEnv...)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		require.NoError(t, cmd.Start())
		defer func() {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_, _ = cmd.Process.Wait()
		}()

		deadline := time.Now().Add(30 * time.Second)
		base := "http://127.0.0.1:" + strconv.Itoa(port) + "/"
		up := false
		for time.Now().Before(deadline) { // the server is up
			if resp, err := http.Get(base); err == nil {
				resp.Body.Close()
				up = true
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		has := func() bool {
			mu.Lock()
			defer mu.Unlock()
			var i, a bool
			for _, e := range events {
				if e.event == "install" {
					i = true
				}
				if e.event == "app_opened" {
					a = true
				}
			}
			return i && a
		}
		for time.Now().Before(deadline) && !has() {
			time.Sleep(200 * time.Millisecond)
		}
		// With an opt-out, nothing should ever arrive: wait a fixed moment so a stray send would be caught.
		if len(extraEnv) > 0 || seed != nil {
			time.Sleep(4 * time.Second)
		}
		mu.Lock()
		defer mu.Unlock()
		out := make([]capturedEvent, len(events))
		copy(out, events)
		return telemetryRun{events: out, up: up, iso: iso, home: home}
	}

	// Case A: a plain dev build sends install and app_opened with the safe standard properties, no 'internal'.
	plain := run(t, nil, nil)
	require.True(t, plain.up, "the server answered, so the sends below come from a running Coral")
	names := map[string]bool{}
	for _, e := range plain.events {
		names[e.event] = true
	}
	require.True(t, names["install"], "install was sent (got %v)", names)
	require.True(t, names["app_opened"], "app_opened was sent (got %v)", names)
	hostname, _ := os.Hostname()
	for _, e := range plain.events {
		assert.Equal(t, "dev-test-sink", e.apiKey, "%s carries the dev test key, so it is the keyed path", e.event)
		assert.NotEmpty(t, e.distinctID, "%s has a distinct_id", e.event)
		for _, k := range []string{"version", "edition", "os", "arch", "platform", "schema_version", "run_id", "entrypoint"} {
			assert.Contains(t, e.props, k, "%s carries the standard property %s", e.event, k)
		}
		assert.NotContains(t, e.props, "internal", "%s has no 'internal' property (there is no internal concept in the repo)", e.event)
		// Nothing that identifies anyone: only allowlisted keys, and no value holds a local path or the host name.
		for k, v := range e.props {
			assert.True(t, allowedEventProps[k], "%s carries property %q, which is not in the allowlist of safe properties", e.event, k)
			val := fmt.Sprint(v)
			for _, secret := range []string{plain.home, plain.iso, hostname} {
				if len(secret) >= 3 {
					assert.False(t, strings.Contains(val, secret), "%s property %q holds a local path or the host name", e.event, k)
				}
			}
		}
		assert.False(t, strings.Contains(e.distinctID, plain.home) || (len(hostname) >= 3 && strings.Contains(e.distinctID, hostname)), "%s distinct_id is not derived in the clear from a path or host name", e.event)
	}

	// Case B: with the env opt-out, the same dev build sends nothing.
	optOut := run(t, []string{"CORAL_TELEMETRY_DISABLED=1"}, nil)
	require.True(t, optOut.up, "the server answered in the opt-out run, so nothing sent is not just a server that never started")
	assert.Empty(t, eventNames(optOut.events), "with CORAL_TELEMETRY_DISABLED a dev build sends nothing")

	// Case C: with the saved Settings opt-out (telemetry_enabled=false) and no env var, it sends nothing either.
	saved := run(t, nil, func(ctx context.Context, ss *store.SessionStore) {
		require.NoError(t, ss.SetSetting(ctx, "telemetry_enabled", "false"))
	})
	require.True(t, saved.up, "the server answered in the saved-setting run")
	assert.Empty(t, eventNames(saved.events), "with telemetry_enabled=false saved a dev build sends nothing")
}

func eventNames(events []capturedEvent) []string {
	var names []string
	for _, e := range events {
		names = append(names, e.event)
	}
	return names
}

// realGoCaches are the module and build caches of the real environment, read before TestMain points HOME at a temporary folder: with that HOME the
// inner go build would use an empty $HOME/go/pkg/mod and download every module from the proxy, and a proxy error failed the test (#5206: ci/5150 go-race).
var realGoModCache, realGoBuildCache = goCaches()

func goCaches() (mod, build string) {
	out, err := exec.Command("go", "env", "GOMODCACHE", "GOCACHE").Output()
	if err != nil {
		return "", ""
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 2 {
		return "", ""
	}
	return strings.TrimSpace(lines[0]), strings.TrimSpace(lines[1])
}

// buildDevCoral builds the dev binary into bin with the real module and build caches, and tries again (see buildWithRetry) only when the failure is a
// module download that the network refused: the code is not at fault then.
func buildDevCoral(t *testing.T, bin string) {
	t.Helper()
	root := telemetryModuleRoot(t)
	once := func() ([]byte, error) {
		build := exec.Command("go", "build", "-tags", "dev", "-o", bin, "./cmd/coral")
		build.Dir = root
		build.Env = os.Environ()
		if realGoModCache != "" {
			build.Env = append(build.Env, "GOMODCACHE="+realGoModCache)
		}
		if realGoBuildCache != "" {
			build.Env = append(build.Env, "GOCACHE="+realGoBuildCache)
		}
		return build.CombinedOutput()
	}
	if out, err := buildWithRetry(once, time.Sleep, t.Logf); err != nil {
		t.Fatalf("build dev coral failed: %v\n%s", err, out)
	}
}

// buildWithRetry runs once up to three times, pausing 3 s then 6 s, and again only while the failure is a refused module download.
func buildWithRetry(once func() ([]byte, error), sleep func(time.Duration), logf func(string, ...any)) ([]byte, error) {
	var out []byte
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		if out, err = once(); err == nil {
			return out, nil
		}
		if !downloadFailed(out) || attempt == 3 {
			break
		}
		logf("build dev coral, attempt %d: a module download failed, trying again:\n%s", attempt, tailBytes(out, 600))
		sleep(time.Duration(attempt) * 3 * time.Second)
	}
	return out, err
}

// downloadFailed: the go command's output for a module it could not fetch (a proxy or network error), not a compile error.
func downloadFailed(out []byte) bool {
	s := string(out)
	for _, marker := range []string{"proxy.golang.org", "i/o timeout", "connection reset", "connection refused", "no such host", "TLS handshake timeout", "unexpected EOF", "502 Bad Gateway", "503 Service Unavailable"} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

func tailBytes(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return "..." + string(b[len(b)-n:])
}
