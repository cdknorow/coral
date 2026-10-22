//go:build !windows

package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/subgentic/coral/internal/hooks"
)

// A mock Codex runs the launch's real Coral hook outside the tool sandbox, then
// puts the real coral-board and coral-agent binaries inside the real Codex
// sandbox. No model, credentials, sign-in, or running Coral instance is used.
// Opt in with CORAL_TEST_REAL_CODEX=/absolute/path/to/codex (0.163+).
func TestCodexSandboxCoralToolsRealCLI(t *testing.T) {
	codex := os.Getenv("CORAL_TEST_REAL_CODEX")
	if codex == "" {
		t.Skip("set CORAL_TEST_REAL_CODEX to exercise the installed sandbox without a model call")
	}
	python, err := exec.LookPath("python3")
	require.NoError(t, err)
	root := t.TempDir()
	home := filepath.Join(root, "home")
	data := filepath.Join(home, "coral")
	tools := filepath.Join(root, "tools")
	work := filepath.Join(root, "work")
	for _, p := range []string{home, data, tools, work, filepath.Join(home, "codex"), filepath.Join(data, "session_tokens")} {
		require.NoError(t, os.MkdirAll(p, 0700))
	}
	for _, name := range []string{"coral-board", "coral-agent", "coral-hook-agentic-state"} {
		cmd := exec.Command("go", "build", "-o", filepath.Join(tools, name), "./cmd/"+name)
		cmd.Dir = "../.."
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s: %s", name, out)
	}
	const sid = "11111111-2222-4333-8444-555555555555"
	const session = "codex-" + sid
	const token = "scratch-sandbox-test-token"
	auth := filepath.Join(data, "session_tokens", sid)
	require.NoError(t, os.WriteFile(auth, []byte(token), 0600))
	old := SessionTokenMinter
	SessionTokenMinter = func(string, string) string { return auth }
	t.Cleanup(func() { SessionTokenMinter = old })
	var mu sync.Mutex
	seen := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(hooks.SessionTokenHeader) != token {
			http.Error(w, "missing session token", 401)
			return
		}
		mu.Lock()
		seen[r.URL.Path] = true
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":1,"ok":true}`))
	}))
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)
	state, _ := json.Marshal(map[string]string{"project": "scratch-board", "job_title": "Sandbox Test", "server_url": srv.URL})
	require.NoError(t, os.WriteFile(filepath.Join(data, hooks.BoardStateFileName(session)), state, 0600))
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex"))
	t.Setenv("CORAL_HOOK_DIR", tools)
	mock := filepath.Join(root, "mock-codex")
	script := `import json,os,subprocess,sys
args=sys.argv[1:]; configs=[args[i+1] for i,x in enumerate(args[:-1]) if x=='-c']
hook=next(x for x in configs if x.startswith('hooks.SessionStart='))
command=json.JSONDecoder().raw_decode(hook.split('command=',1)[1])[0]
subprocess.run(['/bin/sh','-c',command],input=json.dumps({'hook_event_name':'SessionStart','session_id':os.environ['CORAL_SESSION_ID'],'source':'startup'}),text=True,check=True)
env=dict(os.environ);env.pop('CORAL_SESSION_TOKEN',None)
network=[x for x in configs if x.startswith('sandbox_workspace_write.network_access=')]
for tool,argv in [('coral-board',['post','harmless scratch sandbox probe']),('coral-agent',['notify','--title','harmless scratch sandbox probe'])]:
 cmd=[os.environ['REAL_CODEX'],'sandbox','-c','sandbox_mode="workspace-write"','-c','sandbox_workspace_write.exclude_slash_tmp=true','-c','sandbox_workspace_write.exclude_tmpdir_env_var=true']
 for config in network:cmd+=['-c',config]
 cmd+=['--',os.path.join(os.environ['PROBE_TOOLS'],tool)]+argv
 subprocess.run(cmd,env=env,check=True)
# With temp exceptions disabled, Coral data stays read-only inside the sandbox.
cmd=[os.environ['REAL_CODEX'],'sandbox','-c','sandbox_mode="workspace-write"','-c','sandbox_workspace_write.exclude_slash_tmp=true','-c','sandbox_workspace_write.exclude_tmpdir_env_var=true','--',sys.executable,'-c',"import pathlib;pathlib.Path("+repr(os.path.join(os.environ['CORAL_DATA_DIR'],'must-not-write'))+").write_text('no')"]
assert subprocess.run(cmd,env=env,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL).returncode!=0
`
	require.NoError(t, os.WriteFile(mock, []byte("#!"+python+"\n"+script), 0700))
	p := LaunchParams{SessionID: sid, SessionName: session, Role: "Sandbox Test", CLIPath: mock, CoralDir: data, CoralHost: u.Hostname(), CoralPort: port, PermissionMode: DefaultLaunchPermissionMode("codex")}
	launch, err := (&CodexAgent{}).LaunchCommand(p)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", launch)
	cmd.Dir = work
	// A fresh environment prevents credentials or a developer's Coral identity from
	// reaching either the mock CLI or the real sandbox. Only scratch identity exists.
	cmd.Env = []string{"HOME=" + home, "CODEX_HOME=" + filepath.Join(home, "codex"), "PATH=/usr/bin:/bin", "TMPDIR=" + work, "REAL_CODEX=" + codex, "PROBE_TOOLS=" + tools}
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	mu.Lock()
	defer mu.Unlock()
	require.True(t, seen["/api/board/scratch-board/messages"], "board POST reached localhost with auth: %v; %s", seen, out)
	require.True(t, seen["/api/notifications"], "agent POST reached localhost with auth: %v; %s", seen, out)
	hookReached := false
	for path := range seen {
		if strings.Contains(path, "/live/") {
			hookReached = true
		}
	}
	require.True(t, hookReached, "real hook reached localhost: %v; %s", seen, out)
	entries, err := os.ReadDir(filepath.Join(data, "hook_cache"))
	require.NoError(t, err)
	require.NotEmpty(t, entries, "host hook can write its private cache")
	_, err = os.Stat(filepath.Join(data, "must-not-write"))
	require.True(t, os.IsNotExist(err), "agent shell must not write Coral's data directory")
	t.Logf("Codex sandbox + real hook, board, agent passed; authenticated routes: %v", seen)
}
