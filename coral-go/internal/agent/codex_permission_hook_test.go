package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCodexVersionAtLeast(t *testing.T) {
	for v, want := range map[string]bool{
		"0.163.0": true, "0.163.1": true, "0.164.0": true, "0.164.0-alpha.2": true, "1.0.0": true, "0.200.5": true,
		"0.162.9": false, "0.155.1": false, "0.99.99": false, "": false, "codex": false, "0.163": false, "v0.163.0": false,
	} {
		assert.Equal(t, want, codexVersionAtLeast(v, codexPermissionRequestMin), "%q", v)
	}
}

func TestCodexHookEventsForTheVersion(t *testing.T) {
	assert.Equal(t, codexHookEvents, codexHookEventsFor(""), "an unknown version: the base hooks only")
	assert.Equal(t, codexHookEvents, codexHookEventsFor("0.155.1"))
	assert.Equal(t, append(append([]string(nil), codexHookEvents...), "PermissionRequest"), codexHookEventsFor("0.163.0"))
	assert.NotContains(t, codexHookEvents, "PermissionRequest", "the shared list is never changed")
}

// #6615: a Codex that has the PermissionRequest hook gets Coral's hook for it, with its trust entry; one that may not, or whose version is unknown, gets the five it always had.
func TestCodexLaunchInstallsPermissionRequestOnlyForACodexThatHasIt(t *testing.T) {
	old := CodexVersionProbe
	t.Cleanup(func() { CodexVersionProbe = old })
	launch := func(version string, probe bool) string {
		if probe {
			CodexVersionProbe = func(string) string { return version }
		} else {
			CodexVersionProbe = nil
		}
		cmd, err := (&CodexAgent{}).LaunchCommand(LaunchParams{SessionID: "11111111-2222-3333-4444-555555555555", SessionName: "codex-11111111-2222-3333-4444-555555555555", WorkingDir: t.TempDir()})
		assert.NoError(t, err)
		return cmd
	}
	for _, c := range []struct {
		version string
		probe   bool
		want    bool
	}{{"0.163.0", true, true}, {"0.170.2", true, true}, {"0.155.1", true, false}, {"", true, false}, {"unreadable", true, false}, {"", false, false}} {
		cmd := launch(c.version, c.probe)
		assert.Equal(t, c.want, strings.Contains(cmd, "hooks.PermissionRequest="), "version %q probe %v", c.version, c.probe)
		assert.Contains(t, cmd, "hooks.PostToolUse=", "the other hooks are installed as before")
	}
}

func TestCodexTrustEntriesFollowTheInstalledHooks(t *testing.T) {
	args := codexHookTrustArgsFor("windows", `C:\tools\hook.exe`, codexHookEventsFor("0.163.0"))
	assert.Equal(t, len(codexHookEvents)+1, strings.Count(args[1], "trusted_hash="), "one trust entry per hook installed")
	assert.Contains(t, args[1], ":permission_request:0:0")
	base := codexHookTrustArgsFor("windows", `C:\tools\hook.exe`, codexHookEventsFor("0.155.1"))
	assert.NotContains(t, base[1], "permission_request")
}
