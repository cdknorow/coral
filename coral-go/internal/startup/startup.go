// Package startup provides shared server bootstrap logic used by all Coral
// entry points (coral, coral-tray, launch-coral). It handles database setup,
// terminal backend selection, HTTP server creation, and background service
// wiring so that each cmd/ binary doesn't have to duplicate this code.
package startup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/cdknorow/coral/internal/agentprio"
	"github.com/cdknorow/coral/internal/gitutil"
	"github.com/cdknorow/coral/internal/safego"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/cdknorow/coral/internal/agent"
	"github.com/cdknorow/coral/internal/agentenv"
	"github.com/cdknorow/coral/internal/auth"
	"github.com/cdknorow/coral/internal/background"
	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/cloud"
	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/dbcrypt"
	"github.com/cdknorow/coral/internal/inputlog"
	"github.com/cdknorow/coral/internal/naming"
	"github.com/cdknorow/coral/internal/proxy"
	"github.com/cdknorow/coral/internal/ptymanager"
	"github.com/cdknorow/coral/internal/secrets"
	"github.com/cdknorow/coral/internal/server"
	"github.com/cdknorow/coral/internal/server/routes"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/tmux"
	"github.com/cdknorow/coral/internal/tracking"
	"github.com/cdknorow/coral/pkg/tunnel"
)

const devStartupMarker = "DEV-TMUX-DIAGNOSTIC-2026-06-03-01"

// Options configures optional behaviors that differ between entry points.
type Options struct {
	// Restart, when set, restarts this process (the desktop apps' tray, #3460):
	// Settings > Run as a hub offers a Restart button then. It returns once the
	// restart is under way; the server shuts down shortly after.
	Restart func() error

	// BackendType is "pty" or "tmux" from --backend, which wins over the saved Settings >
	// Terminal backend (#3634); "" means not given: the setting, else tmux (pty on Windows).
	BackendType string

	// OnServerError is called when ListenAndServe fails (non-ErrServerClosed).
	// If nil, log.Printf is used.
	OnServerError func(err error)

	// PasswordPrompt is called only when bootstrap selects password mode. The
	// callback must read securely and must not return or log the password.
	PasswordPrompt func() (string, error)

	// UnlockSurface describes the entry point's available secure prompt.
	// Empty means headless/no prompt.
	UnlockSurface string

	// RemoteAccess, when true, skips the loopback restriction so the server
	// binds the configured host even without a persisted remote_access_enabled
	// setting. Equivalent to passing --remote on the command line.
	RemoteAccess bool

	// RemoteAccessPrompt, when set, is called on first launch (before the
	// remote_access_enabled setting exists) to ask the user whether to allow
	// remote connections. It returns true to enable remote access.
	RemoteAccessPrompt func() bool
	// DebugInput forces the terminal input debug log on (--debug-input), whatever
	// the setting says (#3256).
	DebugInput bool

	// AgentPriority overrides the agent_priority setting (--agent-priority or
	// CORAL_AGENT_PRIORITY: low or normal, #3437); "" keeps the setting.
	AgentPriority string

	// TLSCert and TLSKey are a PEM certificate and key file (--tls-cert/--tls-key,
	// CORAL_TLS_CERT/CORAL_TLS_KEY, #3355). With both, the port also serves HTTPS and
	// accepts only HTTPS from other computers. Only one, or a pair that does not load,
	// stops startup.
	TLSCert, TLSKey string
	// StrictLocalAuth overrides strict local auth (--strict-local-auth, #3418). nil: on unless
	// CORAL_STRICT_LOCAL_AUTH says off (#3936), the same for coral, coral-tray and launch-coral.
	StrictLocalAuth *bool
}

// RunningServer holds all resources created during startup.
// Callers use it to access the HTTP server for shutdown and the backend for cleanup.
type RunningServer struct {
	HTTPServer *http.Server
	Server     *server.Server
	DB         *store.DB
	Backend    ptymanager.TerminalBackend
	// LoginToken is the startup dashboard sign-in token, also in <data>/login_token
	// (#3387): single-use, 10 minutes. "" if it could not be minted.
	LoginToken string
	instance   *instanceLock
	lockPath   string
	// workflows runs workflow steps; Shutdown stops what is in progress (#3997).
	workflows *background.WorkflowRunner
}

// NewLoginLink returns a fresh single-use dashboard sign-in link on this computer
// (http://localhost:<port>/?login_token=…, 10 minutes) (#3387). Apps that run Coral
// in-process (launch-coral, coral-tray, and coral-app through the tray's --url) open
// this instead of the bare dashboard URL, so they sign in with no API key and no prompt.
func (rs *RunningServer) NewLoginLink(port int) (string, error) {
	token, err := rs.Server.MintLoginToken()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("http://localhost:%d/?login_token=%s", port, token), nil
}

// Shutdown gracefully shuts down the HTTP server and cleans up resources.
// For PTY backends, all live sessions are marked as sleeping before the
// backend is closed, since PTY child processes cannot survive the server
// exiting. On restart, reconcileOrphanedSessions detects these and the
// user can wake them from the UI.
func (rs *RunningServer) Shutdown(timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := rs.HTTPServer.Shutdown(ctx); err != nil {
		log.Printf("Shutdown error: %v", err)
	}
	// A workflow's shell steps run in their own process group, so they don't stop with the
	// server by themselves: end them here (#3997).
	if rs.workflows != nil {
		rs.workflows.Shutdown(ctx)
	}
	if rs.Server != nil {
		rs.Server.CloseCloud()       // no command-line token outlives the server (#3285)
		rs.Server.RemoveLoginToken() // nor the sign-in token's file (#3628 F7)
	}
	// Mark all PTY sessions as sleeping before killing them so they can
	// be re-launched on next startup.
	if _, isPTY := rs.Backend.(*ptymanager.PTYBackend); isPTY && rs.DB != nil {
		ss := store.NewSessionStore(rs.DB)
		liveSessions, err := ss.GetAllLiveSessions(ctx)
		if err == nil {
			sleeping := 0
			for _, ls := range liveSessions {
				if ls.IsSleeping == 0 {
					// Running when Coral stopped: the next start may wake it again (#3636).
					if err := ss.SetSessionSleepingFor(ctx, ls.SessionID, store.SleepReasonCoralStopped); err == nil {
						sleeping++
					}
				}
			}
			if sleeping > 0 {
				log.Printf("[shutdown] marked %d PTY session(s) as sleeping", sleeping)
			}
		}
	}
	if rs.Backend != nil {
		rs.Backend.Close()
	}
}

