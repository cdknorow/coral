// Package server provides the HTTP server, router, and middleware for Coral.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/cdknorow/coral/internal/cloud"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/google/uuid"

	"github.com/cdknorow/coral/internal/auth"
	"github.com/cdknorow/coral/internal/background"
	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/logexit"
	"github.com/cdknorow/coral/internal/machinehealth"
	"github.com/cdknorow/coral/internal/proxy"
	"github.com/cdknorow/coral/internal/ptymanager"
	"github.com/cdknorow/coral/internal/server/routes"
	"github.com/cdknorow/coral/internal/stallwatch"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/pkg/frontend"
)

// Frontend assets are embedded at build time. The directories must exist
// under pkg/frontend/ (with .gitkeep placeholders where empty).
// To serve the real Python frontend, copy src/coral/static/ and
// src/coral/templates/ into these directories before building.

// staticFS and templateFS are the shared UI assets from pkg/frontend, so the
// Cloud Hub serves the identical frontend.
var (
	staticFS   = frontend.Static
	templateFS = frontend.Templates
)

// Server holds dependencies and exposes the HTTP router.
type Server struct {
	cloudHandler    *routes.CloudHandler
	cfg             *config.Config
	db              *store.DB
	boardStore      *board.Store
	backend         ptymanager.TerminalBackend
	terminal        ptymanager.SessionTerminal
	keyStore        *auth.KeyStore
	identity        *routes.IdentityObserver
	router          chi.Router
	indexTmpl       *template.Template
	indexPage       renderedPage // the dashboard page, rendered and compressed once per data
	staticVersion   string       // build version of the static tree: /static/b/<staticVersion>/
	tasksHandler    *routes.TasksHandler
	boardHandler    *routes.BoardHandler
	historyHandler  *routes.HistoryHandler
	sessHandler     *routes.SessionsHandler
	remoteAgents    *background.RemoteAgentHub
	goalMetrics     *routes.GoalMetricsHandler
	systemHandler   *routes.SystemHandler
	workflowHandler *routes.WorkflowHandler
}

// remoteAgentHubConfig tunes the hub's remote poller. Zero values mean the
// production defaults; only in-package tests set it (to poll quickly).
var remoteAgentHubConfig background.RemoteAgentHubConfig

// templateData is passed to Go templates during rendering.
type templateData = frontend.PageData

// New creates a Server with all routes registered.
// If backend is nil, the server will use tmux-based terminal management.
func New(cfg *config.Config, db *store.DB, backend ptymanager.TerminalBackend, terminal ptymanager.SessionTerminal) *Server {
	return NewWithKey(cfg, db, backend, terminal, "")
}

// NewWithKey constructs the server and opens its separate board database with
// the same optional SQLCipher key used by the primary store.
func NewWithKey(cfg *config.Config, db *store.DB, backend ptymanager.TerminalBackend, terminal ptymanager.SessionTerminal, dbKey string) *Server {
	tightenDataPermissions(cfg.CoralDir()) // #3552
	// Initialize upload directory from config
	routes.InitUploadDir(cfg.CoralDir())
	if db != nil {
		routes.SetUploadSettings(store.NewSessionStore(db).GetSettings)
	}

	// Open the board store (separate SQLite DB)
	boardStore, err := board.NewStoreWithKey(filepath.Join(cfg.CoralDir(), "messageboard.db"), dbKey)
	if err != nil {
		log.Printf("Warning: failed to open board store: %v", err)
	}
	if boardStore != nil && db != nil {
		boardStore.SetSessionsDB(db.Reader())
	}
	applyBoardRekeys(context.Background(), boardStore, db, cfg.CoralDir())

	// Coral doesn't configure MCP servers (#3644): every stored MCP configuration, with the
	// tokens in its env and headers, is removed from the database, and removed for real.
	if n, err := store.WipeMCPConfig(context.Background(), db); err != nil {
		log.Printf("[mcp] clearing stored MCP server settings: %v", err)
	} else if n > 0 {
		log.Printf("[mcp] cleared stored MCP server settings from %d records (Coral no longer configures MCP servers)", n)
	}
	// Coral doesn't send webhooks (#3646): their URLs, usually secrets, are removed from both
	// databases, and removed for real.
	if n, err := store.WipeWebhookData(context.Background(), db); err != nil {
		log.Printf("[webhooks] clearing stored webhook data: %v", err)
	} else if n > 0 {
		log.Printf("[webhooks] cleared stored webhook data (%d records; Coral no longer sends webhooks)", n)
	}
	if n, err := boardStore.WipeWebhookURLs(context.Background()); err != nil {
		log.Printf("[webhooks] clearing board subscriptions' webhook URLs: %v", err)
	} else if n > 0 {
		log.Printf("[webhooks] cleared the webhook URL of %d board subscriptions", n)
	}

	// Initialize API key auth
	keyStore, err := auth.NewKeyStore(cfg.CoralDir())
	if err != nil {
		logexit.Fatalf("Failed to initialize API key store: %v", err)
	}
	// Never log any part of the key (#3356): Settings > Remote Access shows it on this machine.
	log.Printf("API key loaded (Settings > Remote Access shows it on this computer)")
	if err := keyStore.SetTrustedProxies(os.Getenv("CORAL_TRUSTED_PROXIES")); err != nil {
		log.Printf("[auth] CORAL_TRUSTED_PROXIES ignored: %v", err)
	}
	// require_auth_on_loopback (#3585): read once at startup; PutSettings re-applies it on change.
	// A failed read leaves it at the default (off); it never flips silently on a later read error.
	// CORAL_REQUIRE_AUTH_ON_LOOPBACK overrides the stored value (the HTTP-free recovery, point C).
	if auth.EnvOverridesRequireAuthOnLoopback() {
		auth.SetRequireAuthOnLoopback(auth.ResolveRequireAuthOnLoopback(""))
		log.Printf("[auth] %s overrides the stored require_auth_on_loopback setting", auth.RequireAuthOnLoopbackEnv)
	} else {
		ss := store.NewSessionStore(db)
		st, err := ss.GetSettings(context.Background())
		if err != nil { // a security switch must not fail silently open: say so and retry once (#3585 NIT 3)
			log.Printf("[auth] WARNING: could not read settings for require_auth_on_loopback (%v); retrying once", err)
			st, err = ss.GetSettings(context.Background())
		}
		if err != nil {
			log.Printf("[auth] WARNING: require_auth_on_loopback could not be read (%v); leaving it OFF until the next change saves it", err)
		} else {
			auth.SetRequireAuthOnLoopback(auth.SettingOn(st[auth.RequireAuthOnLoopbackSetting]))
		}
	}

	s := &Server{
		cfg:        cfg,
		db:         db,
		boardStore: boardStore,
		backend:    backend,
		terminal:   terminal,
		keyStore:   keyStore,
	}

	// Parse Go templates from embedded FS
	indexTmpl, err := frontend.ParseIndex()
	if err != nil {
		log.Printf("Warning: failed to parse index template: %v (serving placeholder)", err)
	}
	s.indexTmpl = indexTmpl

	s.router = s.buildRouter()

	return s
}

// Router returns the configured chi.Router for use with http.Server.
func (s *Server) Router() chi.Router {
	return s.router
}

// HasRemoteAgents reports whether this server runs the remote agent hub
// (hub mode only). Callers use it to decide whether to start RunRemoteAgents.
func (s *Server) HasRemoteAgents() bool {
	return s.remoteAgents != nil
}

// RunRemoteAgents polls registered remote servers and feeds the merged agent
// list until ctx ends. It blocks until ctx is done, even outside hub mode, so a
// supervisor never sees it return early.
func (s *Server) RunRemoteAgents(ctx context.Context) {
	if s.remoteAgents == nil {
		<-ctx.Done()
		return
	}
	s.remoteAgents.Run(ctx)
}

// SetScheduler injects the job scheduler into the tasks handler for launching/killing.
// SetCloud attaches the Cloud Hub connector manager to the /api/cloud routes.
// CloseCloud removes the command-line token at a graceful shutdown (#3285).
func (s *Server) CloseCloud() {
	if s.cloudHandler != nil {
		s.cloudHandler.Close()
	}
}

