//go:build !windows

package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3362 (CRITICAL): no launch parameter can run a shell command. Each agent's launch
// line is built with a hostile value in one field at a time and really run by /bin/sh;
// the payloads try to create a marker file, which must never appear.
func TestLaunchCommandNeverRunsInjectedShell(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	dir := t.TempDir()
	stub := filepath.Join(dir, "agent-cli")
	require.NoError(t, os.WriteFile(stub, []byte("#!/bin/sh\nexit 0\n"), 0o755))

	// One marker per subtest: they run in parallel. %s is the marker path.
	payloads := []string{
		"$(touch %s)",
		"`touch %s`",
		"x; touch %s",
		"x\ntouch %s",
		"x'; touch %s; echo '",
		`x"; touch %s; echo "`,
	}
	fields := map[string]func(p *LaunchParams, v string){
		"flags":             func(p *LaunchParams, v string) { p.Flags = []string{"--model", v} },
		"resume_session_id": func(p *LaunchParams, v string) { p.ResumeSessionID = v },
		"session_id":        func(p *LaunchParams, v string) { p.SessionID = v },
		"session_name":      func(p *LaunchParams, v string) { p.SessionName = v },
		"board_name":        func(p *LaunchParams, v string) { p.BoardName = v },
		"role":              func(p *LaunchParams, v string) { p.Role = v },
		"prompt":            func(p *LaunchParams, v string) { p.Prompt = v },
		"permission_mode":   func(p *LaunchParams, v string) { p.PermissionMode = v },
		"coral_dir":         func(p *LaunchParams, v string) { p.CoralDir = v },
		"coral_host":        func(p *LaunchParams, v string) { p.CoralHost = v },
		"prompt_override":   func(p *LaunchParams, v string) { p.PromptOverrides = map[string]string{"worker": v} },
		"tools":             func(p *LaunchParams, v string) { p.Tools = []string{v} },
		"cli_path":          func(p *LaunchParams, v string) { p.CLIPath = v },
	}
	agents := map[string]Agent{"claude": &ClaudeAgent{}, "codex": &CodexAgent{}, "agy": &AgyAgent{}, "pi": &PiAgent{}}

	n := 0
	for agentName, ag := range agents {
		for field, set := range fields {
			n++
			sessionID := fmt.Sprintf("11111111-2222-3333-4444-%012d", n)
			t.Run(agentName+"/"+field, func(t *testing.T) {
				t.Parallel()
				marker := filepath.Join(dir, agentName+"-"+field)
				for i, payload := range payloads {
					p := LaunchParams{
						SessionID: sessionID, SessionName: "claude-x",
						CLIPath: stub, BoardName: "team", Role: "Dev", Prompt: "hello", CoralPort: 8420,
					}
					set(&p, fmt.Sprintf(payload, marker))
					cmd := WrapWithBundlePath(ag.BuildLaunchCommand(p))
					out, _ := exec.Command("/bin/sh", "-c", cmd).CombinedOutput()
					_, err := os.Stat(marker)
					if !assert.True(t, os.IsNotExist(err), "payload %d ran a command:\n%s\noutput: %s", i, cmd, out) {
						os.Remove(marker)
					}
					CleanupTempFiles(p.SessionID)
					CleanupTempFiles(p.ResumeSessionID)
				}
			})
		}
	}
}

// The quoting itself: safe words pass through, anything else becomes one single-quoted
// word; only the parts a builder names are raw.
func TestShellJoinQuotesEverythingButNamedRawParts(t *testing.T) {
	for in, want := range map[string]string{
		"claude":              "claude",
		"--model":             "--model",
		"/usr/local/bin/x":    "/usr/local/bin/x",
		"a b":                 "'a b'",
		"$(id)":               "'$(id)'",
		"x\ny":                "'x\ny'",
		"it's":                `'it'\''s'`,
		"export PATH=/x && y": "'export PATH=/x && y'",
	} {
		assert.Equal(t, want, shellQuote(in), "%q", in)
	}
	raw := `"$(cat '/tmp/coral_prompt_x.txt')"`
	assert.Equal(t, `claude '$(touch /tmp/p)' `+raw, ShellJoin([]string{"claude", "$(touch /tmp/p)", raw}, raw))
	assert.Equal(t, `claude '`+strings.ReplaceAll(raw, "'", `'\''`)+`'`, ShellJoin([]string{"claude", raw}), "not named: quoted, even if it looks like Coral's")
}

// A session id from a request can't break out of the prompt file's quotes.
func TestPromptFilePathIsQuoted(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	assert.Equal(t, "ab_c_d-1", safeFileID("ab'c;d-1"))
	path := writeTempFile("prompt", "x'$(touch /tmp/p)'", "txt", []byte("hi"))
	defer os.Remove(path)
	assert.NotContains(t, filepath.Base(path), "'")
	assert.Equal(t, fmt.Sprintf(`"$(cat '%s')"`, path), FormatPromptFileArg(path))
	assert.Equal(t, `"$(cat '/tmp/a'\''b')"`, FormatPromptFileArg("/tmp/a'b"))
}
