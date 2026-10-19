//go:build !dev

package config

// TestHooksEnvHonoured: see test_hooks_dev.go. Release tiers ignore CORAL_TEST_HOOKS.
const TestHooksEnvHonoured = false
