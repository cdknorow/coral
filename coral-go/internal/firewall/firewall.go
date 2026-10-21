// Package firewall reads, and never changes, the Windows Firewall rules that name Coral's own
// programs, so Coral can say when one is broader than it needs to be (#97, audit G4A).
//
// Coral creates no firewall rule, and neither does its installer. When a program first listens
// on a non-loopback address Windows shows its own "allow this app" prompt, and answering it makes a
// rule for the whole PROGRAM on the profiles ticked (Public is ticked by default for an app in a
// user folder): every port, TCP and UDP, from any address. Coral only warns and says how to narrow
// or remove such a rule; changing one needs elevation and is the person's decision.
//
// Only the rules in the firewall's own store (the registry key the Windows Firewall console edits) are read. Rules a Group Policy
// sets (HKLM\SOFTWARE\Policies\Microsoft\WindowsFirewall\FirewallRules) are an administrator's and are not looked at.
package firewall

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/subgentic/coral/internal/psquote"
)

// Rule is one Windows Firewall rule as the registry stores it, reduced to what Coral looks at.
type Rule struct {
	ID       string   // the registry value name: what Set-NetFirewallRule -Name and Remove-NetFirewallRule -Name take
	Name     string   // the display name
	Allow    bool     // Action=Allow
	Active   bool     // Active=TRUE
	Inbound  bool     // Dir=In
	Profiles []string // Domain, Private, Public; empty means every profile
	LPorts   []string // local ports or ranges; empty means every port
	App      string   // the program the rule names
	Protocol string   // 6 TCP, 17 UDP; empty means any
}

// ParseRule reads a FirewallRules registry value ("v2.10|Action=Allow|Active=TRUE|Dir=In|Profile=Public|App=C:\x.exe|Name=x|...").
// ok is false for a value that is not a rule of that format.
func ParseRule(id, value string) (r Rule, ok bool) {
	parts := strings.Split(value, "|")
	if len(parts) < 2 || !strings.HasPrefix(parts[0], "v2.") {
		return Rule{}, false
	}
	r.ID = id
	for _, p := range parts[1:] {
		k, v, found := strings.Cut(p, "=")
		if !found {
			continue
		}
		switch k {
		case "Action":
			r.Allow = strings.EqualFold(v, "Allow")
		case "Active":
			r.Active = strings.EqualFold(v, "TRUE")
		case "Dir":
			r.Inbound = strings.EqualFold(v, "In")
		case "Profile":
			r.Profiles = append(r.Profiles, v)
		case "LPort", "LPort2_10", "LPort2_20":
			r.LPorts = append(r.LPorts, v)
		case "Protocol":
			r.Protocol = v
		case "App":
			r.App = v
		case "Name":
			r.Name = v
		}
	}
	return r, true
}

// Finding is a rule broader than Coral needs, and why.
type Finding struct {
	Rule Rule
	Why  string // "on the Public profile, for every port"
}

// coralPrograms are the executables of Coral's that listen or may be started to: the rule is about the program, wherever it is installed.
var coralPrograms = map[string]bool{"coral.exe": true, "coral-tray.exe": true, "coral-app.exe": true, "launch-coral.exe": true}

