package startup

import (
	"context"
	"fmt"
	"github.com/subgentic/coral/internal/backendchoice"
	"log"
	"os"
	"runtime"

	"github.com/subgentic/coral/internal/agentprio"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/inputlog"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/server/routes"
	setreg "github.com/subgentic/coral/internal/settings"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/tmux"
	"github.com/subgentic/coral/internal/tracking"
	"github.com/subgentic/coral/pkg/tunnel"
)

// applyStartupSettings applies the saved settings the terminal needs before any backend starts: the replay buffer
// size, the terminal input debug log (#3256: off unless the setting, CORAL_DEBUG_INPUT or --debug-input), the agents'
// priority (#3437: the setting, or --agent-priority / CORAL_AGENT_PRIORITY), and the terminal backend (#3634:
// --backend, else the saved setting, else the OS default), into opts and cfg. A tmux backend without tmux is
// logged, not fatal: the dashboard still loads and shows install instructions; launches fail until it is installed.
func applyStartupSettings(ctx context.Context, db *store.DB, cfg *config.Config, opts *Options, backendFlag string) {
	applyReplayBytesFromSettings(ctx, db)
	inputlog.Setup(cfg.CoralDir(), opts.DebugInput || inputlog.EnabledFromEnv())
	// A store already at 80% of the 20 GiB default would start refusing uploads on this upgrade: its limit is raised once instead (#4602).
	routes.RaiseArtifactStoreLimitIfOver(ctx, store.NewSessionStore(db), cfg.CoralDir())
	savedBackend := ""
	if settings, err := store.NewSessionStore(db).GetSettings(ctx); err == nil {
		inputlog.SetSetting(setreg.Bool(settings, inputlog.SettingKey))
		agentprio.SetSetting(setreg.Raw(settings, agentprio.SettingKey))
		savedBackend = setreg.Raw(settings, routes.TerminalBackendSetting)
	}
	var backendForced, keptForAgents bool
	opts.BackendType, backendForced, keptForAgents = backendchoice.Resolve(backendFlag, savedBackend, runtime.GOOS, func() int {
		return len(tmux.PrivateAgentSessions(ctx, tmux.PrivateSocketPath(cfg.CoralDir())))
	})
	cfg.TerminalBackend, cfg.TerminalBackendForced = opts.BackendType, backendForced
	log.Printf("[STARTUP] terminal backend=%s (%s)", opts.BackendType, map[bool]string{true: "--backend", false: "Settings > Terminal backend, or the default"}[backendForced])
	if keptForAgents {
		log.Printf("[startup] Coral agents are still running in Coral's tmux server, so this start keeps tmux to leave them reachable; the first start without them runs the built-in terminal")
	}
	owedDefaultNotice(ctx, store.NewSessionStore(db), savedBackend, opts.BackendType, backendForced, runtime.GOOS)
	if opts.BackendType == "tmux" {
		if _, ok := tmux.IsAvailable(); !ok {
			log.Printf("[startup] tmux not found — agents cannot be launched until tmux is installed (%s)", tmux.InstallCommand())
		}
	}
	forcedPriority := opts.AgentPriority
	if forcedPriority == "" {
		forcedPriority = os.Getenv(agentprio.EnvVar)
	}
	agentprio.Setup(forcedPriority)
}

// applyPrivacySettings loads persisted privacy controls before binding. Remote
// (mobile/LAN) access is OFF by default: the server binds a non-loopback
// address only when the saved remote_access_enabled setting is explicitly
// "true". A missing, unreadable or any other value binds local-only, and the
// change is restart-applied. Telemetry is independently applied to the
// tracking package and never affects essential server behavior.
func applyPrivacySettings(ctx context.Context, db *store.DB, cfg *config.Config, forceRemote bool, remotePrompt func() bool) {
	// Fail closed for telemetry if settings cannot be read during startup.
	tracking.SetTelemetryEnabled(false)
	ss := store.NewSessionStore(db)
	settings, err := ss.GetSettings(ctx)
	if err != nil {
		if !forceRemote {
			// Fail closed for the bind too: never widen exposure because the
			// opt-in could not be read.
			restrictToLoopback(cfg, "settings unreadable")
		} else {
			widenForRemote(cfg)
		}
		return
	}
	// On first launch the setting doesn't exist yet — prompt the user, unless
	// --remote already answered: nothing is saved, so a later start without the
	// flag still asks.
	if configured := setreg.Has(settings, setreg.RemoteAccessEnabled); !configured && remotePrompt != nil && !forceRemote {
		enabled := remotePrompt()
		value := "false"
		if enabled {
			value = "true"
		}
		if err := ss.SetSetting(ctx, setreg.RemoteAccessEnabled, value); err != nil {
			log.Printf("[startup] failed to save remote_access_enabled: %v", err)
		}
		settings[setreg.RemoteAccessEnabled] = value
	} else if !configured && !forceRemote {
		log.Printf("[startup] remote access is off: Coral didn't ask, as nobody is at a terminal to answer (it accepts connections from this computer only). To let other devices connect, turn it on in Settings > Privacy, or start Coral with --remote")
	}
	if !forceRemote && !setreg.Bool(settings, setreg.RemoteAccessEnabled) {
		restrictToLoopback(cfg, "remote access is not enabled")
	} else {
		widenForRemote(cfg)
	}
	tracking.SetTelemetryEnabled(setreg.Bool(settings, setreg.TelemetryEnabled))
}

