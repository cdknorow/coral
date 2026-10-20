//go:build relaunchfixture

package config

// BuildFlavor marks a build made with the relaunchfixture tag (#5452): the relaunch routes are on and a fake updater answers from a fixture folder. It is in coral --version,
// the first startup log line, /api/health's version and the hub handshake's version, so such a build can always be told apart and a shipped binary can be checked for it.
const BuildFlavor = "+relaunchfixture"
