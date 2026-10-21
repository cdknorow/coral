//go:build windows

package firewall

import (
	"os"

	"golang.org/x/sys/windows/registry"
)

// expandApp expands environment strings in a rule's App (%ProgramFiles%\Coral\coral.exe, %USERPROFILE%\...), which the firewall
// stores unexpanded for rules made by hand or by a script; the name match works either way, but the "beside the running
// executable" match compares folders.
func expandApp(app string) string { return expandPercent(app, os.Getenv) }

// rulesKey holds the firewall's rules as one string value each. It is readable without elevation (the rules a person sees in
// "Windows Defender Firewall with Advanced Security" come from it), and Coral only reads it.
const rulesKey = `SYSTEM\CurrentControlSet\Services\SharedAccess\Parameters\FirewallPolicy\FirewallRules`

// ReadRules is every firewall rule in the registry that parses; a read-only open, so no elevation and no change.
func ReadRules() ([]Rule, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, rulesKey, registry.QUERY_VALUE)
	if err != nil {
		return nil, err
	}
	defer k.Close()
	names, err := k.ReadValueNames(-1)
	if err != nil {
		return nil, err
	}
	rules := make([]Rule, 0, len(names))
	for _, n := range names {
		v, _, err := k.GetStringValue(n)
		if err != nil {
			continue
		}
		if r, ok := ParseRule(n, v); ok {
			r.App = expandApp(r.App)
			rules = append(rules, r)
		}
	}
	return rules, nil
}
