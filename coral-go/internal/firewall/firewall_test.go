package firewall

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	publicAll   = `v2.10|Action=Allow|Active=TRUE|Dir=In|Protocol=6|Profile=Public|App=C:\program files (x86)\coral\coral-tray.exe|Name=coral-tray|Desc=coral-tray|Defer=User|`
	privateAll  = `v2.10|Action=Allow|Active=TRUE|Dir=In|Protocol=6|Profile=Private|App=C:\Program Files\Coral\coral.exe|Name=coral|`
	privatePort = `v2.10|Action=Allow|Active=TRUE|Dir=In|Protocol=6|Profile=Private|LPort=8420|App=C:\Program Files\Coral\coral.exe|Name=coral 8420|`
	publicPort  = `v2.10|Action=Allow|Active=TRUE|Dir=In|Protocol=6|Profile=Public|LPort=8420|App=C:\Program Files\Coral\coral.exe|Name=coral p|`
	anyProfile  = `v2.10|Action=Allow|Active=TRUE|Dir=In|LPort2_10=8420|App=C:\Program Files\Coral\coral.exe|Name=coral any|`
	blocked     = `v2.10|Action=Block|Active=TRUE|Dir=In|Protocol=6|Profile=Public|App=C:\users\x\startup.test.exe|Name=startup.test|`
	blockCoral  = `v2.10|Action=Block|Active=TRUE|Dir=In|Protocol=6|Profile=Public|App=C:\Program Files\Coral\coral.exe|Name=coral block|`
	outbound    = `v2.10|Action=Allow|Active=TRUE|Dir=Out|Profile=Public|App=C:\Program Files\Coral\coral.exe|Name=coral out|`
	inactive    = `v2.10|Action=Allow|Active=FALSE|Dir=In|Profile=Public|App=C:\Program Files\Coral\coral.exe|Name=coral off|`
	otherApp    = `v2.10|Action=Allow|Active=TRUE|Dir=In|Profile=Public|App=C:\Program Files\Other\other.exe|Name=other|`
)

func rule(t *testing.T, id, v string) Rule {
	r, ok := ParseRule(id, v)
	require.True(t, ok, v)
	return r
}

func TestParseRuleReadsTheFieldsCoralLooksAt(t *testing.T) {
	r := rule(t, "TCP Query User{G}C:\\x", publicAll)
	assert.Equal(t, "TCP Query User{G}C:\\x", r.ID)
	assert.Equal(t, "coral-tray", r.Name)
	assert.True(t, r.Allow && r.Active && r.Inbound)
	assert.Equal(t, []string{"Public"}, r.Profiles)
	assert.Empty(t, r.LPorts)
	assert.Equal(t, `C:\program files (x86)\coral\coral-tray.exe`, r.App)
	assert.Equal(t, []string{"8420"}, rule(t, "a", anyProfile).LPorts, "the v2.10 port field")

	for _, bad := range []string{"", "garbage", "v1.0", "no-version|Action=Allow"} {
		_, ok := ParseRule("x", bad)
		assert.False(t, ok, bad)
	}
}

func TestAssessReportsOnlyBroadInboundAllowRulesForCoral(t *testing.T) {
	exe := `C:\Program Files\Coral\coral.exe`
	var rules []Rule
	for i, v := range []string{publicAll, privateAll, privatePort, publicPort, anyProfile, blocked, outbound, inactive, otherApp} {
		rules = append(rules, rule(t, string(rune('a'+i)), v))
	}
	got := map[string]string{}
	for _, f := range Assess(rules, exe) {
		got[f.Rule.Name] = f.Why
	}
	assert.Equal(t, map[string]string{
		"coral-tray": "on the Public profile, for every port",
		"coral p":    "on the Public profile",
		"coral any":  "on the Public profile", // no Profile entry: every profile
	}, got)
	// Private + every port (what Coral recommends), Private + one port, Block, outbound, inactive, another program and a test
	// binary: nothing.
}

// M1: the rule Coral tells the person to make (Private networks only, which the Windows prompt makes program-wide) is not a finding.
func TestAPrivateOnlyProgramWideRuleIsNotAFinding(t *testing.T) {
	exe := `C:\Program Files\Coral\coral.exe`
	assert.Empty(t, Assess([]Rule{rule(t, "a", privateAll)}, exe), "Private, TCP, every port")
	udpPrivate := rule(t, "b", `v2.10|Action=Allow|Active=TRUE|Dir=In|Protocol=17|Profile=Private|App=C:\Program Files\Coral\coral.exe|Name=coral|`)
	assert.Empty(t, Assess([]Rule{udpPrivate}, exe), "the UDP half of the same prompt, Private")
	domain := rule(t, "c", `v2.10|Action=Allow|Active=TRUE|Dir=In|Profile=Domain|App=C:\Program Files\Coral\coral.exe|Name=coral|`)
	assert.Empty(t, Assess([]Rule{domain}, exe), "Domain only")
	multi := rule(t, "d", `v2.10|Action=Allow|Active=TRUE|Dir=In|Profile=Domain|Profile=Private|App=C:\Program Files\Coral\coral.exe|Name=coral|`)
	assert.Empty(t, Assess([]Rule{multi}, exe), "Domain and Private")
}