// applyHubModeSetting turns hub mode on when Settings > Run as a hub saved it
// (hub_mode, #3460). --hub and CORAL_HUB=1 already did (and win); nothing here
// turns it off. Unreadable settings leave it as the flags set it.
func applyHubModeSetting(ctx context.Context, db *store.DB, cfg *config.Config) {
	if cfg.HubMode {
		return
	}
	settings, err := store.NewSessionStore(db).GetSettings(ctx)
	if err != nil {
		return
	}
	if setreg.Bool(settings, routes.HubModeSetting) {
		cfg.HubMode = true
		log.Printf("[startup] hub mode is on (Settings > Run as a hub)")
	}
}

// applyCloudMaxMode saves the cap given at launch (--cloud-max-mode, CORAL_CLOUD_MAX_MODE) as the
// cloud_max_mode setting, so the connector, the dashboard and 'coral cloud status' read it as any
// other. It is the launch-time form of the local owner's choice, for a machine with nobody at its
// dashboard: the hub, a tunneled request and the API still cannot change it. Unset changes nothing.
// A value that is not view, interact or launch, or one that cannot be saved, stops Coral from
// starting: a cap that was asked for is never silently left lower, and never made higher. Lowering
// it ends what the hub turned on above the new cap (auto-accept needs Launch), as the dashboard does.
func applyCloudMaxMode(ctx context.Context, db *store.DB, cfg *config.Config) error {
	if cfg.CloudMaxMode == "" {
		return nil
	}
	mode, ok := tunnel.ParseMode(cfg.CloudMaxMode)
	if !ok {
		return fmt.Errorf("cloud max mode %q must be view, interact or launch", cfg.CloudMaxMode)
	}
	ss := store.NewSessionStore(db)
	if err := ss.SetSetting(ctx, routes.MaxModeSetting, mode.String()); err != nil {
		return fmt.Errorf("could not save the cloud max mode: %w", err)
	}
	if mode < tunnel.TierLaunch {
		if _, err := ss.ClearHubAutoAccept(ctx, store.AutoAcceptModeLowered); err != nil {
			return fmt.Errorf("could not turn off Coral Hub auto-accept: %w", err)
		}
	}
	log.Printf("[CLOUD] access cap set to %s by --cloud-max-mode / CORAL_CLOUD_MAX_MODE (applied at every start)", mode.String())
	return nil
}

// applyReplayBytesFromSettings reads the terminal_replay_bytes value from
// user_settings and applies it to the ptymanager replay buffer limit. Values
// outside [4096, 16 MiB] are clamped. Invalid or missing values leave the
// 256 KiB default in place.
func applyReplayBytesFromSettings(ctx context.Context, db *store.DB) {
	ss := store.NewSessionStore(db)
	settings, err := ss.GetSettings(ctx)
	if err != nil {
		return
	}
	n, state := setreg.IntState(settings, setreg.TerminalReplayBytes)
	switch state {
	case setreg.Unset:
		return
	case setreg.Invalid:
		log.Printf("[startup] ignoring invalid %s=%q", setreg.TerminalReplayBytes, settings[setreg.TerminalReplayBytes])
		return
	}
	ptymanager.SetReplayBytes(int(n))
	log.Printf("[startup] terminal_replay_bytes=%d", n)
}

// owedDefaultNotice decides, once, whether the dashboard owes the one line "Agents now stop when you quit Coral" (#6064): only an install that ran on the old tmux default, at its first start on the
// built-in terminal default (no --backend, no saved choice, not Windows, running pty). A new install is marked "none", so it never sees it. A start that kept tmux for running agents decides nothing:
// the question is asked again at the first start that runs the built-in terminal. The decision is written once and never changed by Coral.
func owedDefaultNotice(ctx context.Context, ss *store.SessionStore, saved, backend string, forced bool, goos string) {
	if forced || goos == "windows" || saved == "tmux" || saved == "pty" || backend != "pty" {
		return
	}
	settings, err := ss.GetSettings(ctx)
	if err != nil || setreg.Raw(settings, setreg.TerminalDefaultNotice) != "" {
		return
	}
	used, err := ss.HasRunAgents(ctx)
	if err != nil {
		return
	}
	value := "none"
	if used {
		value = "pending"
	}
	if err := ss.SetSetting(ctx, setreg.TerminalDefaultNotice, value); err != nil {
		log.Printf("[startup] could not record the terminal default notice: %v", err)
		return
	}
	log.Printf("[startup] built-in terminal is now the default; notice=%s", value)
}
