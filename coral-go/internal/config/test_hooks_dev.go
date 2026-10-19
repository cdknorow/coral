//go:build dev

package config

// TestHooksEnvHonoured: a dev build reads CORAL_TEST_HOOKS=1 (the dashboard test suites set it) and tells the page to install
// window.__coralTest, the surface the suites reach into (#5015). Every other tier ignores it: a release page never has the hooks.
const TestHooksEnvHonoured = true
