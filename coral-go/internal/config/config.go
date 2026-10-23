// Package config provides centralized configuration for the Coral server.
// All intervals, timeouts, and paths are collected here.
package config

import (
	"fmt"
	"github.com/subgentic/coral/internal/logsafe"
	"github.com/subgentic/coral/internal/testguard"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// PostHogKey is set at build time via ldflags for install tracking.
var PostHogKey string

// Version is set at build time via ldflags.
var Version string

// Build tier variables are set in tier_dev.go, tier_beta.go, or tier_prod.go
// based on compile-time build tags (-tags dev, -tags beta, or default).
// See those files for the values per tier.

// Config holds all server configuration values.
type Config struct {
	// coralDir is the root data directory for all Coral state.
	// Set by Load() from --home flag, CORAL_DATA_DIR, or ~/.coral.
	coralDir string

	// Database
	DBPath          string
	DBBusyTimeoutMS int

	// Server
	// Host is the --host value: one address or a comma list (Hosts, ListenHosts; #3594).
	Host string
	// HostExplicit is true when the host was asked for (--host or CORAL_HOST): the default, this computer only
	// (127.0.0.1), is widened to every interface when remote access is turned on (Settings > Privacy, #5124); an asked-for host is not.
	HostExplicit bool
	Port         int
	// NoAutoLoopback (--no-auto-loopback or CORAL_NO_AUTO_LOOPBACK=1): don't add the 127.0.0.1
	// listener Coral otherwise opens when Host names only network addresses (#3594).
	NoAutoLoopback bool
	// TLS is true when the server also speaks HTTPS on its port (--tls-cert/--tls-key,
	// #3355); set at startup once the certificate has loaded.
	TLS bool
	// StrictLocalAuth (#3418): a request from this computer needs a credential (an agent's
	// session token, the dashboard's sign-in cookie, the command-line signature or the API
	// key), and an identity that doesn't match its token is refused instead of logged. On
	// by default since #3936 (startup.Start sets it; --strict-local-auth=false turns it off, and
	// CORAL_STRICT_LOCAL_AUTH=0 does in a dev build only, #4290); never settable through the API, so neither
	// Coral Hub nor an agent can turn it off.
	StrictLocalAuth bool

	// Directories
	CoralRoot string
	LogDir    string

	// Background task intervals (seconds)
	IndexerIntervalS       int
	IndexerStartupDelayS   int
	GitPollerIntervalS     int
	BoardNotifierIntervalS int
	TaskIdleReminderS      int
	TaskIdleEscalationS    int
	RemotePollerIntervalS  int

	// HubMode turns this server into a multi-server hub: the remote server
	// registry, the /api/remote proxy and merged remote agents. Off by default;
	// set by the --hub flag or CORAL_HUB=1.
	HubMode bool
	// HubModeForced: HubMode came from --hub or CORAL_HUB=1, which win over the saved
	// Settings > Run as a hub (#3460); turning it off then means starting without them.
	HubModeForced bool
	// TerminalBackend is the backend this run uses, "tmux" or "pty" (#3634), and
	// TerminalBackendForced says it came from --backend, which wins over the saved
	// Settings > Terminal backend. Startup sets both.
	TerminalBackend       string
	TerminalBackendForced bool

	// CloudURL is the Cloud Hub origin this server may connect to as a connector
	// (--cloud-url or CORAL_CLOUD_URL). Empty (the default) means the connector is
	// off and this server never opens an outbound connection to the cloud.
	CloudURL string

	// CloudURLExplicit: CloudURL was given on purpose (--cloud-url or CORAL_CLOUD_URL),
	// not taken from the build tier's default.
	CloudURLExplicit bool

	// CloudConnect is whether this server offers to connect to Coral Hub on its own
	// (#3459), as --cloud-connect or CORAL_CLOUD_CONNECT set it: prompt or auto offer
	// it (the dashboard's first-launch screen, and the startup step), off never does,
	// and unset offers it only for an explicitly given URL (see EffectiveCloudConnect).
	// The build's default URL alone offers nothing. Coral Hub stays reachable either
	// way, from the home screen, Settings > Coral Hub and 'coral cloud connect', and an
	// enrolled server reconnects on its own.
	CloudConnect string

	// CloudMaxMode is the most Coral Hub may do on this computer when the process was started
	// with one: --cloud-max-mode or CORAL_CLOUD_MAX_MODE (view, interact or launch). It is the
	// launch-time form of Settings > Coral Hub > "Most the hub may do on this computer", for a
	// machine nobody sits at (a container, a CI runner). Whoever starts the process is the
	// machine's owner; the hub, a tunneled request and the API still cannot change the cap.
	// Empty leaves the saved setting alone (view if none).
	CloudMaxMode string

	// BootstrapFile and ProofFile name a one-time Coral Hub credential and its
	// optional proof (--bootstrap-file, --proof-file), redeemed at startup when
	// this server has no identity yet (#3149). Paths only: a credential is never
	// taken from the environment or argv. On Linux, without the flag, the
	// well-known /run/coral/bootstrap.d/credential is used if present.
	BootstrapFile string
	ProofFile     string

	// WebSocket
	WSPollIntervalS int
	// WSPollInterval, when set, replaces WSPollIntervalS. Only tests set it,
	// to tick faster than once a second.
	WSPollInterval time.Duration
	// WSRemoteDebounce, when set, replaces the one-second limit on rebuilds for remote feed
	// changes. Only tests set it, to sample remote latency without a second per sample.
	WSRemoteDebounce time.Duration

	// Task dependencies
	MaxTaskDependencyDepth int
}

// Load reads configuration from environment variables with sensible defaults.
// If dataDir is non-empty, it overrides all other data directory settings
// (CORAL_DATA_DIR, CORAL_DIR, ~/.coral). Pass "" to use the default.
func Load(dataDir ...string) *Config {
	homeDir, _ := os.UserHomeDir()
	var logDir string
	var override string
	if len(dataDir) > 0 {
		override = dataDir[0]
	}
	coralDir := ResolveCoralDir(override)

	// Ensure data directory exists: only this user may read it (#3552, Security's #3551 audit)
	testguard.CheckDataPath(coralDir)
	if err := EnsureDataDir(coralDir); err != nil {
		log.Printf("[config] data folder %s: %v", logsafe.Path(coralDir), err)
	}

	// Agent pipe logs (everything an agent's terminal shows) live in the data folder, never in
	// the shared system temp dir, where other users of the computer could read them (#3259 for
	// a second instance; #3601, weak site W17, for every install). Startup creates the folder
	// at 0700 and re-points a running agent's pipe there when it reattaches.
	logDir = filepath.Join(coralDir, "logs")

	envURL, envURLSet := os.LookupEnv("CORAL_CLOUD_URL")
	hubEnv := Truthy(os.Getenv("CORAL_HUB"))
	cfg := &Config{
		HubMode:          hubEnv,
		HubModeForced:    hubEnv,
		CloudURL:         CloudURLSetting(envURL, envURLSet),
		CloudURLExplicit: envURLSet && CloudURLSetting(envURL, envURLSet) != "",
		CloudConnect:     cloudConnectFromEnv(os.Getenv("CORAL_CLOUD_CONNECT")),
		CloudMaxMode:     strings.ToLower(strings.TrimSpace(os.Getenv("CORAL_CLOUD_MAX_MODE"))),
		coralDir:         coralDir,
		DBPath:           filepath.Join(coralDir, "sessions.db"),
		DBBusyTimeoutMS:  5000,

		Host:         envOrDefault("CORAL_HOST", "127.0.0.1"),
		HostExplicit: strings.TrimSpace(os.Getenv("CORAL_HOST")) != "",
		Port:         envIntOrDefault("CORAL_PORT", DefaultPort),

		CoralRoot: envOrDefault("CORAL_ROOT", homeDir),
		LogDir:    logDir,

		IndexerIntervalS:       120,
		IndexerStartupDelayS:   30,
		GitPollerIntervalS:     120,
		BoardNotifierIntervalS: 30,
		TaskIdleReminderS:      envIntOrDefault("CORAL_TASK_IDLE_REMINDER_SECONDS", 1800),
		TaskIdleEscalationS:    envIntOrDefault("CORAL_TASK_IDLE_ESCALATION_SECONDS", 3600),
		RemotePollerIntervalS:  30,

		WSPollIntervalS: 5,

		MaxTaskDependencyDepth: 3,
	}

	return cfg
}

// CloudURLSetting resolves the Coral Hub origin from a --cloud-url / CORAL_CLOUD_URL value:
// unset means the build tier's default (https://app.coralhub.dev in release builds, none in
// dev); "", "off", "none", "0" or "false" turn the connector off. A configured hub is only
// an address: nothing is sent to it until the user connects (#3221).
func CloudURLSetting(v string, set bool) string {
	if !set {
		return TierDefaultCloudURL
	}
	v = strings.TrimSpace(v)
	switch strings.ToLower(v) {
	case "", "off", "none", "0", "false":
		return ""
	}
	return v
}

// EULARequired returns true if the EULA acceptance dialog should be shown.
// False for dev tier (set at compile time via build tags).
func EULARequired() bool {
	return !TierSkipEULA
}

// ResolveCoralDir returns the data directory Load would use, without creating
// it: dataDir (the --home flag) if set, else CORAL_DATA_DIR, CORAL_DIR, or
// ~/.coral.
func ResolveCoralDir(dataDir string) string {
	switch {
	case dataDir != "":
		return ExtendedPath(dataDir)
	case os.Getenv("CORAL_DATA_DIR") != "":
		// CORAL_DATA_DIR takes precedence (matches Python), CORAL_DIR as fallback alias.
		return ExtendedPath(os.Getenv("CORAL_DATA_DIR"))
	case os.Getenv("CORAL_DIR") != "":
		return ExtendedPath(os.Getenv("CORAL_DIR"))
	}
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".coral")
}