func (s *Server) SetCloud(m *cloud.Manager) {
	if s.cloudHandler != nil {
		s.cloudHandler.SetManager(m)
	}
	// Cross-team messages to another Coral go through the same Coral Hub tunnel (#3489).
	if s.boardHandler != nil && m != nil {
		s.boardHandler.SetRelay(m.Relay)
	}
}

func (s *Server) SetScheduler(sched *background.JobScheduler) {
	if s.tasksHandler != nil {
		s.tasksHandler.SetScheduler(sched)
	}
}

// SetIndexer injects the session indexer for manual refresh triggers.
func (s *Server) SetIndexer(idx routes.Indexer) {
	if s.systemHandler != nil {
		s.systemHandler.SetIndexer(idx)
	}
}

// SetWorkflowRunner injects the workflow runner into the workflow handler for triggering/killing.
func (s *Server) SetWorkflowRunner(wr *background.WorkflowRunner) {
	if s.workflowHandler != nil {
		s.workflowHandler.SetWorkflowRunner(wr)
	}
}

// SetSummarizeFn injects the summarize function into the history handler for sync resummarize.
func (s *Server) SetSummarizeFn(fn func(ctx context.Context, sessionID string) error) {
	if s.historyHandler != nil {
		s.historyHandler.SetSummarizeFn(fn)
	}
}

// SetGoalGenerator wires the goal generator into the sessions handler (the
// sparkle endpoint) and the metrics endpoint.
func (s *Server) SetGoalGenerator(g *background.GoalGenerator) {
	if s.sessHandler != nil {
		s.sessHandler.SetGoalRequester(g)
	}
	if s.goalMetrics != nil {
		s.goalMetrics.SetStatusProvider(g)
	}
}

// BoardHandler returns the board handler (used by notifier and sleep/wake).
func (s *Server) BoardHandler() *routes.BoardHandler {
	return s.boardHandler
}

// BoardStore returns the board store (used by board notifier background service).
// WriteLoginToken mints the startup dashboard login token and writes it to
// <data>/login_token (#3387). "" when there is no key store.
func (s *Server) WriteLoginToken() (string, error) {
	if s.keyStore == nil {
		return "", nil
	}
	return s.keyStore.WriteLoginTokenFile(s.cfg.CoralDir())
}

// MintLoginToken mints a fresh single-use dashboard sign-in token (#3387), for an app
// that runs Coral in-process (launch-coral, coral-tray): no API key, file or prompt.
func (s *Server) MintLoginToken() (string, error) {
	if s.keyStore == nil {
		return "", errors.New("no key store")
	}
	return s.keyStore.MintLoginToken()
}

// StartMachineHealth samples this machine's health every 15 s for the feeds (#3444): Coral's own
// process, the agents' process trees (the backend's agent processes), the host, the data folder's
// disk, and the stall watchdog's probe latencies (wd may be nil: the watchdog is off).
// AutoWakeAfterRestart wakes the agents that were running when this pty Coral last stopped
// (#3636); startup calls it once, on pty.
func (s *Server) AutoWakeAfterRestart(ctx context.Context) {
	if s.sessHandler != nil {
		s.sessHandler.AutoWakeAfterRestart(ctx)
	}
}

// AgentsSlept hears the agents the session reconciler found dead, for the exit notice and
// auto-resume (#3613).
func (s *Server) AgentsSlept(ctx context.Context, sessionIDs []string) {
	if s.sessHandler != nil {
		s.sessHandler.AgentsSlept(ctx, sessionIDs)
	}
}

// RunArtifactRetention runs #3554's artifact retention job (off unless the Operator sets
// artifact_retention_days) until ctx ends.
func (s *Server) RunArtifactRetention(ctx context.Context) {
	if s.sessHandler != nil {
		s.sessHandler.RunArtifactRetention(ctx)
	}
}

func (s *Server) StartMachineHealth(ctx context.Context, wd *stallwatch.Watchdog) {
	src := machinehealth.Source{DataDir: s.cfg.CoralDir()}
	if ap, ok := s.backend.(ptymanager.AgentProcesses); ok {
		src.AgentPIDs = ap.AgentPIDs
	}
	if wd != nil {
		src.Responsive = func() (machinehealth.Responsiveness, bool) {
			st := wd.Stats()
			return machinehealth.Responsiveness{Last: st.Last, P95: st.P95, LastSlow: st.LastSlow}, true
		}
	}
	sampler := machinehealth.Start(ctx, src)
	if s.sessHandler != nil {
		s.sessHandler.SetMachineHealth(sampler)
	}
}

func (s *Server) BoardStore() *board.Store {
	return s.boardStore
}

// Proxy returns the LLM proxy instance, or nil if not yet initialized.
// RestoreSleepingBoards restores board pause state for sleeping teams on startup.
func (s *Server) RestoreSleepingBoards() {
	ss := store.NewSessionStore(s.db)
	ctx := context.Background()

	// Clean up orphaned sleeping duplicates (from old wake code that created new rows)
	cleaned, _ := ss.CleanupOrphanedSleeping(ctx)
	if cleaned > 0 {
		log.Printf("Cleaned up %d orphaned sleeping session(s)", cleaned)
	}

	if s.boardHandler == nil {
		return
	}
	boards, err := ss.GetSleepingBoardNames(ctx)
	if err != nil || len(boards) == 0 {
		return
	}
	for _, b := range boards {
		s.boardHandler.SetPaused(b, true)
	}
	log.Printf("Restored pause state for %d sleeping board(s)", len(boards))
}

