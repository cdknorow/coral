package secrets

import (
	"strings"
	"testing"
)

// QA on #5158: places the gate must still look at. Each text is one the rules redact over the whole text, so the gated spans must equal them.
func TestKeywordGateKeepsWhatTheRulesFindInOddText(t *testing.T) {
	v := "Xk9mQ2vL7pR4tZ8wN3bY"
	for name, text := range map[string]string{
		"a long s (U+017F) in the name, which (?i) folds to s": "paſſword=" + v,
		"a Kelvin sign (U+212A) in the name, which folds to k": "api_Key=" + v,
		"a Kelvin sign in token":                               "toKen: " + v,
		"a long s in mysql":                                    "myſql -u r -p" + v,
		"white space between the separator and the value":      "api_key=" + strings.Repeat(" ", 9000) + v,
		"newlines between the separator and the value":         "token:" + strings.Repeat("\n", 9000) + v,
		"a flag name of 60 characters":                         "tool --" + strings.Repeat("a-", 30) + "password " + v,
		"a flag name of 100 characters":                        "tool --" + strings.Repeat("ab-", 34) + "token " + v,
	} {
		sameCoverage(t, name, text)
		sameCoverage(t, name+" in lines", "pre\n"+text+"\npost: 1\n")
	}
}
