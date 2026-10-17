//go:build !dev

package config

// StrictLocalAuthEnvHonoured: see strict_env_dev.go. Release tiers ignore CORAL_STRICT_LOCAL_AUTH=0.
const StrictLocalAuthEnvHonoured = false