func (s *Server) buildRouter() chi.Router {
	r := chi.NewRouter()

	// Middleware
	r.Use(middleware.Recoverer)

	// Requests that came through the Cloud Hub tunnel (marked by the connector) are
	// re-checked against the connector's route policy before anything else sees them.
	r.Use(cloud.Guard(routes.MaxModeFromSettings(store.NewSessionStore(s.db))))
	// Through Coral Hub, no path under the home directory and no hidden file names (#3559).
	r.Use(routes.HubPaths)
	// NOTE: middleware.RealIP was intentionally removed. It rewrites r.RemoteAddr
	// from X-Forwarded-For headers, which would allow an attacker to spoof
	// 127.0.0.1 and bypass auth.IsLocalhost(). Coral is a desktop app with no
	// reverse proxy, so RealIP is not needed.

	// CORS: only this server's own origin (the Origin's host is the request's Host,
	// which also covers remote access by IP or hostname). Other ports of localhost are
	// NOT allowed: a page there is the same site as the dashboard, so the sign-in
	// cookie goes with its requests (#3511).
	r.Use(cors.Handler(cors.Options{
		AllowOriginFunc:  corsSameOrigin,
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"*"},
		AllowCredentials: true,
	}))

	// A write from another page in a browser is refused before any auth: a page on
	// another localhost port or any site can make the browser send one (#3511). Behind a
	// reverse proxy that rewrites Host, a trusted proxy's X-Forwarded-Host is Coral's own host
	// too (loopback, or CORAL_TRUSTED_PROXIES).
	r.Use(routes.ForeignBrowserWrites(s.keyStore.ForwardedHost))

	// API key auth — gates remote access behind an auto-generated key.
	// Localhost connections bypass auth entirely.
	if s.keyStore != nil {
		r.Use(auth.Middleware(s.keyStore))
	}

	// Debug request logger (session/ws calls only, when CORAL_DEBUG=1)
	r.Use(routes.DebugRequestLogger)
	// Board identities never carry control characters (#3376).
	r.Use(routes.RejectBadIdentities)
	// Through Coral Hub, nobody acts with an orchestrator's rights on a board (#3377).
	r.Use(routes.TunnelIdentityGuard(s.boardStore, func(r *http.Request) bool {
		return s.cloudHandler != nil && s.cloudHandler.HubOperatorAllowed(r)
	}))
	// Record API traffic centrally so clients do not need to post metrics.
	r.Use(routes.RequestMetrics(s.db))
	// Identity phase 1 (#3387): who each request is from; mismatches logged, never refused.
	s.identity = routes.NewIdentityObserver(s.db, s.boardStore, s.keyStore.HasValidSession)
	// In every mode a bare local caller needs a credential (#3421 v2): the API key or a
	// command-line signature (checked once here, then marked for the cloud routes).
	s.identity.SetVerifiers(s.keyStore.HasValidKey, func(r *http.Request) (*http.Request, bool) {
		if s.cloudHandler == nil {
			return r, false
		}
		return s.cloudHandler.VerifyCLI(r)
	})
	s.identity.SetOtherUser(func(r *http.Request) bool { return s.cloudHandler != nil && s.cloudHandler.OtherUser(r) })
	if s.cfg.StrictLocalAuth {
		// Phase 2 (#3418): refuse what the report lists instead of logging it.
		log.Printf("[identity] strict local auth is on: requests from this computer need an agent session token, the dashboard sign-in (coral open) or a command-line signature")
		s.identity.SetStrict()
	}
	r.Use(s.identity.Middleware)
	// A board's name in /api/board/<key>/... resolves to its id (#3536), after the identity
	// checks so only an authenticated caller sees an ambiguity's candidates.
	r.Use(routes.BoardKeyResolver(s.boardStore))

	// Health check — the native app polls this every 5s to detect crashes.
	// Auth middleware bypasses localhost.
	r.Get("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// "version" is additive; older hubs ignore unknown fields.
		// "hub" tells the frontend whether to show the multi-server UI.
		json.NewEncoder(w).Encode(map[string]any{"status": "ok", "version": config.ReportedVersion(), "hub": s.cfg.HubMode})
	})

	// Auth endpoints
	authRoutes := auth.NewRoutes(s.keyStore)
	authRoutes.SetStrictLocal(s.cfg.StrictLocalAuth) // /auth here shows the locked page (#3418)
	r.Get("/auth", authRoutes.AuthPage)
	r.Post("/auth", authRoutes.ValidateKey)
	r.Post("/auth/key", authRoutes.ValidateKey)
	r.Post("/auth/logout", authRoutes.Logout)
	r.Get("/api/system/api-key", authRoutes.GetAPIKey)
	r.Post("/api/system/api-key/regenerate", authRoutes.RegenerateKey)
	r.Get("/api/system/auth-status", authRoutes.AuthStatus)

	// ── API Routes ──────────────────────────────────────────────
	sessHandler := routes.NewSessionsHandler(s.db, s.cfg, s.backend, s.terminal, s.boardStore)
	s.sessHandler = sessHandler
	// The automated-input gate reads agents' state through it (#3431).
	sessHandler.InstallAutomatedInputHooks()
	sysHandler := routes.NewSystemHandler(s.db, s.cfg)
	sysHandler.SetTerminal(s.terminal)
	s.systemHandler = sysHandler
	histHandler := routes.NewHistoryHandler(s.db, s.cfg, s.boardStore)
	s.historyHandler = histHandler
	schedHandler := routes.NewScheduleHandler(s.db, s.cfg)
	workflowHandler := routes.NewWorkflowHandler(s.db, s.cfg)
	s.workflowHandler = workflowHandler
	themeHandler := routes.NewThemesHandler(s.cfg)
	themeHandler.SeedBundledThemes()
	// CORAL_NO_DEMO_SEED=1 (test servers): no demo workflow and no scheduled
	// job for it, so a test server never runs a real agent on its own (#3136).
	if !envTrue("CORAL_NO_DEMO_SEED") {
		workflowHandler.SeedDemoWorkflow(store.NewScheduleStore(s.db))
	}
	// The first-launch "ways to run Coral" screen (#3221) is shown once per install:
	// until the user picks one, the dashboard finds welcome_pending in its settings.
	// CORAL_NO_WELCOME=1 (test servers) never asks.
	if !envTrue("CORAL_NO_WELCOME") {
		markWelcomePending(s.db)
	}

	// Live sessions
	r.Get("/api/sessions/resolve", sessHandler.ResolveByPIDs)
	r.Get("/api/sessions/live", sessHandler.List)
	r.Get("/api/sessions/{sessionID}/resolve", sessHandler.ResolveSession)
	r.Get("/api/sessions/{sessionID}/status", sessHandler.SessionStatus)
	r.Get("/api/sessions/{sessionID}/changes", sessHandler.SessionChanges)
	r.Get("/api/sessions/{sessionID}/changes.diff", sessHandler.SessionChangesArtifact)
	r.Get("/api/sessions/live/{name}", sessHandler.Detail)
	r.Get("/api/sessions/live/{name}/capture", sessHandler.Capture)
	r.Get("/api/sessions/live/{name}/poll", sessHandler.Poll)
	r.Get("/api/sessions/live/{name}/chat", sessHandler.Chat)
	r.Get("/api/sessions/live/{name}/info", sessHandler.Info)
	r.Get("/api/sessions/live/{name}/files", sessHandler.Files)
	r.Post("/api/sessions/live/{name}/files/refresh", sessHandler.RefreshFiles)
	r.Get("/api/sessions/live/{name}/diff", sessHandler.Diff)
	r.Get("/api/sessions/live/{name}/changes.diff", sessHandler.ChangesDiff)
	r.Get("/api/sessions/live/{name}/search-files", sessHandler.SearchFiles)
	r.Get("/api/sessions/live/{name}/git", sessHandler.Git)
	r.Post("/api/sessions/live/{name}/send", sessHandler.Send)
	r.Post("/api/sessions/live/{name}/goal", sessHandler.RequestGoal)
	s.goalMetrics = routes.NewGoalMetricsHandler(s.db)
	r.Get("/api/goals/metrics", s.goalMetrics.Metrics)
	callMetrics := routes.NewCallMetricsHandler(s.db)
	r.Post("/api/call-metrics", callMetrics.Record)
	r.Get("/api/call-metrics/summary", callMetrics.Summary)
	r.Post("/api/sessions/live/{name}/keys", sessHandler.Keys)
	r.Post("/api/sessions/live/{name}/resize", sessHandler.Resize)
	r.Post("/api/sessions/live/{name}/kill", sessHandler.Kill)
	r.Post("/api/sessions/live/{name}/restart", sessHandler.Restart)
	r.Post("/api/sessions/live/{name}/resume", sessHandler.Resume)
	r.Post("/api/sessions/live/{name}/attach", sessHandler.Attach)
	r.Put("/api/sessions/live/{name}/display-name", sessHandler.SetDisplayName)
	r.Get("/api/sessions/live/{name}/file-content", sessHandler.GetFileContent)
	r.Get("/api/sessions/live/{name}/resolve-path", sessHandler.ResolvePath)
	r.Post("/api/sessions/live/{name}/open-in-editor", sessHandler.OpenInEditor)
	r.Get("/api/system/editors", sessHandler.ListEditors)
	r.Get("/api/sessions/live/{name}/file-original", sessHandler.GetFileOriginal)
	r.Put("/api/sessions/live/{name}/file-content", sessHandler.SaveFileContent)
	r.Put("/api/sessions/live/{name}/icon", sessHandler.SetIcon)
	r.Put("/api/sessions/live/{name}/name-color", sessHandler.SetNameColor)
	r.Post("/api/sessions/live/{name}/auto-accept/enable", sessHandler.EnableAutoAccept)
	r.Post("/api/sessions/live/{name}/auto-accept/disable", sessHandler.DisableAutoAccept)
	r.Post("/api/auto-accept/disable-all", sessHandler.DisableAllAutoAccept)
	r.Post("/api/sessions/live/{name}/context-window", sessHandler.UpdateContextWindow)
	r.Post("/api/sessions/launch", sessHandler.Launch)
	r.Post("/api/sessions/launch-team", sessHandler.LaunchTeam)
	r.Post("/api/sessions/live/team/{boardName}/kill", sessHandler.KillTeam)
	r.Post("/api/sessions/live/team/{boardName}/reset", sessHandler.ResetTeam)

	// Bulk sleep/wake all (must be registered before {name} routes to avoid conflicts)
	r.Post("/api/sessions/live/sleep-all", sessHandler.SleepAll)
	r.Post("/api/sessions/live/wake-all", sessHandler.WakeAll)

	// Team sleep/wake
	r.Get("/api/sessions/live/team/{boardName}/sleep-status", sessHandler.SleepStatus)
	r.Post("/api/sessions/live/team/{boardName}/sleep", sessHandler.Sleep)
	r.Post("/api/sessions/live/team/{boardName}/wake", sessHandler.Wake)

	// Individual session sleep/wake
	r.Post("/api/sessions/live/{sessionID}/sleep", sessHandler.SleepSession)
	r.Post("/api/sessions/live/{sessionID}/wake", sessHandler.WakeSession)
	// #3636: the start's wake of the agents running when a pty Coral stopped.
	r.Get("/api/system/auto-wake", sessHandler.AutoWakeStatus)
	r.Post("/api/system/auto-wake/dismiss", sessHandler.DismissAutoWake)
	r.Post("/api/sessions/live/{sessionID}/archive", sessHandler.ArchiveSession)
	r.Post("/api/sessions/live/{sessionID}/unarchive", sessHandler.UnarchiveSession)

	// Agent tasks
	sessHandler.RegisterAgentArtifacts(r)
	r.Get("/api/sessions/live/{name}/tasks", sessHandler.ListTasks)
	r.Post("/api/sessions/live/{name}/tasks", sessHandler.CreateTask)
	r.Patch("/api/sessions/live/{name}/tasks/{taskID}", sessHandler.UpdateTask)
	r.Delete("/api/sessions/live/{name}/tasks/{taskID}", sessHandler.DeleteTask)
	r.Post("/api/sessions/live/{name}/tasks/reorder", sessHandler.ReorderTasks)
	// The agent's own tasks (coral-agent task ...), mirroring the board task API
	r.Get("/api/agent/tasks", sessHandler.ListAgentTasksForAgent)
	r.Get("/api/agent/tasks/{taskID}", sessHandler.GetAgentTaskForAgent)
	r.Patch("/api/agent/tasks/{taskID}", sessHandler.UpdateAgentTaskForAgent)
	r.Post("/api/agent/tasks/{taskID}/publish", sessHandler.PublishAgentTaskForAgent)
	r.Post("/api/agent/tasks", sessHandler.AddAgentTaskForAgent)
	r.Post("/api/agent/tasks/claim", sessHandler.ClaimAgentTaskForAgent)
	r.Post("/api/agent/tasks/current", sessHandler.CurrentAgentTaskForAgent)
	r.Post("/api/agent/tasks/{taskID}/complete", sessHandler.CompleteAgentTaskForAgent)
	r.Post("/api/agent/tasks/{taskID}/cancel", sessHandler.CancelAgentTaskForAgent)

	// Agent session history search & context (coral-agent history search / context)
	r.Get("/api/agent/history/search", sessHandler.SearchAgentHistory)
	r.Get("/api/agent/history/context", sessHandler.GetAgentHistoryContext)
	r.Get("/api/sessions/live/{name}/history/search", sessHandler.SearchAgentHistoryForLive)
	r.Get("/api/sessions/live/{name}/history/context", sessHandler.GetAgentHistoryContextForLive)

	r.Get("/api/sessions/live/{name}/subagents", sessHandler.ListSubagents)
	r.Get("/api/sessions/live/{name}/subagents/{subagentID}", sessHandler.GetSubagent)

	// Agent notes
	r.Get("/api/sessions/live/{name}/notes", sessHandler.ListNotes)
	r.Post("/api/sessions/live/{name}/notes", sessHandler.CreateNote)
	r.Patch("/api/sessions/live/{name}/notes/{noteID}", sessHandler.UpdateNote)
	r.Delete("/api/sessions/live/{name}/notes/{noteID}", sessHandler.DeleteNote)

	// Agent events
	r.Get("/api/sessions/live/{name}/events", sessHandler.ListEvents)
	r.Post("/api/sessions/live/{name}/events", sessHandler.CreateEvent)
	r.Post("/api/sessions/live/{name}/turn-end", sessHandler.TurnEndHandoff)
	r.Get("/api/sessions/live/{name}/pending-tool", sessHandler.GetPendingTool)
	r.Post("/api/sessions/live/{name}/pending-tool", sessHandler.SetPendingTool)
	r.Get("/api/sessions/live/{name}/prompt-options", sessHandler.PromptOptions)
	r.Post("/api/sessions/live/{name}/answer-prompt", sessHandler.AnswerPrompt)
	r.Get("/api/sessions/live/{name}/events/counts", sessHandler.EventCounts)
	r.Delete("/api/sessions/live/{name}/events", sessHandler.ClearEvents)
	r.Post("/api/sessions/live/{name}/token-usage", sessHandler.RecordTokenUsage)

	// WebSocket
	r.Get("/ws/coral", sessHandler.WSCoral)
	r.Get("/ws/terminal/{name}", sessHandler.WSTerminal)

	// Cloud Hub connector settings (never reachable through the tunnel)
	s.cloudHandler = routes.NewCloudHandler(store.NewSessionStore(s.db), s.cfg.CoralDir())
	s.cloudHandler.OfferAtLaunch = s.cfg.OffersCloudConnect()
	r.Get("/api/cloud/status", s.cloudHandler.GetStatus)
	r.Post("/api/cloud/connect", s.cloudHandler.Connect)
	r.Post("/api/cloud/connect/cancel", s.cloudHandler.CancelConnect)
	r.Post("/api/cloud/disconnect", s.cloudHandler.Disconnect)
	r.Get("/api/system/hub-operator", s.cloudHandler.HubOperator)
	s.cloudHandler.HubModeRunning, s.cloudHandler.HubModeForced = s.cfg.HubMode, s.cfg.HubModeForced
	r.Get("/api/system/hub-mode", s.cloudHandler.HubMode)
	r.Put("/api/system/hub-mode", s.cloudHandler.HubMode)
	r.Post("/api/system/restart", s.cloudHandler.RestartServer)
	// The backend in use (startup can fall back to pty, #3634), and whether --backend chose it.
	s.cloudHandler.BackendRunning, s.cloudHandler.BackendForced = routes.TerminalKind(s.terminal), s.cfg.TerminalBackendForced
	r.Get("/api/system/terminal-backend", s.cloudHandler.TerminalBackend)
	r.Put("/api/system/terminal-backend", s.cloudHandler.TerminalBackend)
	r.Post("/api/system/terminal-backend/end-leftover", s.cloudHandler.EndLeftoverTmuxAgents)
	r.Put("/api/system/hub-operator", s.cloudHandler.HubOperator)
	s.cloudHandler.SetIdentityObserver(s.identity)
	s.cloudHandler.SetBoardStore(s.boardStore)
	// #3404: the v1.4.18 opt-in is replaced by an opt-out.
	routes.DropLegacyHubOperatorSetting(context.Background(), store.NewSessionStore(s.db))
	if s.keyStore != nil {
		s.cloudHandler.SetLoginMinter(s.keyStore.MintLoginToken)
	}
	r.Post("/api/system/login-token", s.cloudHandler.LoginToken)
	r.Get("/api/system/identity-report", s.cloudHandler.IdentityReport)
	r.Put("/api/cloud/max-mode", s.cloudHandler.SetMaxMode)
	r.Put("/api/cloud/allow-auto-accept", s.cloudHandler.SetAllowHubAutoAccept)
	r.Put("/api/cloud/redact-tokens", s.cloudHandler.SetRedactTokens)
	r.Post("/api/cloud/redeem", s.cloudHandler.Redeem)
	r.Post("/api/cloud/cli-hello", s.cloudHandler.CLIHello)

	// System / settings
	r.Get("/api/settings", sysHandler.GetSettings)
	r.Put("/api/settings", sysHandler.PutSettings)
	r.Put("/api/settings/git-diff-mode", sysHandler.PutGitDiffMode)
	r.Get("/api/system/privacy", sysHandler.GetPrivacyStatus)
	r.Delete("/api/system/debug-input-log", sysHandler.DeleteDebugInputLog)
	r.Get("/api/system/database-security", sysHandler.DatabaseSecurityStatus)
	r.Put("/api/system/database-security", sysHandler.SetDatabaseSecurity)
	r.Get("/api/settings/default-prompts", sysHandler.GetDefaultPrompts)
	r.Get("/api/settings/prompt-inspection", sysHandler.GetPromptInspection)
	r.Get("/api/agent-models", sysHandler.GetAgentModels)
	r.Get("/api/system/status", sysHandler.Status)
	trackHandler := routes.NewTrackingHandler(s.cfg.CoralDir())
	r.Post("/api/tracking/event", trackHandler.TrackEvent)
	r.Get("/api/system/telemetry", trackHandler.TelemetryDisclosure)
	r.Post("/api/system/telemetry/acknowledge", trackHandler.AcknowledgeTelemetryDisclosure)
	r.Get("/api/system/update-check", sysHandler.UpdateCheck)
	r.Get("/api/system/cli-check", sysHandler.CLICheck)
	r.Get("/api/system/qr", sysHandler.QRCode)
	r.Get("/api/system/network-info", sysHandler.NetworkInfo)
	r.Get("/api/filesystem/list", sysHandler.ListFilesystem)
	r.Get("/api/filesystem/is-git", sysHandler.IsGitDir)
	r.Post("/api/indexer/refresh", sysHandler.RefreshIndexer)
	r.Post("/api/teams/import", sysHandler.ImportTeam)
	r.Post("/api/teams/generate", sysHandler.GenerateTeam)
	r.Get("/api/teams/generate/{jobId}", sysHandler.GenerateTeamStatus)

	// Team persistence
	teamsHandler := routes.NewTeamsHandler(s.db)
	teamsHandler.SetBoardStore(s.boardStore)
	r.Get("/api/teams/all", teamsHandler.ListTeams)
	r.Get("/api/teams/detail/{name}/availability", sessHandler.TeamAvailability)
	r.Get("/api/teams/detail/{name}", teamsHandler.GetTeam)
	r.Delete("/api/teams/detail/{name}", teamsHandler.DeleteTeam)
	r.Post("/api/teams/detail/{name}/resurrect", sessHandler.ResurrectTeam)

	// Team knowledge distillation
	r.Post("/api/teams/detail/{name}/distill-knowledge", sessHandler.DistillKnowledge)
	r.Get("/api/teams/detail/{name}/distill-knowledge", sessHandler.GetKnowledgeStatus)
	r.Get("/api/teams/detail/{name}/knowledge", sessHandler.GetTeamKnowledge)
	r.Get("/api/teams/detail/{name}/knowledge/{agentName}", sessHandler.GetAgentKnowledge)
	r.Put("/api/teams/detail/{name}/knowledge/{agentName}", sessHandler.SaveAgentKnowledge)

	// Token usage
	tokenHandler := routes.NewTokenUsageHandler(s.db)
	r.Get("/api/token-usage", tokenHandler.ListUsage)
	r.Get("/api/token-usage/summary", tokenHandler.UsageSummary)
	r.Get("/api/token-usage/session/{sessionID}/turns", tokenHandler.SessionTurns)
	r.Get("/api/token-usage/timeseries", tokenHandler.UsageTimeSeries)
	r.Get("/api/token-usage/by-team", tokenHandler.UsageSummaryByBoard)
	r.Get("/api/token-usage/by-branch", tokenHandler.UsageSummaryByBranch)

	// Tags
	r.Get("/api/tags", sysHandler.ListTags)
	r.Post("/api/tags", sysHandler.CreateTag)
	r.Delete("/api/tags/{tagID}", sysHandler.DeleteTag)
	r.Post("/api/sessions/history/{sessionID}/tags", sysHandler.AddSessionTag)
	r.Delete("/api/sessions/history/{sessionID}/tags/{tagID}", sysHandler.RemoveSessionTag)

	// Folder tags
	r.Get("/api/folder-tags", sysHandler.GetAllFolderTags)
	r.Get("/api/folder-tags/{folderName}", sysHandler.GetFolderTags)
	r.Post("/api/folder-tags/{folderName}", sysHandler.AddFolderTag)
	r.Delete("/api/folder-tags/{folderName}/{tagID}", sysHandler.RemoveFolderTag)
	r.Get("/api/pinned-folders", sysHandler.ListPinnedFolders)
	r.Post("/api/pinned-folders", sysHandler.PinFolder)
	r.Delete("/api/pinned-folders", sysHandler.UnpinFolder)

	// History
	r.Get("/api/sessions/history", histHandler.ListSessions)
	r.Get("/api/sessions/history/search", histHandler.SearchChats)
	r.Get("/api/sessions/history/{sessionID}", histHandler.GetSessionDetail)
	r.Get("/api/sessions/history/{sessionID}/agent-notes", histHandler.GetSessionAgentNotes)
	r.Get("/api/sessions/history/{sessionID}/notes", histHandler.GetSessionNotes)
	r.Put("/api/sessions/history/{sessionID}/notes", histHandler.SaveSessionNotes)
	r.Post("/api/sessions/history/{sessionID}/resummarize", histHandler.Resummarize)
	r.Get("/api/sessions/history/{sessionID}/tags", histHandler.GetSessionTags)
	r.Get("/api/sessions/history/{sessionID}/git", histHandler.GetSessionGit)
	r.Get("/api/sessions/history/{sessionID}/events", histHandler.GetSessionEvents)
	r.Get("/api/sessions/history/{sessionID}/tasks", histHandler.GetSessionTasks)
	r.Get("/api/sessions/history/{sessionID}/files", histHandler.GetSessionFiles)
	r.Get("/api/sessions/history/{sessionID}/resume-info", histHandler.GetResumeInfo)

	// Scheduled jobs
	r.Get("/api/scheduled/jobs", schedHandler.ListJobs)
	r.Get("/api/scheduled/jobs/{jobID}", schedHandler.GetJob)
	r.Post("/api/scheduled/jobs", schedHandler.CreateJob)
	r.Put("/api/scheduled/jobs/{jobID}", schedHandler.UpdateJob)
	r.Delete("/api/scheduled/jobs/{jobID}", schedHandler.DeleteJob)
	r.Post("/api/scheduled/jobs/{jobID}/toggle", schedHandler.ToggleJob)
	r.Get("/api/scheduled/jobs/{jobID}/runs", schedHandler.GetJobRuns)
	r.Get("/api/scheduled/runs/recent", schedHandler.GetRecentRuns)
	r.Post("/api/scheduled/validate-cron", schedHandler.ValidateCron)

	// Workflows
	r.Get("/api/workflows", workflowHandler.ListWorkflows)
	r.Post("/api/workflows", workflowHandler.CreateWorkflow)
	r.Get("/api/workflows/runs/recent", workflowHandler.ListRecentRuns)
	r.Get("/api/workflows/by-name/{name}", workflowHandler.GetWorkflowByName)
	r.Post("/api/workflows/by-name/{name}/trigger", workflowHandler.TriggerWorkflowByName)
	r.Get("/api/workflows/{workflowID}", workflowHandler.GetWorkflow)
	r.Put("/api/workflows/{workflowID}", workflowHandler.UpdateWorkflow)
	r.Delete("/api/workflows/{workflowID}", workflowHandler.DeleteWorkflow)
	r.Post("/api/workflows/{workflowID}/trigger", workflowHandler.TriggerWorkflow)
	r.Get("/api/workflows/{workflowID}/runs", workflowHandler.ListWorkflowRuns)
	r.Get("/api/workflows/runs/{runID}", workflowHandler.GetWorkflowRun)
	r.Post("/api/workflows/runs/{runID}/kill", workflowHandler.KillWorkflowRun)
	r.Get("/api/workflows/runs/{runID}/steps/{stepIndex}/files/*", workflowHandler.GetStepFile)

	// Connected Apps/OAuth endpoints are retired. Existing rows remain in the
	// database for operator-managed cleanup and are never loaded by startup.

	// Themes
	r.Get("/api/themes", themeHandler.ListThemes)
	r.Get("/api/themes/variables", themeHandler.GetThemeVariables)
	r.Get("/api/themes/{name}", themeHandler.GetTheme)
	r.Put("/api/themes/{name}", themeHandler.SaveTheme)
	r.Delete("/api/themes/{name}", themeHandler.DeleteTheme)
	r.Post("/api/themes/import", themeHandler.ImportTheme)
	r.Post("/api/themes/generate", themeHandler.GenerateTheme)

	// Templates
	tmplHandler := routes.NewTemplatesHandler()
	r.Get("/api/templates/agents", tmplHandler.ListAgentCategories)
	r.Get("/api/templates/agents/{category}", tmplHandler.ListAgentsInCategory)
	r.Get("/api/templates/agents/{category}/{name}", tmplHandler.GetAgentTemplate)
	r.Get("/api/templates/commands", tmplHandler.ListCommandCategories)
	r.Get("/api/templates/commands/{category}", tmplHandler.ListCommandsInCategory)
	r.Get("/api/templates/commands/{category}/{name}", tmplHandler.GetCommandTemplate)

	// Uploads
	r.Post("/api/upload", routes.UploadFile)
	r.Delete("/api/upload/{name}", routes.DeleteUpload)

	sessHandler.RegisterAgentUI(r)

	// Custom views
	viewsHandler := routes.NewViewsHandler(s.db)
	r.Get("/api/views", viewsHandler.ListViews)
	r.Post("/api/views", viewsHandler.CreateView)
	r.Get("/api/views/{id}", viewsHandler.GetView)
	r.Put("/api/views/{id}", viewsHandler.UpdateView)
	r.Delete("/api/views/{id}", viewsHandler.DeleteView)

	// Remote server registry (multi-server hub). Hub-local, never proxied.
	// Only registered in hub mode (--hub or CORAL_HUB=1); otherwise these
	// paths do not exist and the server behaves as a standalone Coral.
	if s.cfg.HubMode {
		rsHandler := routes.NewRemoteServersHandler(s.db, s.cfg)
		r.Get("/api/servers", rsHandler.List)
		r.Post("/api/servers", rsHandler.Add)
		r.Patch("/api/servers/{id}", rsHandler.Update)
		r.Delete("/api/servers/{id}", rsHandler.Delete)
		r.Post("/api/servers/{id}/test", rsHandler.Test)
		remoteResolver := routes.NewRemoteResolver(rsHandler.Store())
		routes.MountRemoteProxy(r, remoteResolver)
		s.remoteAgents = background.NewRemoteAgentHub(rsHandler.Store(), remoteResolver, remoteAgentHubConfig)
		rsHandler.SetStatusListener(s.remoteAgents.Refresh) // an edited key shows at once (#3649)
		remoteResolver.SetFailureSink(s.remoteAgents)
		sessHandler.SetRemoteAgents(s.remoteAgents)
	}

	// Board remotes
	brHandler := routes.NewBoardRemotesHandler(s.db, s.cfg)
	r.Post("/api/board/remotes", brHandler.AddSubscription)
	r.Delete("/api/board/remotes", brHandler.RemoveSubscription)
	r.Get("/api/board/remotes", brHandler.ListSubscriptions)
	r.Get("/api/board/remotes/proxy/{remoteServer}/projects", brHandler.ProxyProjects)
	r.Get("/api/board/remotes/proxy/{remoteServer}/{project}/messages/all", brHandler.ProxyMessages)
	r.Get("/api/board/remotes/proxy/{remoteServer}/{project}/subscribers", brHandler.ProxySubscribers)
	r.Get("/api/board/remotes/proxy/{remoteServer}/{project}/messages/check", brHandler.ProxyCheckUnread)

	// Message board
	boardHandler := routes.NewBoardHandler(s.boardStore)
	boardHandler.SetTerminal(s.terminal)
	boardHandler.SetHubOperatorAllowed(func(r *http.Request) bool {
		return s.cloudHandler != nil && s.cloudHandler.HubOperatorAllowed(r)
	})
	boardHandler.SetArchivedSessionLookup(routes.ArchivedSessionLookup(store.NewSessionStore(s.db)))
	boardHandler.SetTaskArtifactWriter(s.cfg.CoralDir(), sessHandler.PersistTaskChanges)
	sessStore := store.NewSessionStore(s.db)
	boardHandler.SetAutoAttacher(routes.NewAutoAttacher(s.cfg.CoralDir(), routes.AutoAttachEnabled(sessStore), routes.SessionDirs(sessStore)))
	go boardHandler.RecoverTaskNotifications(context.Background())
	go func() {
		if err := boardHandler.RestoreSubscriberReminders(context.Background()); err != nil {
			log.Printf("restore subscriber reminders: %v", err)
		}
	}()
	s.boardHandler = boardHandler
	sessHandler.SetBoardHandler(boardHandler)
	sessHandler.SetScheduleStore(store.NewScheduleStore(s.db))
	sessHandler.SetTeamStore(teamsHandler.Store())
	sessHandler.SetAgentOperatorGate(s.identity.AgentOperatorAction)
	sessHandler.SetTokenStore(store.NewTokenUsageStore(s.db))
	// Automated sends are spaced per team (#3613): a session's team is the board it is on.
	ptymanager.SetTeamResolver(newTeamResolver(s.boardStore))
	notifStore := routes.NewNotificationStore()
	sessHandler.SetNotificationStore(notifStore)
	autoAccept := routes.NewAutoAcceptor(sessHandler)
	autoAccept.Start(context.Background())
	go autoAccept.Run(context.Background())
	go sessHandler.RecoverPersonalTaskNotifications(context.Background())
	// Sessions launched before their CLI version was recorded get it now (#3573).
	go sessHandler.BackfillCLIVersions(context.Background())
	notifHandler := routes.NewNotificationHandler(notifStore)
	r.Post("/api/notifications", notifHandler.Create)
	r.Get("/api/board/projects", boardHandler.ListProjects)
	r.Post("/api/board/{project}/subscribe", boardHandler.Subscribe)
	r.Delete("/api/board/{project}/subscribe", boardHandler.Unsubscribe)
	r.Post("/api/board/{project}/messages", boardHandler.PostMessage)
	r.Get("/api/board/{project}/messages", boardHandler.ReadMessages)
	r.Get("/api/board/{project}/messages/all", boardHandler.ListAllMessages)
	r.Post("/api/board/{project}/messages/mark-read", boardHandler.MarkRead)
	r.Get("/api/board/{project}/messages/check", boardHandler.CheckUnread)
	r.Delete("/api/board/{project}/messages/{messageID}", boardHandler.DeleteMessage)
	r.Get("/api/board/{project}/subscribers", boardHandler.ListSubscribers)
	r.Get("/api/board/{project}/peek", boardHandler.PeekAgent)
	r.Post("/api/board/{project}/pause", boardHandler.PauseBoard)
	r.Post("/api/board/{project}/resume", boardHandler.ResumeBoard)
	r.Get("/api/board/{project}/paused", boardHandler.GetPaused)
	r.Get("/api/board/{project}/settings", boardHandler.GetBoardSettings)
	r.Put("/api/board/{project}/settings", boardHandler.UpdateBoardSettings)
	r.Post("/api/board/{project}/external-messages", boardHandler.PostExternalMessage)
	// To another Coral through Coral Hub (#3489); this computer only (not in the tunnel's table).
	r.Post("/api/board/{project}/relay-messages", boardHandler.PostRelayMessage)
	r.Get("/api/board/{project}/relay-targets", boardHandler.ListRelayTargets)
	r.Post("/api/board/{project}/hub-notice", boardHandler.PostHubNotice)
	r.Delete("/api/board/{project}", boardHandler.DeleteBoard)
	r.Get("/api/board/{project}", boardHandler.GetBoardInfo)
	r.Patch("/api/board/{project}", boardHandler.RenameBoard)
	r.Get("/api/board/{project}/groups", boardHandler.ListGroups)
	r.Get("/api/board/{project}/groups/{groupID}/members", boardHandler.ListGroupMembers)
	r.Post("/api/board/{project}/groups/{groupID}/members", boardHandler.AddGroupMember)
	r.Delete("/api/board/{project}/groups/{groupID}/members/{sessionID}", boardHandler.RemoveGroupMember)
	r.Post("/api/board/{project}/waits", boardHandler.RegisterWait)
	r.Get("/api/board/{project}/waits", boardHandler.GetActiveWait)
	r.Delete("/api/board/{project}/waits", boardHandler.CancelWait)
	r.Get("/api/board/{project}/waits/poll", boardHandler.PollWait)

	// Board tasks (global)
	r.Get("/api/board/tasks", boardHandler.ListAllTasks)
	// Board tasks (per-project)
	r.Post("/api/board/{project}/tasks", boardHandler.CreateTask)
	r.Get("/api/board/{project}/status", sessHandler.TeamAvailability)
	r.Get("/api/board/{project}/working-mode", boardHandler.GetWorkingMode)
	r.Put("/api/board/{project}/working-mode", boardHandler.SetWorkingMode)
	r.Get("/api/board/{project}/working-mode/presets", boardHandler.ListWorkflowPresets)
	r.Post("/api/board/{project}/working-mode/presets", boardHandler.SaveWorkflowPreset)
	r.Put("/api/board/{project}/working-mode/presets/{presetID}", boardHandler.SaveWorkflowPreset)
	r.Post("/api/board/{project}/working-mode/presets/{presetID}/reset", boardHandler.ResetWorkflowPreset)
	r.Get("/api/board/{project}/tasks", boardHandler.ListTasks)
	r.Post("/api/board/{project}/tasks/claim", boardHandler.ClaimTask)
	r.Post("/api/board/{project}/tasks/current", boardHandler.ActiveTask)
	r.Get("/api/board/{project}/tasks/{taskID}", boardHandler.GetTask)
	r.Get("/api/board/{project}/tasks/{taskID}/artifact-content", boardHandler.TeamArtifactContent)
	r.Get("/api/board/{project}/artifacts", boardHandler.ListTeamArtifacts)
	r.Get("/api/session-artifacts", boardHandler.ListSessionArtifacts)
	r.Post("/api/board/{project}/tasks/{taskID}/complete", boardHandler.CompleteTaskByID)
	r.Post("/api/board/{project}/tasks/{taskID}/submit-review", boardHandler.SubmitCompletionReview)
	r.Post("/api/board/{project}/tasks/{taskID}/release-review", boardHandler.ReleaseCompletionReview)
	r.Post("/api/board/{project}/tasks/{taskID}/cancel", boardHandler.CancelTaskByID)
	r.Patch("/api/board/{project}/tasks/{taskID}", boardHandler.UpdateTask)
	r.Patch("/api/board/{project}/tasks/{taskID}/amend", boardHandler.AmendTask)
	r.Post("/api/board/{project}/tasks/{taskID}/reassign", boardHandler.ReassignTask)
	r.Post("/api/board/{project}/tasks/{taskID}/nudge", boardHandler.NudgeTask)
	r.Post("/api/board/{project}/tasks/{taskID}/snooze", boardHandler.SnoozeTaskReminder)
	r.Post("/api/board/{project}/tasks/{taskID}/reminder", boardHandler.RemindTask)
	r.Delete("/api/board/{project}/tasks/{taskID}/reminder", boardHandler.RemindTask)
	r.Post("/api/board/{project}/reminder", boardHandler.RemindSubscriber)
	r.Delete("/api/board/{project}/reminder", boardHandler.RemindSubscriber)
	r.Post("/api/board/{project}/tasks/{taskID}/publish", boardHandler.PublishTask)
	r.Get("/api/board/{project}/tasks/{taskID}/cost", boardHandler.TaskLiveCost)
	r.Get("/api/board/{project}/tasks/{taskID}/changes.diff", boardHandler.TaskChangesDiff)

	// One-shot tasks
	tasksHandler := routes.NewTasksHandler(s.db, s.cfg)
	s.tasksHandler = tasksHandler
	r.Post("/api/tasks/run", tasksHandler.SubmitTask)
	r.Get("/api/tasks/runs", tasksHandler.ListTasks)
	r.Get("/api/tasks/active", tasksHandler.ListActiveRuns)
	r.Get("/api/tasks/runs/{runID}", tasksHandler.GetTaskStatus)
	r.Post("/api/tasks/runs/{runID}/kill", tasksHandler.KillTask)

	// Proxy interception, CONNECT, proxy dashboards, and provider-key loading
	// are retired. Keep the historical proxy store read-only for cost reports.
	historicalProxyStore := proxy.NewStore(s.db.DB)
	tasksHandler.SetProxyStore(historicalProxyStore)

	// ── Static Files ────────────────────────────────────────────
	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		logexit.Fatalf("Failed to embed static files: %v", err)
	}
	if v, err := staticBuildVersion(staticSub); err == nil {
		s.staticVersion = v
	} else {
		log.Printf("static build version unavailable, serving plain /static paths: %v", err)
	}
	// Service worker needs wider scope header
	r.Get("/static/sw.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Service-Worker-Allowed", "/")
		w.Header().Set("Content-Type", "application/javascript")
		http.ServeFileFS(w, r, staticFS, "static/sw.js")
	})
	// Files: ETag + no-cache revalidation, gzip for text (static_assets.go). Directories
	// and misses keep the old no-store FileServer behaviour.
	r.Handle("/static/*", http.StripPrefix("/static/", staticAssetHandler(staticSub, noCacheHandler(http.FileServer(http.FS(staticSub))), cacheRevalidate)))
	// The shell's static URLs load from /static/b/<build>/ (immutable); relative
	// module imports, CSS url()s and import.meta.url-based URLs inherit the
	// prefix. Plain /static/ paths above keep working (service worker,
	// panel_host.html, links, older pages).
	r.Handle("/static/b/*", versionedStaticHandler(staticSub, s.staticVersion))

	// API spec for custom views
	r.Get("/api/spec.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		http.ServeFileFS(w, r, staticFS, "static/api-spec.json")
	})

	// Agent docs — embedded API reference documentation
	agentDocsSub, err := fs.Sub(staticFS, "static/agent_docs")
	if err != nil {
		log.Printf("Warning: failed to load agent_docs: %v", err)
	} else {
		docsHandler := routes.NewAgentDocsHandler(agentDocsSub)
		r.Get("/api/agent-docs", docsHandler.List)
		r.Get("/api/agent-docs/all", docsHandler.GetAll)
		r.Get("/api/agent-docs/{name}", docsHandler.Get)
	}

	// PWA manifest (also at /static/manifest.json and under the versioned prefix)
	r.Get("/manifest.json", rootManifestHandler(staticFS))

	// ── Dashboard SPA ───────────────────────────────────────────
	r.Get("/agent/{sessionID}", s.serveAgentPopout)
	r.Get("/agent/{server}/{sessionID}", s.serveAgentPopout)
	r.Get("/", s.serveIndex)
	for _, p := range DashboardPagePaths {
		r.Get(p, s.servePagePath)
	}
	// Bare /team and /board name no view: they go to /agents (QA, one rule with the slashes).
	r.Get("/team", s.servePagePath)
	r.Get("/board", s.servePagePath)
	return r
}

