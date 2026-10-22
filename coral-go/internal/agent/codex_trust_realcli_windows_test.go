//go:build windows

package agent

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/UserExistsError/conpty"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// The key and hash codexHookTrustHash computes are Codex internals, recorded against 0.161 (codex_trust_test.go). This runs the
// real Codex under ConPTY and checks that its own version still trusts exactly what Coral computes (#5891, Codex Test's finding 6
// on #5889; checked against 0.163.0). Set CORAL_REAL_CODEX_BIN to the native codex.exe (the platform package of @openai/codex,
// not the npm .cmd shim). Everything is temporary: HOME, USERPROFILE, CODEX_HOME and APPDATA are new folders, the hook program is a
// stand-in, and Codex is given a made-up API key from its environment (it is only saved to the temporary CODEX_HOME; no
// request is made, since no message is ever sent). Four launches, the same five hooks each:
//   - Coral's trust flag: Codex raises no "Hooks need review";
//   - the flag for another command (a hook changed since it was trusted): the dialog;
//   - no trust flag: the dialog, which also shows the check can see it;
//   - the same hooks in the user's config.toml with no trust: the dialog.
func TestCodexHookTrustRealCLI(t *testing.T) {
	bin := os.Getenv("CORAL_REAL_CODEX_BIN")
	if bin == "" {
		t.Skip("CORAL_REAL_CODEX_BIN names a native codex.exe to run this")
	}
	_, err := os.Stat(bin)
	require.NoError(t, err, "CORAL_REAL_CODEX_BIN is set but is not a file")

	tools := t.TempDir()
	// a harmless executable for the hook program: the dialog is about the command's text, and nothing runs it here
	standIn, err := os.ReadFile(filepath.Join(os.Getenv("SystemRoot"), "System32", "whoami.exe"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(tools, "coral-hook-agentic-state.exe"), standIn, 0o755))
	t.Setenv(HookDirEnv, tools)

	oldSafe := codexHooksFolderSafe
	t.Cleanup(func() { codexHooksFolderSafe = oldSafe; codexTrustSwitch.Store(nil) })
	codexHooksFolderSafe = func(string) (bool, string) { return true, "" } // an installed Coral's protected folder, which a temp dir is not

	SetCodexHookTrust(func() bool { return false })
	hooks := codexHookArgs("real-cli-hooks-only")
	SetCodexHookTrust(func() bool { return true })
	trusted := codexHookArgs("real-cli-trusted")
	require.Greater(t, len(trusted), len(hooks), "the trust flag is part of the launch")
	require.Equal(t, hooks, trusted[:len(hooks)])

	command, ok := ResolvePowerShellHookCommand("coral-hook-agentic-state")
	require.True(t, ok)
	changed := append(append([]string{}, hooks...), codexHookTrustArgs("windows", command+" # changed")...)
	userConfig := "[hooks]\n"
	for _, event := range codexHookEvents {
		userConfig += event + " = [{hooks=[{type=\"command\",command=" + tomlString(command) + "}]}]\n"
	}

	for _, c := range []struct {
		name       string
		args       []string
		config     string
		wantReview bool
	}{
		{"coral's trust flag", trusted, "", false},
		{"a changed hook", changed, "", true},
		{"no trust flag", hooks, "", true},
		{"hooks in the user's config.toml, no trust", nil, userConfig, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			screen := runCodexUnderConPTY(t, bin, c.args, c.config)
			// "hooks need review" is not searched for: Codex's cursor movements eat letters of it in the stream. "5 hooks are new or changed"
			// and the dialog's own options are.
			review := strings.Contains(screen, "are new or changed") || strings.Contains(screen, "trust all and continue")
			t.Logf("%d characters drawn after the key was saved; review dialog: %v", len(screen), review)
			if review != c.wantReview {
				t.Logf("screen:\n%s", scrubUser(screen))
			}
			assert.Equal(t, c.wantReview, review)
			assert.Contains(t, screen, "openai codex", "Codex did not draw its screen after the key: the run proves nothing")
		})
	}
}

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]|\x1b\\][^\x07\x1b]*(\x07|\x1b\\\\)|\x1b[()][A-Z0-9]|\x1b[=>]")

