package agent

import (
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #153: Coral's trust for its own Codex hooks must be exactly what Codex computes, or Codex ignores it and asks. These
// are the keys and hashes Codex 0.161.0 wrote to a scratch CODEX_HOME when the person picked "Trust all and continue" for
// the hooks Coral passed (a scratch Coral whose tools were in D:\coral-team-cache\codex153-272787701\bin).
func TestCodexHookTrustMatchesWhatCodexWrites(t *testing.T) {
	command := "& 'D:/coral-team-cache/codex153-272787701/bin/coral-hook-agentic-state.exe'"
	written := map[string]string{ // key -> trusted_hash, as Codex wrote them
		`C:\<session-flags>\config.toml:session_start:0:0`:      "sha256:5879fde0d95f21d8cc2691fb3911cb94dfd032645097cf13c05bfacc95ebbd41",
		`C:\<session-flags>\config.toml:pre_tool_use:0:0`:       "sha256:1401b821aaf89cedbbfd9f35ec363180998679b93883d176d16832d0f36341e3",
		`C:\<session-flags>\config.toml:post_tool_use:0:0`:      "sha256:37ee971ab65817cdad6105260453b8c0b9a64f12de3549e7712b00d86b3096e3",
		`C:\<session-flags>\config.toml:user_prompt_submit:0:0`: "sha256:94bbd95f8dede581f1308f476aca5f9e97b37a15d44700ec4e089d052ab01e03",
		`C:\<session-flags>\config.toml:stop:0:0`:               "sha256:a3ef88f5a89a73f016b0c9abc911ca9551f3b1e80d4a9f06cecc04c3d8255789",
	}
	require.Len(t, codexHookEvents, len(written), "one trust entry per hook Coral passes")
	for _, event := range codexHookEvents {
		key := codexHookStateKey("windows", event)
		want, ok := written[key]
		require.True(t, ok, "%s: key %s", event, key)
		assert.Equal(t, want, codexHookTrustHash(event, command), event)
	}
	assert.Equal(t, "/<session-flags>/config.toml:stop:0:0", codexHookStateKey("linux", "Stop"), "elsewhere the synthetic path is made against /")

	// A changed command (another tools folder) is another hash: Codex then asks again, it never trusts a different hook.
	assert.NotEqual(t, codexHookTrustHash("Stop", command), codexHookTrustHash("Stop", strings.Replace(command, "codex153", "elsewhere", 1)))
}

// The flag is one -c setting hooks.state to an inline table: Codex splits a -c key on every "." and the keys hold
// "config.toml", so each key is a TOML literal string inside the value, and the hash a basic string.
func TestCodexHookTrustArgsShape(t *testing.T) {
	command := "& 'C:/Program Files (x86)/Coral/coral-hook-agentic-state.exe'"
	args := codexHookTrustArgs("windows", command)
	require.Len(t, args, 2)
	assert.Equal(t, "-c", args[0])
	assert.True(t, strings.HasPrefix(args[1], "hooks.state={'C:\\<session-flags>\\config.toml:session_start:0:0'={trusted_hash=\"sha256:"), args[1])
	assert.True(t, strings.HasSuffix(args[1], "\"}}"), args[1])
	for _, event := range codexHookEvents {
		assert.Contains(t, args[1], "'"+codexHookStateKey("windows", event)+"'={trusted_hash=\""+codexHookTrustHash(event, command)+"\"}")
	}
	assert.Equal(t, len(codexHookEvents), strings.Count(args[1], "trusted_hash="))
	assert.NotContains(t, strings.Join(args, " "), "bypass", "never Codex's --dangerously-bypass-hook-trust")
}

// The trust is given only with the switch on, on Windows, and only for a hooks folder nobody but an administrator can
// change; otherwise there is none and Codex shows its dialog as before.
func TestCodexHookTrustNeedsTheSwitchAndAProtectedFolder(t *testing.T) {
	oldSafe := codexHooksFolderSafe
	t.Cleanup(func() { codexHooksFolderSafe = oldSafe; codexTrustSwitch.Store(nil) })
	withHookTool(t)

	for _, tc := range []struct {
		name   string
		on     bool
		safe   bool
		trusts bool
	}{
		{"switch on, protected folder", true, true, true},
		{"switch on, folder others can change", true, false, false},
		{"switch off, protected folder", false, true, false},
		{"switch off, folder others can change", false, false, false},
	} {
		on, safe := tc.on, tc.safe
		SetCodexHookTrust(func() bool { return on })
		codexHooksFolderSafe = func(string) (bool, string) {
			if safe {
				return true, ""
			}
			return false, "this user can change it"
		}
		trusted, why := CodexHookTrust()
		args := strings.Join(codexHookArgs("sid-"+tc.name), " ")
		if runtime.GOOS != "windows" {
			assert.False(t, trusted, tc.name)
			assert.Contains(t, why, "only on Windows")
			assert.NotContains(t, args, "hooks.state", tc.name)
			continue
		}
		assert.Equal(t, tc.trusts, trusted, "%s: %s", tc.name, why)
		assert.Equal(t, tc.trusts, strings.Contains(args, "hooks.state={"), tc.name)
		assert.Contains(t, args, "hooks.SessionStart=", "%s: the hooks themselves are always passed", tc.name)
	}
	codexTrustSwitch.Store(nil)
	codexHooksFolderSafe = func(string) (bool, string) { return true, "" }
	trusted, _ := CodexHookTrust()
	assert.Equal(t, runtime.GOOS == "windows", trusted, "unset is on")
}