// servePagePath serves the shell for a dashboard page path, with one canonical form (#3532):
// a trailing slash redirects 301 to the path without it (the query kept), and a bare /team or
// /board, which names no view, redirects 301 to /agents. Coral Hub follows the same rule.
func (s *Server) servePagePath(w http.ResponseWriter, r *http.Request) {
	// The ESCAPED path: a redirect built from the decoded one would undo %40, %2F, %3F, %23 and
	// %20 and change what the link means (QA and Security on the 1a delta).
	p := r.URL.EscapedPath()
	clean := strings.TrimRight(p, "/")
	if clean == "/team" || clean == "/board" {
		clean = "/agents"
	}
	if clean != p {
		target := clean
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusMovedPermanently)
		return
	}
	s.serveIndex(w, r)
}

// DashboardPagePaths are the dashboard's page routes (#3532): each family is its base path and
// everything under it, serving the same shell as "/", GET only, behind the same middleware.
// The client router reads the view from the path and validates its segments; the server
// doesn't. An explicit list, never a NotFound
// catch-all, so a miss under /api, /ws, /auth or /static stays a 404 that isn't HTML.
// Coral Hub's webui serves the same families (the contract in #3532's hub spec).
var DashboardPagePaths = []string{
	"/agents", "/agents/*",
	"/chats", "/chats/*",
	"/team/*",
	"/board/*",
	"/workflows", "/workflows/*",
	"/jobs", "/jobs/*",
	"/analytics", "/analytics/*",
	"/docs", "/docs/*",
	"/servers", "/servers/*",
}