// Close stops the server's background work and closes its databases (the board's, then the
// sessions database) and the instance lock. Call it after Shutdown.
func (rs *RunningServer) Close() {
	if rs.Server != nil {
		rs.Server.Close() // its loops and the board database, before the sessions database (#3914)
	}
	if rs.DB != nil {
		rs.DB.Close()
	}
	if rs.instance != nil {
		rs.instance.close(rs.lockPath)
		rs.instance = nil
	}
}

// Start opens the database, selects the terminal backend, creates the HTTP
// server, wires up all background services, and starts listening. The HTTP
// server runs in a background goroutine; callers should wait on ctx.Done()
// then call RunningServer.Shutdown().
func Start(ctx context.Context, cfg *config.Config, opts Options) (*RunningServer, error) {
	// The secret detector (#3808): coral, the tray and launch-coral all serve Coral Hub and
	// write coral.log from this process, so none of them runs without it.
	if err := secrets.Init(); err != nil {
		return nil, fmt.Errorf("the secret detector could not be built: %w", err)
	}
	dbcrypt.SetUnlockSurface(opts.UnlockSurface)
	backendFlag := opts.BackendType // resolved against the saved setting once the database is open
	exe, exeErr := os.Executable()
	log.Printf("[STARTUP] %s version=%q tier=%s backend=%s pid=%d exe=%q exe_err=%v path=%q shell=%q coral_tmux_bin=%q",
		devStartupMarker,
		config.ReportedVersion(),
		config.TierName,
		opts.BackendType,
		os.Getpid(),
		exe,
		exeErr,
		os.Getenv("PATH"),
		os.Getenv("SHELL"),
		os.Getenv("CORAL_TMUX_BIN"),
	)

	// Ensure ~/.coral directory exists before any file operations
	coralDir := cfg.CoralDir()
	// A standard (non-sqlcipher) build cannot use database encryption. Refuse a
	// requested or existing encrypted setup before anything is created, saved,
	// locked or opened, so a rejected start leaves the Coral home untouched.
	if err := dbcrypt.RejectUnsupportedEncryption(coralDir, cfg.DBPath, filepath.Join(coralDir, "messageboard.db")); err != nil {
		return nil, err
	}
	if err := config.EnsureDataDir(coralDir); err != nil { // this user only (#3637)
		return nil, fmt.Errorf("failed to create data directory %s: %w", coralDir, err)
	}

	// Ensure DB parent directory exists (may differ from coralDir if overridden)
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create database directory: %w", err)
	}
	lockPath := filepath.Join(coralDir, "coral-server.lock")
	instance, err := acquireInstanceLock(lockPath)
	if err != nil {
		return nil, fmt.Errorf("cannot start Coral: %w (stop the existing server or use a different data directory)", err)
	}
	releaseOnError := true
	defer func() {
		if releaseOnError {
			instance.close(lockPath)
		}
	}()

	// Resolve encryption before opening either database. The board database is
	// separate and must use the same mode/key. No listener or service has been
	// started at this point, so an unlock failure cannot expose partial state.
	security, err := dbcrypt.Load(coralDir)
	if err != nil {
		return nil, err
	}
	// Missing security.json is the backwards-compatible plaintext setup.
	// Normalize the empty bootstrap value before validating explicit modes.
	if security.DatabaseEncryption == "" {
		security.DatabaseEncryption = "disabled"
	}
	if envMode := strings.TrimSpace(os.Getenv("CORAL_DB_ENCRYPTION")); envMode != "" {
		if _, statErr := os.Stat(filepath.Join(coralDir, dbcrypt.BootstrapName)); os.IsNotExist(statErr) {
			security.DatabaseEncryption = envMode
			if err := dbcrypt.Save(coralDir, security); err != nil {
				return nil, fmt.Errorf("save database encryption setting: %w", err)
			}
		}
	}
	if security.DatabaseEncryption != "disabled" && security.DatabaseEncryption != "key_file" && security.DatabaseEncryption != "password" {
		return nil, fmt.Errorf("unsupported database encryption mode %q", security.DatabaseEncryption)
	}
	boardPath := filepath.Join(coralDir, "messageboard.db")
	if err := dbcrypt.CheckMigrationRecovery([]string{cfg.DBPath, boardPath}); err != nil {
		return nil, err
	}
	migrateRequested := strings.EqualFold(strings.TrimSpace(os.Getenv("CORAL_DB_ENCRYPTION_MIGRATE")), "1") || strings.EqualFold(strings.TrimSpace(os.Getenv("CORAL_DB_ENCRYPTION_MIGRATE")), "true")
	var dbKey string
	if migrateRequested && security.DatabaseEncryption == "key_file" {
		keyPath := security.KeyFile
		if keyPath == "" {
			keyPath = dbcrypt.KeyName
		}
		if !filepath.IsAbs(keyPath) {
			keyPath = filepath.Join(coralDir, keyPath)
		}
		if _, keyErr := os.Stat(keyPath); os.IsNotExist(keyErr) {
			for _, path := range []string{cfg.DBPath, boardPath} {
				encrypted, probeErr := dbcrypt.EncryptedFile(path)
				if probeErr != nil {
					return nil, probeErr
				}
				if encrypted {
					return nil, fmt.Errorf("database key file is missing for encrypted database %s; refusing replacement", filepath.Base(path))
				}
			}
		}
		dbKey, err = dbcrypt.GenerateKeyFile(coralDir, security)
	} else {
		dbKey, err = dbcrypt.ResolveKey(coralDir, security, cfg.DBPath, boardPath)
	}
	if err != nil && !errors.Is(err, dbcrypt.ErrUnlockRequired) {
		return nil, err
	}
	if errors.Is(err, dbcrypt.ErrUnlockRequired) {
		if opts.PasswordPrompt == nil {
			return nil, fmt.Errorf("database encryption requires an interactive password prompt")
		}
		dbKey, err = opts.PasswordPrompt()
		if err != nil {
			return nil, fmt.Errorf("database unlock cancelled: %w", err)
		}
		if strings.TrimSpace(dbKey) == "" {
			return nil, fmt.Errorf("database unlock password is empty")
		}
	}
	if dbKey != "" && security.DatabaseEncryption != "disabled" {
		if security.DatabaseEncryption == "password" && security.KeySalt == "" {
			salt := make([]byte, 16)
			if _, saltErr := rand.Read(salt); saltErr != nil {
				return nil, fmt.Errorf("generate password salt: %w", saltErr)
			}
			security.KeySalt = hex.EncodeToString(salt)
			if saveErr := dbcrypt.Save(coralDir, security); saveErr != nil {
				return nil, fmt.Errorf("save password salt: %w", saveErr)
			}
		}
		dbKey, err = dbcrypt.PrepareStorageKey(dbKey, security.DatabaseEncryption, security.KeySalt)
		if err != nil {
			return nil, err
		}
	}
	if migrateRequested && dbKey != "" {
		if err := dbcrypt.MigratePlaintexts([]string{cfg.DBPath, boardPath}, dbKey); err != nil {
			return nil, fmt.Errorf("database migration failed: %w", err)
		}
	}

	// Open database
	db, err := store.OpenWithKey(ctx, cfg.DBPath, dbKey)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}
	dbcrypt.SetEffectiveMode(security.DatabaseEncryption)
	// Probe the separate board database before constructing the server. A
	// wrong key must fail closed rather than starting with only one DB open.
	boardProbe, err := board.NewStoreWithKey(boardPath, dbKey)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to open board database: %w", err)
	}
	boardProbe.Close()
	applyPrivacySettings(ctx, db, cfg, opts.RemoteAccess, opts.RemoteAccessPrompt)
	applyHubModeSetting(ctx, db, cfg)
	if err := applyCloudMaxMode(ctx, db, cfg); err != nil {
		db.Close()
		return nil, err
	}
	// Per-session agent tokens, before anything can launch an agent (#3387).
	setupSessionTokens(db, cfg.CoralDir())
	setupAgentTools(cfg.CoralDir())

	// Native TLS (#3355): load the pair before binding, so a bad pair stops startup.
	tlsConfig, err := loadTLSConfig(opts.TLSCert, opts.TLSKey)
	if err != nil {
		db.Close()
		return nil, err
	}
	cfg.TLS = tlsConfig != nil
	cfg.StrictLocalAuth = strictLocalAuth(cfg.CoralDir(), opts.StrictLocalAuth)

	// Bind the port now and keep the listeners open to avoid a TOCTOU race
	// (another process grabbing the port between check and serve). One listener per --host
	// address, all on one port, plus 127.0.0.1 when the list names only network addresses
	// (#3594).
	lns, boundPort, err := listenAll(cfg)
	if err != nil {
		db.Close()
		return nil, err
	}
	// The DNS-rebinding check accepts exactly the names Coral listens on (#3594).
	auth.SetListenHosts(cfg.ListenHosts())
	if tlsConfig != nil {
		for i := range lns {
			lns[i] = newSniffListener(lns[i], tlsConfig)
		}
	}
	// Everything this server starts (the tmux server, agents, workflow steps) inherits this
	// process's environment: make it say THIS server, never whatever started it (#3421), and
	// always its loopback listener when it has one (#3594).
	agentenv.UseServerEnv(cfg.AgentHost(), boundPort, cfg.CoralDir())

	// Apply terminal_replay_bytes setting from user_settings before any
	// backend touches the replay buffer size.
	applyReplayBytesFromSettings(ctx, db)

	// The terminal input debug log (#3256): off unless the setting, CORAL_DEBUG_INPUT
	// or --debug-input turns it on.
	inputlog.Setup(cfg.CoralDir(), opts.DebugInput || inputlog.EnabledFromEnv())
	savedBackend := ""
	if settings, err := store.NewSessionStore(db).GetSettings(ctx); err == nil {
		inputlog.SetSetting(config.Truthy(settings[inputlog.SettingKey]))
		agentprio.SetSetting(settings[agentprio.SettingKey])
		savedBackend = settings[routes.TerminalBackendSetting]
	}
	// The terminal backend (#3634): --backend, else the saved setting, else the OS default.
	var backendForced bool
	opts.BackendType, backendForced = ResolveBackend(backendFlag, savedBackend, runtime.GOOS)
	cfg.TerminalBackend, cfg.TerminalBackendForced = opts.BackendType, backendForced
	log.Printf("[STARTUP] terminal backend=%s (%s)", opts.BackendType, map[bool]string{true: "--backend", false: "Settings > Terminal backend, or the default"}[backendForced])
	// Check if tmux is available when using tmux backend. We don't fail
	// startup if it's missing — the dashboard still loads and the UI shows
	// install instructions. Agent launch will fail until tmux is installed.
	if opts.BackendType == "tmux" {
		if _, ok := tmux.IsAvailable(); !ok {
			log.Printf("[startup] tmux not found — agents cannot be launched until tmux is installed (brew install tmux)")
		}
	}
	// Agents run at a lower OS priority than this server (#3437): the setting,
	// or --agent-priority / CORAL_AGENT_PRIORITY.
	forcedPriority := opts.AgentPriority
	if forcedPriority == "" {
		forcedPriority = os.Getenv(agentprio.EnvVar)
	}
	agentprio.Setup(forcedPriority)

	// Select terminal backend and agent runtime
	prepareLogDir(cfg.LogDir)
	naming.SetDefaultLogDir(cfg.LogDir)
	backend, agentRT, terminal := selectBackend(opts.BackendType, cfg.LogDir, cfg.CoralDir())

	// Build the HTTP server
	srv := server.NewWithKey(cfg, db, backend, terminal, dbKey)
	srv.SetRestarter(opts.Restart)
	loginToken, lerr := srv.WriteLoginToken()
	if lerr != nil {
		log.Printf("[identity] could not write the dashboard sign-in token: %v", lerr)
	}

	// Reconcile orphaned live sessions: if the app was killed without
	// cleanly sleeping sessions, they remain in live_sessions with
	// is_sleeping=0 but no actual process running. Detect these and
	// mark them as sleeping so the user can wake them. Also wakes
	// sessions that are marked sleeping but still running (e.g. tmux
	// survived a server restart).
	_, ptyRun := backend.(*ptymanager.PTYBackend)
	if ptyRun {
		cfg.TerminalBackend = "pty" // also when startup fell back to pty (a long socket path)
	}
	reconcileOrphanedSessions(ctx, db, agentRT, ptyRun)
	reconcileOrphanedTeams(ctx, db)

	// Restore board pause state AFTER reconciliation so it reflects
	// the final sleeping state (including recovered sessions).
	srv.RestoreSleepingBoards()

	var handler http.Handler = srv.Router()
	if tlsConfig != nil {
		handler = requireTLSFromNetwork(handler)
	}
	httpServer := &http.Server{
		Handler:           handler,
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 10 * time.Second, // Prevents slowloris attacks
		WriteTimeout:      0,                // Disabled for WebSocket/SSE
		IdleTimeout:       60 * time.Second,
	}

	// Start all background services
	workflows := startBackgroundServices(ctx, db, cfg, srv, agentRT)
	startPprof(ctx)
	wd := startStallWatch(ctx, cfg.CoralDir(), srv.Router(), db, srv.BoardStore())
	srv.StartMachineHealth(ctx, wd)

	// Start HTTP server in background goroutine using the already-bound listener
	onErr := opts.OnServerError
	if onErr == nil {
		onErr = func(err error) { log.Printf("[FATAL] Server error: %v", err) }
	}
	go func() {
		defer safego.Recover("startup/startup:464")
		if tlsConfig != nil {
			log.Printf("Coral dashboard: https://localhost:%d (TLS; plain http:// only from this computer)", cfg.Port)
		} else {
			log.Printf("Coral dashboard: http://localhost:%d", cfg.Port)
		}
		if w := remoteAccessWarning(cfg.Host, cfg.TLS); w != "" {
			log.Print(w)
		}
		for _, ln := range lns[1:] {
			go func(ln net.Listener) {
				defer safego.Recover("startup/startup:474")
				if err := httpServer.Serve(ln); err != nil && err != http.ErrServerClosed {
					onErr(err)
				}
			}(ln)
		}
		if err := httpServer.Serve(lns[0]); err != nil && err != http.ErrServerClosed {
			onErr(err)
		}
	}()

	releaseOnError = false
	return &RunningServer{
		workflows:  workflows,
		HTTPServer: httpServer,
		Server:     srv,
		LoginToken: loginToken,
		DB:         db,
		Backend:    backend,
		instance:   instance,
		lockPath:   lockPath,
	}, nil
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
		}
		return
	}
	// On first launch the setting doesn't exist yet — prompt the user, unless
	// --remote already answered: nothing is saved, so a later start without the
	// flag still asks.
	if _, configured := settings["remote_access_enabled"]; !configured && remotePrompt != nil && !forceRemote {
		enabled := remotePrompt()
		value := "false"
		if enabled {
			value = "true"
		}
		if err := ss.SetSetting(ctx, "remote_access_enabled", value); err != nil {
			log.Printf("[startup] failed to save remote_access_enabled: %v", err)
		}
		settings["remote_access_enabled"] = value
	} else if !configured && !forceRemote {
		log.Printf("[startup] remote access is off: Coral didn't ask, as nobody is at a terminal to answer (it accepts connections from this computer only). To let other devices connect, turn it on in Settings > Privacy, or start Coral with --remote")
	}
	if !forceRemote && !strings.EqualFold(strings.TrimSpace(settings["remote_access_enabled"]), "true") {
		restrictToLoopback(cfg, "remote access is not enabled")
	}
	tracking.SetTelemetryEnabled(!strings.EqualFold(strings.TrimSpace(settings["telemetry_enabled"]), "false"))
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
	if strings.EqualFold(strings.TrimSpace(settings[routes.HubModeSetting]), "true") {
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

// listenAll opens one listener per address in cfg.ListenHosts(), all on one port: the first
// binds cfg.Port (0 picks a free one) and the rest bind the port it got. The automatic
// 127.0.0.1 listener is logged once. On any failure every listener opened so far is closed.
func listenAll(cfg *config.Config) ([]net.Listener, int, error) {
	if err := config.HostsError(cfg.Host); err != nil {
		return nil, 0, err
	}
	hosts := cfg.ListenHosts()
	port := cfg.Port
	var lns []net.Listener
	for _, h := range hosts {
		ln, err := net.Listen("tcp", net.JoinHostPort(strings.Trim(h, "[]"), strconv.Itoa(port)))
		if err != nil {
			for _, open := range lns {
				open.Close()
			}
			return nil, 0, fmt.Errorf("can't listen on %s port %d (in use?): %w", displayHost(h), port, err)
		}
		if port == 0 {
			if tcp, ok := ln.Addr().(*net.TCPAddr); ok {
				port = tcp.Port
			}
		}
		lns = append(lns, ln)
	}
	if cfg.AutoLoopback() {
		log.Printf("[startup] --host %q names only network addresses: also listening on 127.0.0.1:%d for this computer's agents, command line and dashboard (--no-auto-loopback turns it off)", cfg.Host, port)
	} else if cfg.NoAutoLoopback && !config.IsLoopbackHost(cfg.AgentHost()) {
		log.Printf("[startup] WARNING: --no-auto-loopback with only network addresses: agents started here reach Coral at %s, where only the API key or a dashboard sign-in is accepted, so their board and task calls will be refused. Leave the loopback listener on (and use --strict-local-auth on a shared computer) if Coral runs agents here.", cfg.AgentHost())
	}
	if len(hosts) > 1 {
		shown := make([]string, len(hosts))
		for i, h := range hosts {
			shown[i] = displayHost(h)
		}
		log.Printf("[startup] listening on %s, port %d", strings.Join(shown, ", "), port)
	}
	return lns, port, nil
}

func displayHost(h string) string {
	if config.IsWildcardHost(h) {
		return "every interface"
	}
	return h
}

// restrictToLoopback forces a local-only bind. When the requested host would
// have been reachable from the network it logs how to opt in, so an operator
// who passed --host or CORAL_HOST is told why the bind is local. A --host list
// that is already loopback only (127.0.0.1,::1) is kept as given (#3594, QA's
// observation): it was being collapsed to 127.0.0.1 without a word.
func restrictToLoopback(cfg *config.Config, reason string) {
	if config.IsLoopbackHost(cfg.Host) {
		return
	}
	log.Printf("[startup] remote access is off (%s): binding 127.0.0.1 instead of %q. Enable it in Settings > Privacy (remote_access_enabled=true) and restart Coral to accept network connections.", reason, cfg.Host)
	cfg.Host = "127.0.0.1"
}

// reconcileOrphanedSessions checks all non-sleeping live sessions against
// the runtime to see if their processes are actually running. Any session
// that exists in the DB but has no running process is marked as sleeping
// so the user can wake it from the UI.
// ptyRun: on pty, an awake row with no running agent was running when Coral last stopped
// without its shutdown (a crash), so it is marked coral_stopped for the auto-wake (#3636).
func reconcileOrphanedSessions(ctx context.Context, db *store.DB, agentRT background.AgentRuntime, ptyRun bool) {
	ss := store.NewSessionStore(db)

	liveSessions, err := ss.GetAllLiveSessions(ctx)
	if err != nil {
		log.Printf("[startup] failed to read live sessions for reconciliation: %v", err)
		return
	}

	// Ask the runtime which sessions are actually running
	agents, err := agentRT.ListAgents(ctx)
	if err != nil {
		log.Printf("[startup] failed to list running agents for reconciliation: %v", err)
		return
	}
	alive := make(map[string]bool, len(agents))
	for _, a := range agents {
		alive[a.SessionID] = true
	}

	orphaned := 0
	recovered := 0
	for _, ls := range liveSessions {
		if ls.IsSleeping == 1 {
			// Check if a sleeping session is actually alive (e.g. tmux still running
			// after a server restart). If so, wake it.
			if alive[ls.SessionID] {
				if err := ss.SetSessionSleeping(ctx, ls.SessionID, false); err != nil {
					log.Printf("[startup] failed to wake recovered session %s: %v", ls.SessionID[:8], err)
					continue
				}
				recovered++
			}
			continue
		}
		if alive[ls.SessionID] {
			continue
		}
		slog.Warn("detected crashed agent, marking as sleeping", "session_id", ls.SessionID, "agent_name", ls.AgentName)
		reason := ""
		if ptyRun {
			reason = store.SleepReasonCoralStopped
		}
		if err := ss.SetSessionSleepingFor(ctx, ls.SessionID, reason); err != nil {
			log.Printf("[startup] failed to mark orphaned session %s as sleeping: %v", ls.SessionID[:8], err)
			continue
		}
		orphaned++
	}
	if orphaned > 0 {
		log.Printf("[startup] Marked %d orphaned session(s) as sleeping", orphaned)
	}
	if recovered > 0 {
		log.Printf("[startup] Recovered %d sleeping session(s) that are still alive", recovered)
	}
}

// reconcileOrphanedTeams checks for teams in 'running' status whose
// member sessions no longer exist. These are teams orphaned by a server
// crash. They are marked as stopped and any worktree is cleaned up.
func reconcileOrphanedTeams(ctx context.Context, db *store.DB) {
	ts := store.NewTeamStore(db)
	ss := store.NewSessionStore(db)

	teams, err := ts.ListTeams(ctx, "running")
	if err != nil {
		log.Printf("[startup] failed to list running teams for reconciliation: %v", err)
		return
	}

	liveSessions, err := ss.GetAllLiveSessions(ctx)
	if err != nil {
		log.Printf("[startup] failed to read live sessions for team reconciliation: %v", err)
		return
	}
	liveSet := make(map[string]bool, len(liveSessions))
	for _, ls := range liveSessions {
		liveSet[ls.SessionID] = true
	}

	orphaned := 0
	for _, team := range teams {
		members, err := ts.GetActiveMembers(ctx, team.ID)
		if err != nil {
			log.Printf("[startup] failed to get members for team %s: %v", team.Name, err)
			continue
		}

		// Check if any member still has a live session
		hasLive := false
		for _, m := range members {
			if m.SessionID != nil && liveSet[*m.SessionID] {
				hasLive = true
				break
			}
		}
		if hasLive {
			continue
		}

		slog.Warn("detected orphaned team, marking as stopped", "team", team.Name, "id", team.ID)

		// Mark members and team as stopped
		if err := ts.SetAllMembersStatus(ctx, team.ID, "active", "stopped"); err != nil {
			log.Printf("[startup] failed to stop members of team %s: %v", team.Name, err)
		}
		if err := ts.UpdateTeamStatus(ctx, team.ID, "stopped"); err != nil {
			log.Printf("[startup] failed to stop team %s: %v", team.Name, err)
		}

		// Clean up the team's worktree, but never its work (#3595): an orphaned team is usually
		// one whose server stopped mid-work, so its worktree may hold uncommitted changes. Git
		// removes it only when it's clean (no --force), and when git can't say (a timeout, git
		// missing, a broken .git file), the folder is left where it is and logged.
		if team.IsWorktree == 1 && team.WorkingDir != "" {
			cleanCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			repoOut, repoErr := gitutil.Command(cleanCtx, team.WorkingDir, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
			if repoErr == nil {
				repoPath := filepath.Dir(strings.TrimSpace(string(repoOut)))
				cmd := gitutil.Command(cleanCtx, repoPath, "worktree", "remove", team.WorkingDir)
				if out, err := cmd.CombinedOutput(); err != nil {
					log.Printf("[startup] left the orphaned worktree %s in place (git won't remove it, e.g. it has uncommitted work): %s", team.WorkingDir, strings.TrimSpace(string(out)))
				} else {
					log.Printf("[startup] cleaned up orphaned worktree: %s", team.WorkingDir)
				}
			} else {
				log.Printf("[startup] left the orphaned worktree %s in place: git can't find its repository (%v)", team.WorkingDir, repoErr)
			}
			cancel()
		}

		orphaned++
	}
	if orphaned > 0 {
		log.Printf("[startup] Marked %d orphaned team(s) as stopped", orphaned)
	}
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
	raw, ok := settings["terminal_replay_bytes"]
	if !ok || raw == "" {
		return
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		log.Printf("[startup] ignoring invalid terminal_replay_bytes=%q: %v", raw, err)
		return
	}
	const (
		minBytes = 4 * 1024
		maxBytes = 16 * 1024 * 1024
	)
	if n < minBytes {
		n = minBytes
	}
	if n > maxBytes {
		n = maxBytes
	}
	ptymanager.SetReplayBytes(n)
	log.Printf("[startup] terminal_replay_bytes=%d", n)
}

func selectBackend(backendType, logDir, coralDir string) (ptymanager.TerminalBackend, background.AgentRuntime, ptymanager.SessionTerminal) {
	// A tmux socket path over the platform's sun_path limit cannot be bound.
	// Previously that produced a bare "File name too long" from tmux while the
	// server went on logging that it was using the tmux backend, so the operator
	// was told one thing and got another. Say it plainly and pick PTY on purpose.
	if backendType != "pty" && coralDir != "" {
		if err := tmux.CheckSocketPath(coralDir); err != nil {
			log.Printf("[startup] tmux backend unavailable: %v", err)
			log.Printf("[startup] using a shorter --home / CORAL_DATA_DIR would restore the tmux backend")
			backendType = "pty"
		}
	}
	if backendType == "pty" {
		ptyBackend := ptymanager.NewPTYBackend()
		log.Println("Using native PTY terminal backend")
		return ptyBackend, background.NewPTYRuntime(ptyBackend), ptymanager.NewPTYSessionTerminal(ptyBackend)
	}
	tmuxClient := tmux.NewClient(coralDir)
	// Panes a tmux server from before #3437 started at normal priority: lower
	// them (and their children) once, on our own socket only.
	if n := tmuxClient.LowerExistingPanes(context.Background()); n > 0 {
		log.Printf("Lowered the priority of %d existing agent processes (agent_priority=%s)", n, agentprio.Level())
	}
	// How many agents only the default-socket merge finds (#3635; it goes off by default next release).
	tmuxClient.LogDefaultSocketMerges(context.Background())
	tmuxBackend := ptymanager.NewTmuxBackend(tmuxClient, logDir)
	log.Println("Using tmux terminal backend")
	return tmuxBackend, background.NewTmuxRuntime(tmuxClient), ptymanager.NewTmuxSessionTerminal(tmuxClient)
}

// safeGo runs fn in a goroutine with panic recovery. If fn panics, the panic
// and stack trace are logged, and fn is restarted after a short delay. This
// prevents a single background service crash from taking down the entire
// server process. When the context is cancelled (normal shutdown), the
// goroutine exits without restarting.
// gitPollInterval resolves the configured git poll cadence (see
// background.GitPollInterval); "0" means the user has turned polling off and
// wants to refresh the file list by hand.
func gitPollInterval(ctx context.Context, ss *store.SessionStore, defaultSeconds int) time.Duration {
	settings, err := ss.GetSettings(ctx)
	if err != nil {
		return time.Duration(defaultSeconds) * time.Second
	}
	return background.GitPollInterval(settings, defaultSeconds)
}

func safeGo(ctx context.Context, name string, fn func()) {
	// A background service restarts after a panic, with a backoff (#3998: the one helper now).
	safego.Loop(ctx, name, fn)
}

func startBackgroundServices(ctx context.Context, db *store.DB, cfg *config.Config, srv *server.Server, agentRT background.AgentRuntime) *background.WorkflowRunner {
	gitStore := store.NewGitStore(db)
	taskStore := store.NewTaskStore(db)
	sessStore := store.NewSessionStore(db)
	schedStore := store.NewScheduleStore(db)
	rbStore := store.NewRemoteBoardStore(db)

	// Shared agent discovery function — enriches runtime agents with display names from the DB.
	discoverFn := func(ctx context.Context) ([]background.AgentInfo, error) {
		agents, err := agentRT.ListAgents(ctx)
		if err != nil {
			return nil, err
		}
		// Look up display names from live_sessions for stable board
		// subscriber_id, in one query rather than one per agent.
		ids := make([]string, 0, len(agents))
		for _, a := range agents {
			if a.SessionID != "" {
				ids = append(ids, a.SessionID)
			}
		}
		if names, err := sessStore.GetLiveDisplayNames(ctx, ids); err == nil {
			for i, a := range agents {
				if name, ok := names[a.SessionID]; ok {
					agents[i].DisplayName = name
				}
			}
		}
		return agents, nil
	}

	// Coral Hub connector. Release builds offer https://app.coralhub.dev unless --cloud-url /
	// CORAL_CLOUD_URL say otherwise (off disables it). Nothing is sent to the hub until the
	// user connects; when off it opens no outbound connection and starts no goroutine.
	startCloudConnector(ctx, db, cfg, srv, sessStore)

	// Multi-server hub: remote agent poller and feed relay. Hub mode only;
	// a standalone server has nothing to poll.
	if srv.HasRemoteAgents() {
		safeGo(ctx, "remote_agents", func() { srv.RunRemoteAgents(ctx) })
	}

	// Git poller. Its cadence is a user setting, re-read before every wait, so
	// someone on a repository where scanning is slow can stretch it out or
	// turn it off without restarting.
	gitPoller := background.NewGitPoller(gitStore, agentRT, time.Duration(cfg.GitPollerIntervalS)*time.Second)
	gitPoller.SetIntervalFn(func() time.Duration {
		return gitPollInterval(ctx, sessStore, cfg.GitPollerIntervalS)
	})
	safeGo(ctx, "git_poller", func() { gitPoller.Run(ctx) })

	// Session indexer
	scanners := []agent.HistoryScanner{
		&agent.ClaudeAgent{},
		&agent.AgyAgent{},
		&agent.CodexAgent{},
	}
	indexer := background.NewSessionIndexer(
		sessStore, scanners,
		time.Duration(cfg.IndexerIntervalS)*time.Second,
		time.Duration(cfg.IndexerStartupDelayS)*time.Second,
	)
	safeGo(ctx, "session_indexer", func() { indexer.Run(ctx) })
	srv.SetIndexer(indexer)

	// Job scheduler
	scheduler := background.NewJobScheduler(schedStore, 30*time.Second)
	launcher := background.NewAgentLauncher(agentRT, sessStore, cfg.Port)
	scheduler.SetLaunchFn(launcher.BuildSchedulerLaunchFn(schedStore))
	scheduler.SetSessionStore(sessStore)
	scheduler.SetRuntime(agentRT)
	scheduler.SetNextFireTimeFn(background.NextFireTime)
	scheduler.SetWorktreeRoot(filepath.Join(cfg.CoralDir(), "worktrees"))
	safeGo(ctx, "job_scheduler", func() { scheduler.Run(ctx) })
	srv.SetScheduler(scheduler)

	// Board notifier
	boardNotifier := background.NewBoardNotifier(srv.BoardStore(), agentRT, time.Duration(cfg.BoardNotifierIntervalS)*time.Second)
	boardNotifier.SetDiscoverFn(discoverFn)
	if bh := srv.BoardHandler(); bh != nil {
		boardNotifier.SetIsPausedFn(bh.IsPaused)
		bh.SetNotifyFn(boardNotifier.NotifyBoard)
	}
	// Seeded before its first pass, but not before the server serves: on a long board
	// history seeding takes seconds (#3845, perf pass #3833 item 4). A NotifyNow meanwhile
	// waits in its channel for Run.
	safeGo(ctx, "board_notifier", func() {
		boardNotifier.SeedFromDB(ctx)
		boardNotifier.Run(ctx)
	})
	// Board health scanning — enabled by default; disable with
	// `board_health_monitor` user setting set to `false`.
	boardSettings, _ := sessStore.GetSettings(ctx)
	if boardSettings["board_health_monitor"] != "false" {
		healthMonitor := background.NewBoardHealthMonitor(srv.BoardStore(), 10*time.Minute)
		healthMonitor.SetRuntime(agentRT)
		healthMonitor.SetIdleThresholds(time.Duration(cfg.TaskIdleReminderS)*time.Second, time.Duration(cfg.TaskIdleEscalationS)*time.Second)
		safeGo(ctx, "board_health_monitor", func() { healthMonitor.Run(ctx) })
	}

	// Remote board poller
	remotePoller := background.NewRemoteBoardPoller(rbStore, agentRT, 30*time.Second)
	remotePoller.SetDiscoverFn(discoverFn)
	safeGo(ctx, "remote_board_poller", func() { remotePoller.Run(ctx) })

	// Batch summarizer — only runs if auto_summarize setting is enabled
	summarizeFn := background.BuildSummarizeFn(sessStore)
	settings, _ := sessStore.GetSettings(ctx)
	if config.Truthy(settings["auto_summarize"]) { // PUT /api/settings stores a JSON true as "True"
		batchSummarizer := background.NewBatchSummarizer(sessStore, summarizeFn)
		safeGo(ctx, "batch_summarizer", func() { batchSummarizer.Run(ctx) })
	}
	srv.SetSummarizeFn(summarizeFn)

	// Goal generator — keeps a short, current goal line for every live agent
	// from its transcript (auto_goals setting, on unless "false")
	goalGenerator := background.NewGoalGenerator(sessStore, taskStore, 30*time.Second)
	goalGenerator.SetMetricsStore(store.NewGoalMetricsStore(db))
	safeGo(ctx, "goal_generator", func() { goalGenerator.Run(ctx) })
	srv.SetGoalGenerator(goalGenerator)

	// Session reconciler — periodically detects crashed agents and marks them sleeping
	reconciler := background.NewSessionReconciler(sessStore, agentRT, 30*time.Second)
	reconciler.SetOnSlept(srv.AgentsSlept) // the exit notice and auto-resume (#3613)
	safeGo(ctx, "session_reconciler", func() { reconciler.Run(ctx) })

	// Token poller — extracts token usage from Codex/Claude transcripts for cost tracking
	tokenUsageStore := store.NewTokenUsageStore(db)
	// Reprice rows recorded before provider aliases (Codex/Antigravity model
	// names) were added so analytics reflects existing usage as well as new turns.
	_ = tokenUsageStore.RepriceZeroCosts(ctx, func(model string, input, output, cacheRead, cacheWrite int) float64 {
		return proxy.CalculateCost(model, proxy.TokenUsage{InputTokens: input, OutputTokens: output, CacheReadTokens: cacheRead, CacheWriteTokens: cacheWrite})
	})
	tokenPoller := background.NewTokenPoller(sessStore, tokenUsageStore, 30*time.Second)
	tokenPoller.SetSubagentStore(store.NewSubagentStore(db))
	safeGo(ctx, "token_poller", func() { tokenPoller.Run(ctx) })

	// Call metrics retention: keeps call_metrics to the retention window
	// (call_metrics_retention_days, 14 days by default) in short batches (#3261).
	metricsRetention := background.NewCallMetricsRetention(db)
	safeGo(ctx, "call_metrics_retention", func() { metricsRetention.Run(ctx) })

	// A pty Coral wakes the agents that were running when it last stopped (#3636): one pass per
	// start. Restart, not safeGo: safeGo restarts a service that returns, so the pass (and its
	// "agents were running" notice) ran again every few seconds (#4033).
	if cfg.TerminalBackend == "pty" {
		safego.Restart("startup/auto_wake", func() { srv.AutoWakeAfterRestart(ctx) })
	}

	// Uploads retention: pasted/dropped images expire after uploads_retention_days
	// (30 by default) and the folder stays under uploads_max_mb (#3364).
	uploadsRetention := background.NewUploadsRetention(db, cfg.CoralDir())
	safeGo(ctx, "uploads_retention", func() { uploadsRetention.Run(ctx) })

	// Artifact retention (#3554): off unless the Operator sets artifact_retention_days; the
	// first run waits for an acknowledged dry run (or 24 hours), and each run is capped.
	safeGo(ctx, "artifact_retention", func() { srv.RunArtifactRetention(ctx) })

	// Thinking tracker — no hook fires for model thinking, so thinking turns
	// and their durations are read from Claude transcripts into the activity stream
	thinkingTracker := background.NewThinkingTracker(sessStore, taskStore, 2*time.Second)
	safeGo(ctx, "thinking_tracker", func() { thinkingTracker.Run(ctx) })

	// Workflow runner — executes multi-step workflows (shell + agent)
	wfStore := store.NewWorkflowStore(db)
	wfRunner := background.NewWorkflowRunner(wfStore, launcher, agentRT, nil, nil, cfg.CoralDir(), cfg.AgentHost(), cfg.Port)
	srv.SetWorkflowRunner(wfRunner)
	scheduler.SetWorkflowRunner(wfRunner, wfStore)

	log.Printf("Started 8 background services + workflow runner (git poller, indexer, scheduler, board notifier, remote board poller, batch summarizer, session reconciler, token poller)")
	return wfRunner
}

// startCloudConnector wires the optional Cloud Hub connector. A bad --cloud-url is
// logged and ignored: the connector stays off and the server starts normally.
// cloudAttacher is the part of *server.Server the connector needs.
type cloudAttacher interface {
	SetCloud(*cloud.Manager)
	// Sign in with Coral Hub (#3966).
	HubSigninOrigins() []string
	HubSigninEvent(ctx context.Context, ev cloud.SigninEvent)
	EndHubSessions(ctx context.Context, why string)
}

func startCloudConnector(ctx context.Context, db *store.DB, cfg *config.Config, srv cloudAttacher, ss *store.SessionStore) {
	hubURL := ""
	if strings.TrimSpace(cfg.CloudURL) != "" {
		u, err := cloud.NormalizeHubURL(cfg.CloudURL)
		if err != nil {
			log.Printf("[CLOUD] ignoring --cloud-url: %v", err)
		} else {
			hubURL = u
		}
	}
	mgr := cloud.New(cloud.Options{
		HubURL:       hubURL,
		Store:        store.NewCloudStore(db, store.NewRemoteServerStore(db, cfg.CoralDir())),
		LocalBaseURL: "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.Port)),
		MaxMode:      routes.MaxModeFromSettings(ss),
		RedactTokens: routes.RedactTokensFromSettings(ss),
		Redactor:     secrets.Redactor(),
		HubLimits:    routes.HubLimitsFromSettings(ss),
		CoralVersion: config.ReportedVersion(),
		Logger:       slog.Default(),
		// Sign in with Coral Hub (#3966): the return origins, the owner-pin events, and the
		// sessions a lost link ends.
		SigninOrigins: srv.HubSigninOrigins,
		OnSigninEvent: srv.HubSigninEvent,
		OnLinkLost: func(ctx context.Context) {
			srv.EndHubSessions(ctx, "this computer is no longer linked to Coral Hub")
			if n, err := ss.ClearHubAutoAccept(ctx, store.AutoAcceptLinkRemoved); err != nil {
				log.Printf("[CLOUD] turning off Coral Hub auto-accept: %v", err)
			} else if n > 0 {
				log.Printf("[CLOUD] link removed: turned off auto-accept the Coral Hub enabled for %d agent(s)", n)
			}
		},
	})
	srv.SetCloud(mgr)
	// A one-time credential (--bootstrap-file, or on Linux the well-known
	// /run/coral/bootstrap.d/credential) enrolls this server before the connector
	// runs. It is checked locally first; it never blocks the server's own startup.
	src := cloud.CredentialSource{WellKnown: cfg.BootstrapFile == "", File: cfg.BootstrapFile, ProofFile: cfg.ProofFile}
	hostname, _ := os.Hostname()
	if hubURL == "" {
		if cfg.BootstrapFile != "" {
			mgr.Bootstrap(ctx, ss, hostname, src) // records no_hub_configured; reads nothing
		}
		return
	}
	log.Printf("[CLOUD] Coral Hub: %s (nothing is sent until this server is connected: Settings > Coral Hub)", hubURL)
	safeGo(ctx, "cloud_connector", func() {
		mgr.Bootstrap(ctx, ss, hostname, src)
		mgr.Run(ctx)
	})
}

// prepareLogDir makes the agents' pipe log folder, readable by this user only (#3601, weak site
// W17): an older install made it 0755, so an existing folder is tightened too.
func prepareLogDir(dir string) {
	if dir == "" {
		return
	}
	os.MkdirAll(dir, 0o700)
	os.Chmod(dir, 0o700)
}

// ResolveBackend picks this run's terminal backend: an explicit --backend (forced); on Windows
// always pty (no tmux there); else the saved setting when it is exactly "tmux" or "pty"; else
// tmux.
func ResolveBackend(flagValue, saved, goos string) (backend string, forced bool) {
	if flagValue != "" {
		if flagValue == "pty" {
			return "pty", true
		}
		return "tmux", true
	}
	if goos == "windows" {
		return "pty", false
	}
	if saved == "tmux" || saved == "pty" {
		return saved, false
	}
	return "tmux", false
}
