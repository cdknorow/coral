// Package startup provides shared server bootstrap logic used by all Coral
// entry points (coral, coral-tray, launch-coral). It handles database setup,
// terminal backend selection, HTTP server creation, and background service
// wiring so that each cmd/ binary doesn't have to duplicate this code.
package startup

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/subgentic/coral/internal/agentenv"
	"github.com/subgentic/coral/internal/auth"
	"github.com/subgentic/coral/internal/background"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/dbcrypt"
	"github.com/subgentic/coral/internal/logsafe"
	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/safego"
	"github.com/subgentic/coral/internal/secrets"
	"github.com/subgentic/coral/internal/server"
	"github.com/subgentic/coral/internal/server/routes"
	"github.com/subgentic/coral/internal/store"
)

// Options configures optional behaviors that differ between entry points.
type Options struct {
	// Instance is the data folder's instance lock, taken by the caller with LockInstance before it loaded the configuration (config.Load itself makes and tightens the folder), so a second
	// start that loses the lock touched nothing at all (#6388). Start owns it from then on. Nil: Start takes the lock itself, as soon as the folder is known (#6379).
	Instance *Instance

	// Restart, when set, restarts this process (the desktop apps' tray, #3460):
	// Settings > Run as a hub offers a Restart button then. It returns once the
	// restart is under way; the server shuts down shortly after.
	Restart func() error

	// QuitForUpdate, when set, makes this process quit for an update (#166): the local "Install update" has just started the updater, and the tray (which
	// supervises this server) must not start it again. It returns once the quit is under way; the server shuts down shortly after and the caller exits with
	// routes.UpdateQuitExitCode. Only the entry points a tray supervises set it.
	QuitForUpdate func() error

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
	// CORAL_STRICT_LOCAL_AUTH says off in a dev build (#3936, #4290), the same for coral, coral-tray and launch-coral.
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
	// scheduler's run goroutines are waited for at Shutdown, so none is still running its end-of-run code as the process exits (#6918).
	scheduler *background.JobScheduler
	// stopFirewall ends the firewall re-checks (#97): Shutdown and Close call it.
	stopFirewall context.CancelFunc
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
	if rs.stopFirewall != nil {
		rs.stopFirewall() // the firewall re-checks end with the server, whatever ctx Start was given (#97)
	}
	// Open sockets are hijacked connections, which Shutdown below neither waits for nor ends (#5366).
	routes.CloseWebsockets()
	if err := rs.HTTPServer.Shutdown(ctx); err != nil {
		log.Printf("Shutdown error: %v", err)
	}
	// A workflow's shell steps run in their own process group, so they don't stop with the
	// server by themselves: end them here (#3997).
	if rs.workflows != nil {
		rs.workflows.Shutdown(ctx)
	}
	// The run goroutines of the job scheduler end with their context: wait for them (briefly), so a run's end-of-run code is not racing the exit.
	// A run is left running at a shutdown (its agent outlives Coral), and the next start settles or adopts it.
	if rs.scheduler != nil && !rs.scheduler.WaitWatchers(3*time.Second) {
		log.Printf("[shutdown] a job run's goroutine was still running after 3s")
	}
	// The input gate's retry loop sleeps between tries: wake it and wait for it to end, so it doesn't
	// outlive the server (held sends are not delivered after Coral exits either way).
	ptymanager.StopGate(5 * time.Second)
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
	if rs.stopFirewall != nil {
		rs.stopFirewall()
	}
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

	// The instance lock comes before anything this start would write (#6379, from #6370's stray start): a second start that loses it creates no folder, tightens no mode, opens no file and logs
	// nothing about itself in the data folder. Only the read-only encryption check precedes it (a refused start leaves the home untouched, as before). A folder that does not exist yet has no
	// server to lose to: it is made first, since the lock file lives in it.
	coralDir := cfg.CoralDir()
	lockPath := filepath.Join(coralDir, "coral-server.lock")
	var instance *instanceLock
	if opts.Instance != nil { // the caller took it before it loaded the configuration (LockInstance): Start owns it from here and releases it on any failure
		instance, lockPath = opts.Instance.lock, opts.Instance.path
	}
	releaseOnError := true
	defer func() {
		if releaseOnError && instance != nil {
			instance.close(lockPath)
		}
	}()
	if err := rejectUnsupportedEncryption(cfg, coralDir); err != nil {
		return nil, err
	}
	if fi, statErr := os.Lstat(coralDir); instance == nil && statErr == nil && fi.IsDir() {
		var err error
		if instance, err = acquireInstanceLock(lockPath); err != nil {
			return nil, lockError(coralDir, lockPath, err)
		}
	}
	logStartup(opts)
	if err := prepareDataDirs(cfg, coralDir); err != nil {
		return nil, err
	}
	// Which data folder this server uses, as it uses it (#235): next to the tools line, so a test server's log names both.
	log.Printf("[startup] data folder: %s", logsafe.Path(coralDir)) // a data location, through logsafe (its census, #300)
	if instance == nil {
		var err error
		if instance, err = acquireInstanceLock(lockPath); err != nil {
			return nil, lockError(coralDir, lockPath, err)
		}
	}

	boardPath := filepath.Join(coralDir, "messageboard.db")
	security, dbKey, err := resolveDatabaseKey(cfg, coralDir, boardPath, opts)
	if err != nil {
		return nil, err
	}
	db, err := openDatabases(ctx, cfg, boardPath, dbKey, security)
	if err != nil {
		return nil, err
	}
	stopFirewall := func() {} // ends the firewall re-checks (started below), whatever ctx the caller passed
	closeDB := func(err error) (*RunningServer, error) {
		stopFirewall()
		db.Close()
		return nil, err
	}
	if err := prepareWithDatabase(ctx, db, cfg, opts); err != nil {
		return closeDB(err)
	}
	// Native TLS (#3355): load the pair before binding, so a bad pair stops startup.
	tlsConfig, err := loadTLSConfig(opts.TLSCert, opts.TLSKey)
	if err != nil {
		return closeDB(err)
	}
	cfg.TLS = tlsConfig != nil
	if cfg.StrictLocalAuth, err = strictLocalAuth(cfg.CoralDir(), opts.StrictLocalAuth); err != nil {
		return closeDB(err)
	}

	// Bind the port now and keep the listeners open to avoid a TOCTOU race
	// (another process grabbing the port between check and serve). One listener per --host
	// address, all on one port, plus 127.0.0.1 when the list names only network addresses
	// (#3594).
	lns, boundPort, err := listenAll(cfg)
	if err != nil {
		return closeDB(err)
	}
	// Where this server can be reached, recorded next to its PID for a second start on the same data folder (#235).
	scheme := "http"
	if tlsConfig != nil {
		scheme = "https"
	}
	instance.recordAddress(fmt.Sprintf("%s://localhost:%d", scheme, boundPort))
	// The DNS-rebinding check accepts exactly the names Coral listens on (#3594).
	auth.SetListenHosts(cfg.ListenHosts())
	// The re-checks end with the server (Shutdown, or a Start that fails later), not only with the caller's ctx: a caller may pass
	// context.Background().
	fwCtx, fwCancel := context.WithCancel(ctx)
	stopFirewall = fwCancel
	safego.Go("startup/firewall_check", func() { startFirewallCheck(fwCtx) }) // Windows: warn about a firewall rule broader than Coral needs (read-only, #97)
	if tlsConfig != nil {
		for i := range lns {
			lns[i] = newSniffListener(lns[i], tlsConfig)
		}
	}
	// Everything this server starts (the tmux server, agents, workflow steps) inherits this
	// process's environment: make it say THIS server, never whatever started it (#3421), and
	// always its loopback listener when it has one (#3594).
	agentenv.UseServerEnv(cfg.AgentHost(), boundPort, cfg.CoralDir())

	applyStartupSettings(ctx, db, cfg, &opts, backendFlag)

	// Select terminal backend and agent runtime
	prepareLogDir(cfg.LogDir)
	naming.SetDefaultLogDir(cfg.LogDir)
	backend, agentRT, terminal := selectBackend(opts.BackendType, cfg.LogDir, cfg.CoralDir())

	// Build the HTTP server
	srv, err := server.NewWithKey(cfg, db, backend, terminal, dbKey)
	if err != nil {
		return closeDB(err)
	}
	srv.SetRestarter(opts.Restart)
	srv.SetUpdateQuitter(opts.QuitForUpdate)
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
	httpServer := newHTTPServer(handler)

	// Start all background services
	workflows, scheduler := startBackgroundServices(ctx, db, cfg, srv, agentRT)
	startPprof(ctx)
	wd := startStallWatch(ctx, cfg.CoralDir(), srv.Router(), db, srv.BoardStore())
	srv.StartMachineHealth(ctx, wd)

	serveListeners(httpServer, lns, cfg, tlsConfig != nil, opts.OnServerError)

	releaseOnError = false
	return &RunningServer{
		stopFirewall: fwCancel,
		workflows:    workflows,
		scheduler:    scheduler,
		HTTPServer:   httpServer,
		Server:       srv,
		LoginToken:   loginToken,
		DB:           db,
		Backend:      backend,
		instance:     instance,
		lockPath:     lockPath,
	}, nil
}

// logStartup writes the startup line: version, tier, backend asked for, process, executable and environment.
func logStartup(opts Options) {
	exe, exeErr := os.Executable()
	log.Printf("[STARTUP] version=%q tier=%s backend=%s pid=%d exe=%q exe_err=%v path_entries=%d shell=%q coral_tmux_bin_set=%t",
		config.ReportedVersion(),
		config.TierName,
		opts.BackendType,
		os.Getpid(),
		logsafe.Path(exe),
		exeErr,
		logsafe.Count(),
		filepath.Base(os.Getenv("SHELL")),
		os.Getenv("CORAL_TMUX_BIN") != "",
	)
}