// noCacheHandler wraps an http.Handler to set Cache-Control headers that force
// the browser to revalidate on every request. This prevents stale JS/CSS after
// a server rebuild.
func noCacheHandler(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
		h.ServeHTTP(w, r)
	})
}

// staticBase is the versioned prefix, with its trailing slash, that the shell
// templates put in front of every static URL ("" before the version is known,
// which the templates render as /static/).
func (s *Server) staticBase() string {
	if s.staticVersion == "" {
		return ""
	}
	return "/static/b/" + s.staticVersion + "/"
}

// dashboardCSP for the dashboard pages (#3515):
//   - img-src: images only from this server, data: and blob:. A remote image in an agent's
//     post or chat is never fetched (the sanitizer already turns it into a link; this is the
//     second layer). Nothing on these pages needs a remote image (avatars are emoji; icons
//     and the logo are served from /static; external previews are sandboxed iframes).
//   - base-uri 'self' and object-src 'none': no injected <base> or plugin content.
//   - frame-ancestors 'self': no other site can frame the dashboard or the terminal view
//     (clickjacking). Nothing frames these pages but Coral itself: the native app shows them
//     top-level, Coral Hub serves its own copy (its proxy drops this header), and the
//     dashboard's own iframes load agent panels, previews and views, never these pages.
//   - script-src 'self' 'unsafe-inline' (#3623, rendering audit F3 stage 1): scripts only from
//     this server. Inline scripts and handlers stay allowed for now (csp_shapes.js tracks every
//     inline handler; stage 2 removes them, as Coral Hub's copy already has script-src 'self').
//     No eval: nothing in the dashboard or its vendored libraries uses eval or new Function.
//   - form-action 'self': a form can only post to this server.
//   - media-src 'self' blob: data: (Security's SHOULD on #3622): audio and video only from this
//     server, so remote media can't be fetched from the page (agent content can't carry media
//     tags at all; this is the second layer).
const dashboardCSP = "script-src 'self' 'unsafe-inline'; form-action 'self'; media-src 'self' blob: data:; " +
	"img-src 'self' data: blob:; base-uri 'self'; object-src 'none'; frame-ancestors 'self'"

