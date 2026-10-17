//go:build dev

package config

import "testing"

// #4290: a dev build honours CORAL_STRICT_LOCAL_AUTH=0 (the test suites use it).
func TestStrictLocalAuthEnvOffIsHonouredInDevBuilds(t *testing.T) {
	t.Setenv("CORAL_STRICT_LOCAL_AUTH", "0")
	if on, w := StrictLocalAuthFromEnv(); on || w != "" {
		t.Errorf("dev build: on=%v warning=%q, want off and no warning", on, w)
	}
}
