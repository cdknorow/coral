//go:build !dev

package config

import (
	"strings"
	"testing"
)

// #4290: every other tier ignores CORAL_STRICT_LOCAL_AUTH=0, stays strict and names the flag.
func TestStrictLocalAuthEnvOffIsIgnoredInReleaseBuilds(t *testing.T) {
	for _, v := range []string{"0", "false", "no", "off", " OFF "} {
		t.Setenv("CORAL_STRICT_LOCAL_AUTH", v)
		on, w := StrictLocalAuthFromEnv()
		if !on || !strings.Contains(w, "--strict-local-auth=false") {
			t.Errorf("CORAL_STRICT_LOCAL_AUTH=%q: on=%v warning=%q, want on and a warning naming the flag", v, on, w)
		}
	}
}