func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", dashboardCSP)
	// Team, board and session names are in the page path now (#3532): a link out of the
	// dashboard never carries them in Referer.
	w.Header().Set("Referrer-Policy", "same-origin")
	purgePreSandboxCache(w, r)

	if s.indexTmpl == nil {
		w.Write([]byte(`<!DOCTYPE html><html><body>Template not loaded</body></html>`))
		return
	}
	data := templateData{CoralRoot: s.cfg.CoralRoot, StaticBase: s.staticBase()}
	render := func(out io.Writer) error { return s.indexTmpl.Execute(out, data) }
	if err := s.indexPage.serve(w, r, data.CoralRoot+"\x00"+data.StaticBase, render); err != nil {
		log.Printf("Error rendering index template: %v", err)
		http.Error(w, "Template render error", http.StatusInternalServerError)
	}
}

// serveAgentPopout renders the dashboard bundle in single-agent mode. A valid
// but currently unknown session still gets the shell so the client can
// distinguish indexing lag, a historical session, and a genuinely unknown ID.
func (s *Server) serveAgentPopout(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")
	parsed, err := uuid.Parse(sessionID)
	if err != nil || parsed.String() != sessionID {
		http.Error(w, "invalid session ID", http.StatusBadRequest)
		return
	}

	// /agent/{server}/{id} addresses an agent on a registered remote. The
	// server segment is only ever echoed to the page, never dialed here, but
	// it must still be a valid slug.
	server := chi.URLParam(r, "server")
	if server != "" && store.ValidateRemoteID(server) != nil {
		http.Error(w, "invalid server ID", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", dashboardCSP)
	w.Header().Set("Referrer-Policy", "same-origin") // as the dashboard shell (#3532)
	if s.indexTmpl == nil {
		w.Write([]byte(`<!DOCTYPE html><html><body>Template not loaded</body></html>`))
		return
	}
	data := templateData{
		CoralRoot:       s.cfg.CoralRoot,
		EntryMode:       "agent",
		TargetSessionID: sessionID,
		TargetServer:    server,
		StaticBase:      s.staticBase(),
	}
	// Rendered per request (the data names the session); served with the same
	// revalidation and compression as the dashboard page.
	var page renderedPage
	render := func(out io.Writer) error { return s.indexTmpl.Execute(out, data) }
	if err := page.serve(w, r, "", render); err != nil {
		log.Printf("Error rendering agent popout template: %v", err)
		http.Error(w, "Template render error", http.StatusInternalServerError)
	}
}

// corsSameOrigin is the CORS rule: only this server's own origin, the Origin's host
// being the request's Host (#3511).
func corsSameOrigin(r *http.Request, origin string) bool {
	parsed, err := url.Parse(origin)
	return err == nil && parsed.Host != "" && strings.EqualFold(parsed.Host, r.Host)
}

// markWelcomePending records that the first-launch screen is due, unless the user has
// already chosen (welcome_choice) or it is already due. The dashboard clears it.
func markWelcomePending(db *store.DB) {
	if db == nil {
		return
	}
	if _, err := db.Exec(`INSERT INTO user_settings (key, value)
		SELECT 'welcome_pending', 'True' WHERE NOT EXISTS (SELECT 1 FROM user_settings WHERE key = 'welcome_choice')
		ON CONFLICT(key) DO NOTHING`); err != nil {
		log.Printf("first-launch screen: %v", err)
	}
}

// envTrue reports whether the environment variable is set to 1 or true.
func envTrue(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// cacheMarkerCookie records that this browser has had the v1.4.12 cache purge.
const cacheMarkerCookie = "coral_csd"

// purgePreSandboxCache clears this origin's HTTP cache once per browser (v1.4.12, #3168).
//
// Before 1.4.12 /api/artifacts/<digest> served agent-written HTML and SVG inline, with no
// sandbox, as "public, max-age=1y, immutable". A browser that opened such a link keeps that
// response and can answer the same link from it for a year, whatever the server now sends.
// Clear-Site-Data: "cache" drops those entries (the static bundle is versioned and simply
// reloads).
//
// Browsers act on Clear-Site-Data only in a secure context (https, or a loopback host), so
// the marker is set only there: over plain http on a LAN address the header is ignored and
// sent again on every load, and the purge happens as soon as the dashboard is opened over
// https or on this machine.
func purgePreSandboxCache(w http.ResponseWriter, r *http.Request) {
	if _, err := r.Cookie(cacheMarkerCookie); err == nil {
		return
	}
	w.Header().Set("Clear-Site-Data", `"cache"`)
	secure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	if !secure && !loopbackHost(r.Host) {
		// Ignored here: keep revalidating normally and try again on the next load.
		return
	}
	// The purge runs once: send the full page so it is not a 304 that might bypass the
	// browser's handling of the header.
	r.Header.Del("If-None-Match")
	r.Header.Del("If-Modified-Since")
	http.SetCookie(w, &http.Cookie{
		Name:     cacheMarkerCookie,
		Value:    "1412",
		Path:     "/",
		MaxAge:   10 * 365 * 24 * 60 * 60,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
	})
}

// loopbackHost reports whether a Host header names this machine the way browsers treat as a
// secure context: localhost (and *.localhost), 127.0.0.0/8 or ::1.
func loopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.Trim(host, "[]"), ".")
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// SetRestarter lets Settings restart this process (#3460); nil (the default) offers no
// Restart button.
func (s *Server) SetRestarter(fn func() error) {
	if s.cloudHandler != nil {
		s.cloudHandler.Restart = fn
	}
}

// newTeamResolver maps an agent session to its board, for spacing automated sends (#3613),
// cached for 30 s so a notifier pass doesn't query the board database for every send.
func newTeamResolver(bs *board.Store) func(string) string {
	type entry struct {
		team string
		at   time.Time
	}
	var mu sync.Mutex
	cache := map[string]entry{}
	return func(sessionName string) string {
		if bs == nil || sessionName == "" {
			return ""
		}
		mu.Lock()
		if e, ok := cache[sessionName]; ok && time.Since(e.at) < 30*time.Second {
			mu.Unlock()
			return e.team
		}
		mu.Unlock()
		team := ""
		if sub, err := bs.GetSubscriptionBySessionName(context.Background(), sessionName); err == nil && sub != nil {
			team = sub.Project
		}
		mu.Lock()
		cache[sessionName] = entry{team: team, at: time.Now()}
		mu.Unlock()
		return team
	}
}
