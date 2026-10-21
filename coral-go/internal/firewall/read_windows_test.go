//go:build windows

package firewall

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
