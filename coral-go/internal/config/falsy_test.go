package config

import "testing"

// The words for off (#5129): the same four in any case and with white space, nothing else (an empty or unknown value is not off).
func TestFalsy(t *testing.T) {
	for v, want := range map[string]bool{"false": true, "False": true, "FALSE": true, " false ": true, "0": true, "no": true, "No": true, "off": true, "OFF": true,
		"": false, "true": false, "True": false, "1": false, "yes": false, "on": false, "garbage": false, "falsey": false, "00": false} {
		if got := Falsy(v); got != want {
			t.Errorf("Falsy(%q) = %v, want %v", v, got, want)
		}
	}
}

// Falsy and Truthy do not overlap.
func TestFalsyAndTruthyDoNotOverlap(t *testing.T) {
	for _, v := range []string{"true", "false", "0", "1", "yes", "no", "on", "off", "", "x", "True", "False"} {
		if Falsy(v) && Truthy(v) {
			t.Errorf("%q is both", v)
		}
	}
}
