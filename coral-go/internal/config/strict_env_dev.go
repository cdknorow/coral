//go:build dev

package config

// StrictLocalAuthEnvHonoured: a dev build reads CORAL_STRICT_LOCAL_AUTH=0 (test servers and the
// test suites use it). Every other tier ignores it (#4290): turning strict local auth off there is
// the command line's --strict-local-auth=false, which a stray environment variable can't do.
const StrictLocalAuthEnvHonoured = true