// namesCoral reports whether app is one of Coral's programs: by name, or any coral*.exe beside the running executable exe (a
// renamed or dev build such as coral-fix-ready.exe).
func namesCoral(app, exe string) bool {
	if app == "" {
		return false
	}
	base := strings.ToLower(filepath.Base(strings.ReplaceAll(app, `\`, "/")))
	if coralPrograms[base] {
		return true
	}
	if exe != "" && strings.HasPrefix(base, "coral") && strings.HasSuffix(base, ".exe") {
		dir := strings.ToLower(filepath.Dir(strings.ReplaceAll(app, `\`, "/")))
		return dir == strings.ToLower(filepath.Dir(strings.ReplaceAll(exe, `\`, "/")))
	}
	return false
}

func (r Rule) onPublic() bool {
	if len(r.Profiles) == 0 {
		return true // no Profile entry: every profile
	}
	for _, p := range r.Profiles {
		if strings.EqualFold(p, "Public") || strings.EqualFold(p, "All") {
			return true
		}
	}
	return false
}

// profilesOverlap reports whether two rules apply to a common profile (no entry means every profile).
func profilesOverlap(a, b Rule) bool {
	if len(a.Profiles) == 0 || len(b.Profiles) == 0 {
		return true
	}
	for _, p := range a.Profiles {
		for _, q := range b.Profiles {
			if strings.EqualFold(p, q) || strings.EqualFold(p, "All") || strings.EqualFold(q, "All") {
				return true
			}
		}
	}
	return false
}

// blockedAnyway reports whether an active inbound BLOCK rule names the same program on a common profile: Windows lets a block win
// over an allow, so the allow is not in effect.
func blockedAnyway(rules []Rule, allow Rule) bool {
	for _, b := range rules {
		if !b.Allow && b.Active && b.Inbound && strings.EqualFold(b.App, allow.App) && profilesOverlap(b, allow) {
			return true
		}
	}
	return false
}

// Assess returns the active inbound ALLOW rules for Coral's programs (exe is the running executable) that are on the Public
// profile or on no profile in particular (so on Public too). A rule on the Private (or Domain) profile only is what Coral
// recommends and is NOT a finding, whatever its ports: the Windows prompt always makes a program-wide rule. A Block rule, an outbound
// rule, an inactive one and an allow that a matching Block rule overrides are not findings either. A UDP allow on Public is noted
// as unneeded: Coral listens on TCP only.
func Assess(rules []Rule, exe string) []Finding {
	var out []Finding
	for _, r := range rules {
		if !r.Allow || !r.Active || !r.Inbound || !namesCoral(r.App, exe) || !r.onPublic() || blockedAnyway(rules, r) {
			continue
		}
		why := "on the Public profile"
		if len(r.LPorts) == 0 {
			why += ", for every port"
		}
		if r.Protocol == "17" {
			why += "; it is a UDP rule and Coral listens on TCP only, so it can simply be removed"
		}
		out = append(out, Finding{Rule: r, Why: why})
	}
	return out
}

// expandPercent replaces each %NAME% in s with get(NAME) (an unset name stays as it was), as Windows expands environment strings.
func expandPercent(s string, get func(string) string) string {
	var b strings.Builder
	for {
		i := strings.IndexByte(s, '%')
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		j := strings.IndexByte(s[i+1:], '%')
		if j < 0 {
			b.WriteString(s)
			return b.String()
		}
		name := s[i+1 : i+1+j]
		b.WriteString(s[:i])
		if v := get(name); name != "" && v != "" {
			b.WriteString(v)
		} else {
			b.WriteString(s[i : i+j+2])
		}
		s = s[i+j+2:]
	}
}

// Fix is the commands that narrow the rule to Private networks or remove it, for an elevated PowerShell.
func (f Finding) Fix() string {
	id := psquote.Literal(f.Rule.ID) // PowerShell also ends a quote at the curly single quotes: psquote doubles those too (#3904)
	return fmt.Sprintf("Set-NetFirewallRule -Name %s -Profile Private   (or: Remove-NetFirewallRule -Name %s)", id, id)
}

// Message is the warning for findings, or "" for none.
func Message(findings []Finding) string {
	if len(findings) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Windows Firewall allows incoming connections to Coral's programs more widely than needed. Coral never makes firewall rules; these came from Windows' own \"allow this app\" prompt, which allows the whole program on every port. Allow Private networks only. In an administrator PowerShell:")
	for _, f := range findings {
		fmt.Fprintf(&b, "\n- %s (%s) %s: %s", f.Rule.Name, f.Rule.App, f.Why, f.Fix())
	}
	return b.String()
}

// BindWarning is the line logged before Coral listens on a non-loopback address on Windows, where the first such listen
// makes Windows ask whether to allow the program; "" on other systems and for a loopback-only bind.
func BindWarning(host, goos string, loopbackOnly bool) string {
	if goos != "windows" || loopbackOnly {
		return ""
	}
	return fmt.Sprintf("[startup] Coral will listen on %q, reachable from other computers. Windows will offer a firewall prompt for this program the first time: answering Allow creates a rule for the WHOLE program (every port, TCP and UDP) on the networks you tick. Tick Private networks only, not Public.", host)
}

var (
	mu       sync.RWMutex
	findings []Finding
)

// Set records the findings of the startup check, for the dashboard.
func Set(f []Finding) { mu.Lock(); findings = f; mu.Unlock() }

// Warning is the startup check's message ("" when there is nothing to say), shown on the dashboard.
func Warning() string { mu.RLock(); defer mu.RUnlock(); return Message(findings) }
