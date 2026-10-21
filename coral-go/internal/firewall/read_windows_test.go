//go:build windows

package firewall

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// L2: environment strings in a rule's App are expanded, so a coral*.exe written as %USERPROFILE%\... is matched beside the running executable.
func TestExpandAppExpandsEnvironmentStrings(t *testing.T) {
	t.Setenv("CORAL_FW_TEST_DIR", `D:\dev\coral`)
	assert.Equal(t, `D:\dev\coral\coral-fix-ready.exe`, expandApp(`%CORAL_FW_TEST_DIR%\coral-fix-ready.exe`))
	assert.Equal(t, `C:\plain\coral.exe`, expandApp(`C:\plain\coral.exe`))
	r, ok := ParseRule("a", `v2.10|Action=Allow|Active=TRUE|Dir=In|Profile=Public|App=%CORAL_FW_TEST_DIR%\coral-fix-ready.exe|Name=dev|`)
	require.True(t, ok)
	r.App = expandApp(r.App)
	assert.Len(t, Assess([]Rule{r}, `D:\dev\coral\coral.exe`), 1)
}

// A read-only look at this machine's real rule store: it must open without elevation and parse rules. It says nothing about
// which rules exist, so it passes on a machine with none of Coral's.
func TestReadRulesOpensTheRealStoreWithoutElevation(t *testing.T) {
	rules, err := ReadRules()
	require.NoError(t, err)
	assert.NotEmpty(t, rules, "a Windows machine has firewall rules")
	exe, _ := os.Executable()
	for _, f := range Assess(rules, exe) {
		t.Logf("broad rule: %s (%s) %s", f.Rule.Name, f.Rule.App, f.Why)
	}
}
