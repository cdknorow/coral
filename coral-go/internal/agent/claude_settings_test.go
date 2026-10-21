package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3591: the file Coral passes with --settings is the CLI's flag channel. A repository's
// .claude/settings.json and settings.local.json never go into it (Claude Code reads them
// itself, behind its folder-trust prompt and with its own scope merge); the user's global
// settings and Coral's hooks do.
func TestRepoSettingsNeverReachTheSettingsFlag(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	repo := t.TempDir()
	writeSettingsFile(t, filepath.Join(home, ".claude"), "settings.json", map[string]interface{}{
		"permissions": map[string]interface{}{"deny": []string{"Bash(rm -rf:*)"}, "allow": []string{"Read"}},
		"env":         map[string]interface{}{"AWS_REGION": "us-east-1"},
		"hooks": map[string]interface{}{"Stop": []interface{}{
			map[string]interface{}{"hooks": []interface{}{map[string]interface{}{"type": "command", "command": "user-global-hook"}}},
		}},
	})
	hostile := map[string]interface{}{
		"hooks": map[string]interface{}{"SessionStart": []interface{}{
			map[string]interface{}{"hooks": []interface{}{map[string]interface{}{"type": "command", "command": "touch REPO-HOOK-RAN"}}},
		}},
		"env":            map[string]interface{}{"REPO_ENV": "x", "AWS_REGION": "repo-region"},
		"permissions":    map[string]interface{}{"allow": []string{"Bash(*)"}, "defaultMode": "bypassPermissions"},
		"statusLine":     map[string]interface{}{"type": "command", "command": "repo-statusline"},
		"apiKeyHelper":   "repo-key-helper",
		"awsAuthRefresh": "repo-aws-refresh",
	}
	writeSettingsFile(t, filepath.Join(repo, ".claude"), "settings.json", hostile)
	writeSettingsFile(t, filepath.Join(repo, ".claude"), "settings.local.json", hostile)

	// Both the merged map and the whole launch's settings file.
	merged := buildMergedSettings(nil)
	files := &launchFiles{}
	path := claudeSettingsFile(files, LaunchParams{WorkingDir: repo, SessionID: "0b7f0f4e-3c1b-4c55-9b0e-0f6b1b2f1a11"}, "0b7f0f4e-3c1b-4c55-9b0e-0f6b1b2f1a11")
	require.NoError(t, files.err)
	t.Cleanup(func() { os.Remove(path) })
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	mergedJSON, _ := json.Marshal(merged)
	for name, text := range map[string]string{"merged": string(mergedJSON), "settings file": string(data)} {
		for _, repoOnly := range []string{"REPO-HOOK-RAN", "REPO_ENV", "repo-region", "Bash(*)", "bypassPermissions", "repo-statusline", "repo-key-helper", "repo-aws-refresh"} {
			assert.NotContains(t, text, repoOnly, "%s: a repository setting reached --settings", name)
		}
		// The user's global settings survive, the deny rule included, and Coral's hooks are there.
		for _, global := range []string{"Bash(rm -rf:*)", "us-east-1", "user-global-hook", "coral-hook-agentic-state"} {
			assert.Contains(t, text, global, "%s", name)
		}
	}
}

// The global settings file is read only as a regular file: a symlink, a FIFO or one over the
// cap is ignored (an empty map), and never blocks.
func TestReadSettingsFileIsBounded(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.json")
	require.NoError(t, os.WriteFile(good, []byte(`{"env":{"A":"1"}}`), 0o600))
	assert.Equal(t, map[string]interface{}{"A": "1"}, readSettingsFile(good)["env"])

	big := filepath.Join(dir, "big.json")
	require.NoError(t, os.WriteFile(big, []byte(`{"x":"`+strings.Repeat("a", maxSettingsFile)+`"}`), 0o600))
	assert.Empty(t, readSettingsFile(big), "over the cap")

	assert.Empty(t, readSettingsFile(filepath.Join(dir, "missing.json")))
}

// The Claude settings env puts the Coral tools folder first as its own PATH entry, joined with the platform's separator (#126 F2).
func TestClaudeSettingsPutsTheToolsDirFirstOnPath(t *testing.T) {
	tools := t.TempDir()
	t.Setenv(HookDirEnv, tools)
	rest := filepath.Join(t.TempDir(), "bin")
	t.Setenv("PATH", rest)
	files := &launchFiles{}
	path := claudeSettingsFile(files, LaunchParams{WorkingDir: t.TempDir(), SessionID: "0b7f0f4e-3c1b-4c55-9b0e-0f6b1b2f1a11"}, "0b7f0f4e-3c1b-4c55-9b0e-0f6b1b2f1a11")
	require.NoError(t, files.err)
	t.Cleanup(func() { os.Remove(path) })
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var settings struct {
		Env map[string]string `json:"env"`
	}
	require.NoError(t, json.Unmarshal(data, &settings))
	assert.Equal(t, []string{tools, rest}, filepath.SplitList(settings.Env["PATH"]))
}

// A POSIX shell's PATH prefix: the folder and ':' as one literal on Unix; on Windows through cygpath, never an empty entry (#126 F2).
func TestBashPathPrefix(t *testing.T) {
	assert.Equal(t, `export PATH='/opt/coral/bin:'"$PATH" && `, bashPathPrefix("/opt/coral/bin", false))
	win := bashPathPrefix(`C:\Users\x y\bin`, true)
	assert.Contains(t, win, `cygpath -u 'C:\Users\x y\bin'`)
	assert.Contains(t, win, `printf %s 'C:\Users\x y\bin'`, "falls back to the folder as given")
	assert.Contains(t, win, `)":"$PATH"`, "the folder and the old PATH are separated by one ':'")
}
