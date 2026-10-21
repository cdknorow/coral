//go:build windows

package firewall

import (
	"golang.org/x/sys/windows/registry"
)

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
			rules = append(rules, r)
		}
	}
	return rules, nil
}