// CoralDir returns the root data directory for all Coral state.
// Controlled by --home flag, CORAL_DATA_DIR env var, or defaults to ~/.coral.
func (c *Config) CoralDir() string {
	return c.coralDir
}

// UseCoralDir sets the data directory of a Config built by hand (Load sets it otherwise), so
// tests that make a Config{} get their own folder instead of "" (paths relative to wherever the
// test runs, shared by every test in the process: #3566).
func (c *Config) UseCoralDir(dir string) {
	c.coralDir = dir
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envIntOrDefault(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

// EffectiveCloudConnect is CloudConnect as it applies (#3459): an explicit setting
// as given; unset, prompt for an explicitly given Coral Hub URL and off otherwise
// (the build's default URL alone).
func (c *Config) EffectiveCloudConnect() string {
	if c.CloudConnect != "" {
		return c.CloudConnect
	}
	if c.CloudURLExplicit && c.CloudURL != "" {
		return "prompt"
	}
	return "off"
}

// OffersCloudConnect reports whether this server offers to connect to Coral Hub on its
// own: the first-launch screen and the startup step.
func (c *Config) OffersCloudConnect() bool {
	if c == nil || c.CloudURL == "" {
		return false
	}
	m := c.EffectiveCloudConnect()
	return m == "prompt" || m == "auto"
}

// cloudConnectFromEnv reads CORAL_CLOUD_CONNECT: prompt, auto or off, or the usual
// switch words (1, true, yes, on mean prompt; 0, false, no mean off). Anything else is
// reported and taken as off, rather than stopping a service over a typo; --cloud-connect
// itself is still refused when wrong.
func cloudConnectFromEnv(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	switch v {
	case "", "prompt", "auto", "off":
		return v
	case "1", "true", "yes", "on":
		return "prompt"
	case "0", "false", "no":
		return "off"
	}
	log.Printf("[config] WARNING: CORAL_CLOUD_CONNECT=%q is not prompt, auto or off: taken as off", v)
	return "off"
}

// StrictLocalAuthFromEnv reads CORAL_STRICT_LOCAL_AUTH (#3936): strict local auth is on
// unless it says off (0, false, no or off, in any case), and only a dev build honours that
// (#4290): another tier ignores an off, with a warning naming --strict-local-auth=false. Unset is
// on; a value that is neither on nor off is taken as on, with a warning for the log, so a typo
// never weakens it.
func StrictLocalAuthFromEnv() (on bool, warning string) {
	v := os.Getenv("CORAL_STRICT_LOCAL_AUTH")
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "false", "no", "off":
		if !StrictLocalAuthEnvHonoured {
			return true, fmt.Sprintf("[auth] WARNING: CORAL_STRICT_LOCAL_AUTH=%q is ignored in this build: strict local auth stays on. Start Coral with --strict-local-auth=false to turn it off", v)
		}
		return false, ""
	case "", "1", "true", "yes", "on":
		return true, ""
	}
	return true, fmt.Sprintf("[auth] WARNING: CORAL_STRICT_LOCAL_AUTH=%q is not on or off: taken as on", v)
}

// Truthy reads an on/off value, from the environment or a stored setting: 1, true, yes or on, in
// any case, around spaces. The one helper for it (#3824: there were six copies).
// Falsy is the settings' words for off: false, 0, no and off, in any case. A setting that is on unless it is turned off (telemetry,
// the board health monitor) is read with it, not with a comparison to the one word "false": PUT /api/settings accepts all four, and a
// JSON false is stored as "False" (#5129).
func Falsy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "false", "no", "off":
		return true
	}
	return false
}

func Truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