func scrubUser(s string) string {
	for _, v := range []string{os.Getenv("USERNAME"), "runneradmin"} {
		if len(v) > 2 {
			s = strings.ReplaceAll(s, v, strings.Repeat("x", len(v)))
		}
	}
	return s
}

func flattenScreen(raw string) string {
	return strings.ToLower(strings.Join(strings.Fields(ansiRe.ReplaceAllString(raw, " ")), " "))
}

// runCodexUnderConPTY starts bin with args in a ConPTY, its HOME, USERPROFILE, CODEX_HOME and APPDATA in new folders (config is
// written to the CODEX_HOME's config.toml when not empty), takes it through the sign-in screen with the API key from its
// environment, and returns what it drew after that (escape sequences removed, lower case, whitespace folded): the screen where
// its hooks are loaded and reviewed.
func runCodexUnderConPTY(t *testing.T, bin string, args []string, config string) string {
	t.Helper()
	home := t.TempDir()
	codexHome := filepath.Join(home, ".codex")
	for _, d := range []string{codexHome, filepath.Join(home, "AppData", "Roaming"), filepath.Join(home, "AppData", "Local")} {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}
	if config != "" {
		require.NoError(t, os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(config), 0o600))
	}
	set := map[string]string{
		"HOME": home, "USERPROFILE": home, "CODEX_HOME": codexHome,
		"APPDATA": filepath.Join(home, "AppData", "Roaming"), "LOCALAPPDATA": filepath.Join(home, "AppData", "Local"),
		"CORAL_TELEMETRY_DISABLED": "1", "OPENAI_API_KEY": "sk-probe-not-a-real-key", "CODEX_API_KEY": "",
	}
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if _, ok := set[strings.ToUpper(name)]; ok || strings.HasPrefix(kv, "=") {
			continue
		}
		env = append(env, kv)
	}
	for k, v := range set {
		env = append(env, k+"="+v)
	}

	cpty, err := conpty.Start(windows.ComposeCommandLine(append([]string{bin}, args...)),
		conpty.ConPtyDimensions(120, 40), conpty.ConPtyEnv(env), conpty.ConPtyWorkDir(t.TempDir()))
	require.NoError(t, err)
	defer cpty.Close()

	var mu sync.Mutex
	var raw strings.Builder
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := cpty.Read(buf)
			if n > 0 {
				mu.Lock()
				raw.Write(buf[:n])
				mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	drawn := func() string { mu.Lock(); defer mu.Unlock(); return raw.String() }

	waitFor := func(what string, d time.Duration, ok func(screen string) bool) {
		t.Helper()
		for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(250 * time.Millisecond) {
			if ok(flattenScreen(drawn())) {
				return
			}
		}
		t.Fatalf("Codex did not show %s within %s; its screen:\n%s", what, d, scrubUser(flattenScreen(drawn())))
	}
	// Codex asks for the terminal's cursor position before it draws: answer as a terminal does.
	answered := 0
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			select {
			case <-done:
				return
			case <-time.After(200 * time.Millisecond):
			}
			s := drawn()
			if n := strings.Count(s, "\x1b[6n"); n > answered {
				for ; answered < n; answered++ {
					_, _ = cpty.Write([]byte("\x1b[1;1R"))
				}
			}
		}
	}()

	waitFor("its sign-in screen", 60*time.Second, func(s string) bool { return strings.Contains(s, "provide your own api key") })
	_, _ = cpty.Write([]byte("3"))
	time.Sleep(time.Second)
	_, _ = cpty.Write([]byte("\r"))
	waitFor("the API key box", 30*time.Second, func(s string) bool { return strings.Contains(s, "press enter to save") })
	before := len(drawn())
	_, _ = cpty.Write([]byte("\r"))
	// The hooks are reviewed (or not) as the screen after the sign-in comes up; the sandbox prompt or the hooks dialog follows the key.
	waitFor("its next screen after the key", 60*time.Second, func(s string) bool {
		return strings.Contains(s, "api key configured") || strings.Contains(s, "new or changed") || strings.Contains(s, "sandbox")
	})
	time.Sleep(12 * time.Second)
	return flattenScreen(drawn()[before:])
}
