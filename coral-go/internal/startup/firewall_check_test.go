package startup

import (
	"bytes"
	"errors"
	"log"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/firewall"
)

func captureLog(t *testing.T) *bytes.Buffer {
	var b bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&b)
	t.Cleanup(func() { log.SetOutput(prev); firewall.Set(nil) })
	return &b
}

func stubRules(t *testing.T, values ...string) {
	var rules []firewall.Rule
	for i, v := range values {
		r, ok := firewall.ParseRule(string(rune('a'+i)), v)
		require.True(t, ok, v)
		rules = append(rules, r)
	}
	old := firewallRules
	firewallRules = func() ([]firewall.Rule, error) { return rules, nil }
	t.Cleanup(func() { firewallRules = old })
}

// #97: a broad inbound allow rule for Coral's program is logged and shown on the dashboard, with the command that narrows it; the
// check only reads.
func TestCheckFirewallWarnsAboutABroadRuleForCoral(t *testing.T) {
	out := captureLog(t)
	stubRules(t,
		`v2.10|Action=Allow|Active=TRUE|Dir=In|Protocol=6|Profile=Public|App=C:\Program Files\Coral\coral.exe|Name=coral|`,
		`v2.10|Action=Allow|Active=TRUE|Dir=In|Profile=Private|LPort=8420|App=C:\Program Files\Coral\coral.exe|Name=coral private 8420|`,
		`v2.10|Action=Allow|Active=TRUE|Dir=In|Profile=Public|App=C:\Program Files\Other\other.exe|Name=other|`)
	checkFirewall(func() (string, error) { return `C:\Program Files\Coral\coral.exe`, nil })
	assert.Contains(t, out.String(), "[firewall] WARNING")
	assert.Contains(t, out.String(), "Set-NetFirewallRule -Name 'a' -Profile Private")
	assert.NotContains(t, out.String(), "private 8420", "a Private, one-port rule is fine")
	assert.NotContains(t, out.String(), "other.exe")
	assert.Contains(t, firewall.Warning(), "coral")
}

func TestCheckFirewallSaysNothingWhenTheRulesAreNarrowAndKeepsGoingOnAReadError(t *testing.T) {
	out := captureLog(t)
	stubRules(t, `v2.10|Action=Allow|Active=TRUE|Dir=In|Profile=Private|LPort=8420|App=C:\Program Files\Coral\coral.exe|Name=coral|`)
	checkFirewall(func() (string, error) { return `C:\Program Files\Coral\coral.exe`, nil })
	assert.Empty(t, out.String())
	assert.Empty(t, firewall.Warning())

	old := firewallRules
	firewallRules = func() ([]firewall.Rule, error) { return nil, errors.New("access denied") }
	t.Cleanup(func() { firewallRules = old })
	checkFirewall(func() (string, error) { return "", nil })
	assert.Contains(t, out.String(), "could not read the Windows Firewall rules")
	assert.Empty(t, firewall.Warning(), "an unreadable store is not a finding")
}
