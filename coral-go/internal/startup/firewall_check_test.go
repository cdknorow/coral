package startup

import (
	"bytes"
	"context"
	"errors"
	"log"
	"testing"
	"time"

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
	last := checkFirewall(func() (string, error) { return `C:\Program Files\Coral\coral.exe`, nil }, "")
	assert.Contains(t, out.String(), "[firewall] WARNING")
	assert.Contains(t, out.String(), "Set-NetFirewallRule -Name 'a' -Profile Private")
	assert.NotContains(t, out.String(), "private 8420", "a Private, one-port rule is fine")
	assert.NotContains(t, out.String(), "other.exe")
	assert.Contains(t, firewall.Warning(), "coral")
	assert.Equal(t, firewall.Warning(), last, "the message in effect is returned")

	// L1: the same finding is not logged again by a later look, and the dashboard keeps showing it.
	before := out.Len()
	checkFirewall(func() (string, error) { return `C:\Program Files\Coral\coral.exe`, nil }, last)
	assert.Equal(t, before, out.Len(), "logged once")
	assert.NotEmpty(t, firewall.Warning())
}

// L1: a rule the person creates after the first look (they answer Windows' prompt later) is found by a later look.
func TestALaterLookFindsARuleCreatedAfterTheFirst(t *testing.T) {
	out := captureLog(t)
	stubRules(t, `v2.10|Action=Allow|Active=TRUE|Dir=In|Profile=Private|App=C:\Program Files\Coral\coral.exe|Name=coral|`)
	last := checkFirewall(func() (string, error) { return `C:\Program Files\Coral\coral.exe`, nil }, "")
	assert.Empty(t, last)
	assert.Empty(t, out.String())
	stubRules(t, `v2.10|Action=Allow|Active=TRUE|Dir=In|Profile=Public|App=C:\Program Files\Coral\coral.exe|Name=coral|`)
	last = checkFirewall(func() (string, error) { return `C:\Program Files\Coral\coral.exe`, nil }, last)
	assert.NotEmpty(t, last)
	assert.Contains(t, out.String(), "[firewall] WARNING")
}

// The later looks end with the server's context: a shutdown leaves no goroutine sleeping for an hour (the startup leak check).
func TestRunFirewallChecksStopsWhenTheContextEnds(t *testing.T) {
	captureLog(t)
	stubRules(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runFirewallChecks(ctx, func() (string, error) { return `C:\Program Files\Coral\coral.exe`, nil }, []time.Duration{time.Hour, time.Hour})
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("it returned before the context ended or a wait passed")
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("it was still waiting after the context ended")
	}
}

// With short waits every look runs: one now and one after each wait.
func TestRunFirewallChecksLooksAfterEachWait(t *testing.T) {
	captureLog(t)
	var looks int
	old := firewallRules
	firewallRules = func() ([]firewall.Rule, error) { looks++; return nil, nil }
	t.Cleanup(func() { firewallRules = old })
	runFirewallChecks(context.Background(), func() (string, error) { return "", nil }, []time.Duration{time.Millisecond, time.Millisecond})
	assert.Equal(t, 3, looks)
}

func TestCheckFirewallSaysNothingWhenTheRulesAreNarrowAndKeepsGoingOnAReadError(t *testing.T) {
	out := captureLog(t)
	stubRules(t, `v2.10|Action=Allow|Active=TRUE|Dir=In|Profile=Private|LPort=8420|App=C:\Program Files\Coral\coral.exe|Name=coral|`)
	checkFirewall(func() (string, error) { return `C:\Program Files\Coral\coral.exe`, nil }, "")
	assert.Empty(t, out.String())
	assert.Empty(t, firewall.Warning())

	old := firewallRules
	firewallRules = func() ([]firewall.Rule, error) { return nil, errors.New("access denied") }
	t.Cleanup(func() { firewallRules = old })
	checkFirewall(func() (string, error) { return "", nil }, "")
	assert.Contains(t, out.String(), "could not read the Windows Firewall rules")
	assert.Empty(t, firewall.Warning(), "an unreadable store is not a finding")
}