func TestExpandPercentExpandsKnownNamesOnly(t *testing.T) {
	env := map[string]string{"ProgramFiles": `C:\Program Files`, "EMPTY": ""}
	get := func(n string) string { return env[n] }
	assert.Equal(t, `C:\Program Files\Coral\coral.exe`, expandPercent(`%ProgramFiles%\Coral\coral.exe`, get))
	assert.Equal(t, `%UNKNOWN%\x.exe`, expandPercent(`%UNKNOWN%\x.exe`, get), "an unset name stays")
	assert.Equal(t, `%EMPTY%\x`, expandPercent(`%EMPTY%\x`, get))
	assert.Equal(t, `100%\x`, expandPercent(`100%\x`, get), "a lone percent sign stays")
	assert.Equal(t, `%%\x`, expandPercent(`%%\x`, get))
	assert.Equal(t, `C:\Program Files\a\C:\Program Files`, expandPercent(`%ProgramFiles%\a\%ProgramFiles%`, get))
}

func TestAPublicUDPRuleIsFlaggedAsUnneeded(t *testing.T) {
	exe := `C:\Program Files\Coral\coral.exe`
	udp := rule(t, "a", `v2.10|Action=Allow|Active=TRUE|Dir=In|Protocol=17|Profile=Public|App=C:\Program Files\Coral\coral.exe|Name=coral|`)
	got := Assess([]Rule{udp}, exe)
	require.Len(t, got, 1)
	assert.Contains(t, got[0].Why, "UDP rule and Coral listens on TCP only")
}

// L4: an active inbound Block rule for the same program on a common profile wins over the allow, so the allow is not in effect.
func TestAMatchingBlockRuleOverridesTheAllow(t *testing.T) {
	exe := `C:\Program Files\Coral\coral.exe`
	allow := rule(t, "a", `v2.10|Action=Allow|Active=TRUE|Dir=In|Protocol=6|Profile=Public|App=C:\Program Files\Coral\coral.exe|Name=coral|`)
	block := rule(t, "b", `v2.10|Action=Block|Active=TRUE|Dir=In|Protocol=6|Profile=Public|App=C:\Program Files\Coral\coral.exe|Name=coral block|`)
	blockPrivate := rule(t, "c", `v2.10|Action=Block|Active=TRUE|Dir=In|Profile=Private|App=C:\Program Files\Coral\coral.exe|Name=coral block private|`)
	blockInactive := rule(t, "d", `v2.10|Action=Block|Active=FALSE|Dir=In|Profile=Public|App=C:\Program Files\Coral\coral.exe|Name=coral block off|`)
	assert.Empty(t, Assess([]Rule{allow, block}, exe))
	assert.Len(t, Assess([]Rule{allow, blockPrivate}, exe), 1, "a block on another profile does not cancel it")
	assert.Len(t, Assess([]Rule{allow, blockInactive}, exe), 1, "an inactive block does not")
}

func TestACoralBuildBesideTheRunningExecutableCounts(t *testing.T) {
	dev := rule(t, "a", `v2.10|Action=Allow|Active=TRUE|Dir=In|Profile=Public|App=D:\software\coral\coral-go\coral-fix-ready.exe|Name=coral-fix-ready|`)
	assert.Len(t, Assess([]Rule{dev}, `D:\software\coral\coral-go\coral.exe`), 1, "a coral*.exe in the running executable's folder")
	assert.Empty(t, Assess([]Rule{dev}, `C:\elsewhere\coral.exe`), "a coral*.exe in another folder is not Coral's")
	assert.Empty(t, Assess([]Rule{dev}, ""))
}

func TestFixNamesTheRuleExactlyAndMessageListsEachRule(t *testing.T) {
	r := rule(t, `TCP Query User{26C1}C:\x\it's coral.exe`, strings.Replace(publicAll, "coral-tray.exe", "coral.exe", 1))
	f := Finding{Rule: r, Why: "on the Public profile"}
	assert.Contains(t, f.Fix(), `Set-NetFirewallRule -Name 'TCP Query User{26C1}C:\x\it''s coral.exe' -Profile Private`)
	assert.Contains(t, f.Fix(), `Remove-NetFirewallRule -Name 'TCP Query User{26C1}C:\x\it''s coral.exe'`)
	msg := Message([]Finding{f, f})
	assert.Equal(t, 2, strings.Count(msg, "\n- "))
	assert.Contains(t, msg, "Coral never makes firewall rules")
	assert.Empty(t, Message(nil))
}

func TestBindWarningOnlyForANetworkBindOnWindows(t *testing.T) {
	w := BindWarning("0.0.0.0", "windows", false)
	assert.Contains(t, w, "firewall prompt")
	assert.Contains(t, w, "Private networks only")
	assert.Empty(t, BindWarning("127.0.0.1", "windows", true), "loopback: no prompt, no warning")
	assert.Empty(t, BindWarning("0.0.0.0", "linux", false))
	assert.Empty(t, BindWarning("0.0.0.0", "darwin", false))
}

func TestWarningFollowsTheRecordedFindings(t *testing.T) {
	Set(nil)
	assert.Empty(t, Warning())
	Set([]Finding{{Rule: rule(t, "a", publicAll), Why: "on the Public profile"}})
	assert.Contains(t, Warning(), "coral-tray")
	Set(nil)
	assert.Empty(t, Warning())
}
