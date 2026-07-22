// Package server provides the HTTP server, router, and middleware for Coral.
package server

import (
	"context"
	"embed"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"github.com/cdknorow/coral/internal/auth"
	"github.com/cdknorow/coral/internal/background"
	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/license"
	"github.com/cdknorow/coral/internal/proxy"
	"github.com/cdknorow/coral/internal/ptymanager"
	"github.com/cdknorow/coral/internal/server/routes"
	"github.com/cdknorow/coral/internal/store"
)

// Frontend assets are embedded at build time. The directories must exist
// under internal/server/frontend/ even if empty (with .gitkeep placeholders).
// To serve the real Python frontend, copy src/coral/static/ and
// src/coral/templates/ into these directories before building.

//go:embed all:frontend/static
var staticFS embed.FS

//go:embed all:frontend/templates
var templateFS embed.FS

// Server holds dependencies and exposes the HTTP router.
type Server struct {
	cfg             *config.Config
	db              *store.DB
	boardStore      *board.Store
	backend         ptymanager.TerminalBackend
	terminal        ptymanager.SessionTerminal
	licenseMgr      *license.Manager
	launchCounter   *license.LaunchCounter
	keyStore        *auth.KeyStore
	router          chi.Router
	indexTmpl       *template.Template
	tasksHandler    *routes.TasksHandler
	boardHandler    *routes.BoardHandler
	historyHandler  *routes.HistoryHandler
	systemHandler   *routes.SystemHandler
	workflowHandler *routes.WorkflowHandler
	proxy           *proxy.Proxy
}

// templateData is passed to Go templates during rendering.
type templateData struct {
	CoralRoot string
}

// New creates a Server with all routes registered.
// If backend is nil, the server will use tmux-based terminal management.
func New(cfg *config.Config, db *store.DB, backend ptymanager.TerminalBackend, terminal ptymanager.SessionTerminal) *Server {
	// Initialize upload directory from config
	routes.InitUploadDir(cfg.CoralDir())

	// Open the board store (separate SQLite DB)
	boardStore, err := board.NewStore(filepath.Join(cfg.CoralDir(), "messageboard.db"))
	if err != nil {
		log.Printf("Warning: failed to open board store: %v", err)
	}
	if boardStore != nil && db != nil {
		boardStore.SetSessionsDB(db.DB)
	}

	// Initialize license manager (skip when license not required)
	licenseMgr := license.NewManager(cfg.CoralDir())
	if cfg.LicenseRequired() && licenseMgr.NeedsRevalidation() {
		log.Println("Revalidating license...")
		licenseMgr.Revalidate()
	}

	// Track launch count for nag screen frequency
	launchCounter := license.NewLaunchCounter(cfg.CoralDir())
	launchCount := launchCounter.Increment()
	log.Printf("Launch count: %d (nag=%v)", launchCount, launchCounter.IsNagLaunch())

	// Initialize API key auth
	keyStore, err := auth.NewKeyStore(cfg.CoralDir())
	if err != nil {
		log.Fatalf("Failed to initialize API key store: %v", err)
	}
	log.Printf("API Key: %s...", keyStore.Key()[:8])

	s := &Server{
		cfg:        cfg,
		db:         db,
		boardStore: boardStore,
		backend:    backend,
		terminal:   terminal,
		licenseMgr:    licenseMgr,
		launchCounter: launchCounter,
		keyStore:      keyStore,
	}

	// Parse Go templates from embedded FS
	indexTmpl, err := template.ParseFS(templateFS,
		"frontend/templates/index.html",
		"frontend/templates/includes/sidebar.html",
		"frontend/templates/includes/modals.html",
		"frontend/templates/includes/views/live_session.html",
		"frontend/templates/includes/views/history_session.html",
		"frontend/templates/includes/views/message_board.html",
		"frontend/templates/includes/views/workflows.html",
		"frontend/templates/includes/views/connected_apps.html",
		"frontend/templates/includes/views/docs.html",
		"frontend/templates/includes/views/cost_dashboard.html",
	)
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

// SetScheduler injects the job scheduler into the tasks handler for launching/killing.
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

// BoardHandler returns the board handler (used by notifier and sleep/wake).
func (s *Server) BoardHandler() *routes.BoardHandler {
	return s.boardHandler
}

// BoardStore returns the board store (used by board notifier background service).
func (s *Server) BoardStore() *board.Store {
	return s.boardStore
}

// Proxy returns the LLM proxy instance, or nil if not yet initialized.
func (s *Server) Proxy() *proxy.Proxy {
	return s.proxy
}

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
	// NOTE: middleware.RealIP was intentionally removed. It rewrites r.RemoteAddr
	// from X-Forwarded-For headers, which would allow an attacker to spoof
	// 127.0.0.1 and bypass auth.IsLocalhost(). Coral is a desktop app with no
	// reverse proxy, so RealIP is not needed.

	// CORS: allow localhost origins and same-origin requests (where the
	// Origin host matches the request Host). This lets remote users who
	// access the server directly by IP/hostname work correctly, while
	// still blocking cross-site requests from unrelated origins.
	r.Use(cors.Handler(cors.Options{
		AllowOriginFunc: func(r *http.Request, origin string) bool {
			if isLocalhostOrigin(origin) {
				return true
			}
			// Allow same-origin: the browser's Origin should match the Host
			// the user navigated to. This covers remote access via IP/hostname.
			parsed, err := url.Parse(origin)
			if err == nil && parsed.Host == r.Host {
				return true
			}
			return false
		},
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"*"},
		AllowCredentials: true,
	}))

	// API key auth — gates remote access behind an auto-generated key.
	// Localhost connections bypass auth entirely.
	if s.keyStore != nil {
		r.Use(auth.Middleware(s.keyStore))
	}

	// License middleware — tracks license state but does not gate any routes.
	// Skipped entirely in dev and beta tiers (compile-time build tags).
	if s.cfg.LicenseRequired() {
		r.Use(license.Middleware(s.licenseMgr))
	}

	// Debug request logger (session/ws calls only, when CORAL_DEBUG=1)
	r.Use(routes.DebugRequestLogger)

	// Health check — the native app polls this every 5s to detect crashes.
	// Auth middleware bypasses localhost; license middleware ungates this path.
	r.Get("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	// License endpoints (ungated — must be accessible to activate)
	licRoutes := license.NewRoutes(s.licenseMgr)
	if secret := os.Getenv("CORAL_LS_WEBHOOK_SECRET"); secret != "" {
		licRoutes.SetWebhookSecret(secret)
	}
	r.Post("/api/license/activate", licRoutes.Activate)
	r.Post("/api/license/deactivate", licRoutes.Deactivate)
	r.Get("/api/license/status", licRoutes.Status)
	r.Post("/api/license/webhook", licRoutes.Webhook)

	// Auth endpoints
	authRoutes := auth.NewRoutes(s.keyStore)
	r.Get("/auth", authRoutes.AuthPage)
	r.Post("/auth", authRoutes.ValidateKey)
	r.Post("/auth/key", authRoutes.ValidateKey)
	r.Get("/api/system/api-key", authRoutes.GetAPIKey)
	r.Post("/api/system/api-key/regenerate", authRoutes.RegenerateKey)
	r.Get("/api/system/auth-status", authRoutes.AuthStatus)

	// ── API Routes ──────────────────────────────────────────────
	sessHandler := routes.NewSessionsHandler(s.db, s.cfg, s.backend, s.terminal, s.boardStore)
	sysHandler := routes.NewSystemHandler(s.db, s.cfg)
	s.systemHandler = sysHandler
	histHandler := routes.NewHistoryHandler(s.db, s.cfg, s.boardStore)
	s.historyHandler = histHandler
	schedHandler := routes.NewScheduleHandler(s.db, s.cfg)
	whHandler := routes.NewWebhooksHandler(s.db, s.cfg)
	workflowHandler := routes.NewWorkflowHandler(s.db, s.cfg)
	s.workflowHandler = workflowHandler
	themeHandler := routes.NewThemesHandler(s.cfg)
	themeHandler.SeedBundledThemes()
	workflowHandler.SeedDemoWorkflow(store.NewScheduleStore(s.db))

	// Live sessions
	r.Get("/api/sessions/resolve", sessHandler.ResolveByPIDs)
	r.Get("/api/sessions/live", sessHandler.List)
	r.Get("/api/sessions/live/{name}", sessHandler.Detail)
	r.Get("/api/sessions/live/{name}/capture", sessHandler.Capture)
	r.Get("/api/sessions/live/{name}/poll", sessHandler.Poll)
	r.Get("/api/sessions/live/{name}/chat", sessHandler.Chat)
	r.Get("/api/sessions/live/{name}/info", sessHandler.Info)
	r.Get("/api/sessions/live/{name}/files", sessHandler.Files)
	r.Post("/api/sessions/live/{name}/files/refresh", sessHandler.RefreshFiles)
	r.Get("/api/sessions/live/{name}/diff", sessHandler.Diff)
	r.Get("/api/sessions/live/{name}/search-files", sessHandler.SearchFiles)
	r.Get("/api/sessions/live/{name}/git", sessHandler.Git)
	r.Post("/api/sessions/live/{name}/send", sessHandler.Send)
	r.Post("/api/sessions/live/{name}/keys", sessHandler.Keys)
	r.Post("/api/sessions/live/{name}/resize", sessHandler.Resize)
	r.Post("/api/sessions/live/{name}/kill", sessHandler.Kill)
	r.Post("/api/sessions/live/{name}/restart", sessHandler.Restart)
	r.Post("/api/sessions/live/{name}/resume", sessHandler.Resume)
	r.Post("/api/sessions/live/{name}/attach", sessHandler.Attach)
	r.Put("/api/sessions/live/{name}/display-name", sessHandler.SetDisplayName)
	r.Get("/api/sessions/live/{name}/file-content", sessHandler.GetFileContent)
	r.Get("/api/sessions/live/{name}/file-original", sessHandler.GetFileOriginal)
	r.Put("/api/sessions/live/{name}/file-content", sessHandler.SaveFileContent)
	r.Put("/api/sessions/live/{name}/icon", sessHandler.SetIcon)
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

	// Agent tasks
	r.Get("/api/sessions/live/{name}/tasks", sessHandler.ListTasks)
	r.Post("/api/sessions/live/{name}/tasks", sessHandler.CreateTask)
	r.Patch("/api/sessions/live/{name}/tasks/{taskID}", sessHandler.UpdateTask)
	r.Delete("/api/sessions/live/{name}/tasks/{taskID}", sessHandler.DeleteTask)
	r.Post("/api/sessions/live/{name}/tasks/reorder", sessHandler.ReorderTasks)

	// Agent notes
	r.Get("/api/sessions/live/{name}/notes", sessHandler.ListNotes)
	r.Post("/api/sessions/live/{name}/notes", sessHandler.CreateNote)
	r.Patch("/api/sessions/live/{name}/notes/{noteID}", sessHandler.UpdateNote)
	r.Delete("/api/sessions/live/{name}/notes/{noteID}", sessHandler.DeleteNote)

	// Agent events
	r.Get("/api/sessions/live/{name}/events", sessHandler.ListEvents)
	r.Post("/api/sessions/live/{name}/events", sessHandler.CreateEvent)
	r.Get("/api/sessions/live/{name}/events/counts", sessHandler.EventCounts)
	r.Delete("/api/sessions/live/{name}/events", sessHandler.ClearEvents)
	r.Post("/api/sessions/live/{name}/token-usage", sessHandler.RecordTokenUsage)

	// WebSocket
	r.Get("/ws/coral", sessHandler.WSCoral)
	r.Get("/ws/terminal/{name}", sessHandler.WSTerminal)

	// System / settings
	r.Get("/api/settings", sysHandler.GetSettings)
	r.Put("/api/settings", sysHandler.PutSettings)
	r.Get("/api/settings/default-prompts", sysHandler.GetDefaultPrompts)
	r.Get("/api/agent-models", sysHandler.GetAgentModels)
	r.Get("/api/system/status", sysHandler.Status)
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
	r.Get("/api/teams/all", teamsHandler.ListTeams)
	r.Get("/api/teams/detail/{name}", teamsHandler.GetTeam)
	r.Delete("/api/teams/detail/{name}", teamsHandler.DeleteTeam)
	r.Post("/api/teams/detail/{name}/resurrect", sessHandler.ResurrectTeam)

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

	// History
	r.Get("/api/sessions/history", histHandler.ListSessions)
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

	// Connected Apps
	connAppsHandler := routes.NewConnectedAppsHandler(s.db, s.cfg)
	r.Get("/api/connected-apps", connAppsHandler.ListConnections)
	r.Get("/api/connected-apps/providers", connAppsHandler.ListProviders)
	r.Post("/api/connected-apps/auth/start", connAppsHandler.StartAuth)
	r.Get("/api/connected-apps/callback", connAppsHandler.Callback)
	r.Get("/api/connected-apps/{id}", connAppsHandler.GetConnection)
	r.Get("/api/connected-apps/{id}/token", connAppsHandler.GetToken)
	r.Delete("/api/connected-apps/{id}", connAppsHandler.DeleteConnection)
	r.Post("/api/connected-apps/{id}/test", connAppsHandler.TestConnection)

	// Webhooks
	r.Get("/api/webhooks", whHandler.ListWebhooks)
	r.Post("/api/webhooks", whHandler.CreateWebhook)
	r.Patch("/api/webhooks/{webhookID}", whHandler.UpdateWebhook)
	r.Delete("/api/webhooks/{webhookID}", whHandler.DeleteWebhook)
	r.Post("/api/webhooks/{webhookID}/test", whHandler.TestWebhook)
	r.Get("/api/webhooks/{webhookID}/deliveries", whHandler.ListDeliveries)

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

	// Custom views
	viewsHandler := routes.NewViewsHandler(s.db)
	r.Get("/api/views", viewsHandler.ListViews)
	r.Post("/api/views", viewsHandler.CreateView)
	r.Get("/api/views/{id}", viewsHandler.GetView)
	r.Put("/api/views/{id}", viewsHandler.UpdateView)
	r.Delete("/api/views/{id}", viewsHandler.DeleteView)

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
	s.boardHandler = boardHandler
	sessHandler.SetBoardHandler(boardHandler)
	sessHandler.SetLicenseManager(s.licenseMgr)
	sessHandler.SetScheduleStore(store.NewScheduleStore(s.db))
	sessHandler.SetTeamStore(teamsHandler.Store())
	sessHandler.SetTokenStore(store.NewTokenUsageStore(s.db))
	notifStore := routes.NewNotificationStore()
	sessHandler.SetNotificationStore(notifStore)
	notifHandler := routes.NewNotificationHandler(notifStore)
	r.Post("/api/notifications", notifHandler.Create)
	r.Get("/api/board/projects", boardHandler.ListProjects)
	r.Post("/api/board/{project}/subscribe", boardHandler.Subscribe)
	r.Delete("/api/board/{project}/subscribe", boardHandler.Unsubscribe)
	r.Post("/api/board/{project}/messages", boardHandler.PostMessage)
	r.Get("/api/board/{project}/messages", boardHandler.ReadMessages)
	r.Get("/api/board/{project}/messages/all", boardHandler.ListAllMessages)
	r.Get("/api/board/{project}/messages/check", boardHandler.CheckUnread)
	r.Delete("/api/board/{project}/messages/{messageID}", boardHandler.DeleteMessage)
	r.Get("/api/board/{project}/subscribers", boardHandler.ListSubscribers)
	r.Get("/api/board/{project}/peek", boardHandler.PeekAgent)
	r.Post("/api/board/{project}/pause", boardHandler.PauseBoard)
	r.Post("/api/board/{project}/resume", boardHandler.ResumeBoard)
	r.Get("/api/board/{project}/paused", boardHandler.GetPaused)
	r.Delete("/api/board/{project}", boardHandler.DeleteBoard)
	r.Get("/api/board/{project}/groups", boardHandler.ListGroups)
	r.Get("/api/board/{project}/groups/{groupID}/members", boardHandler.ListGroupMembers)
	r.Post("/api/board/{project}/groups/{groupID}/members", boardHandler.AddGroupMember)
	r.Delete("/api/board/{project}/groups/{groupID}/members/{sessionID}", boardHandler.RemoveGroupMember)

	// Board tasks (global)
	r.Get("/api/board/tasks", boardHandler.ListAllTasks)
	// Board tasks (per-project)
	r.Post("/api/board/{project}/tasks", boardHandler.CreateTask)
	r.Get("/api/board/{project}/tasks", boardHandler.ListTasks)
	r.Post("/api/board/{project}/tasks/claim", boardHandler.ClaimTask)
	r.Post("/api/board/{project}/tasks/current", boardHandler.ActiveTask)
	r.Post("/api/board/{project}/tasks/{taskID}/complete", boardHandler.CompleteTaskByID)
	r.Post("/api/board/{project}/tasks/{taskID}/cancel", boardHandler.CancelTaskByID)
	r.Patch("/api/board/{project}/tasks/{taskID}", boardHandler.UpdateTask)
	r.Post("/api/board/{project}/tasks/{taskID}/reassign", boardHandler.ReassignTask)
	r.Post("/api/board/{project}/tasks/{taskID}/publish", boardHandler.PublishTask)
	r.Get("/api/board/{project}/tasks/{taskID}/cost", boardHandler.TaskLiveCost)

	// One-shot tasks
	tasksHandler := routes.NewTasksHandler(s.db, s.cfg)
	s.tasksHandler = tasksHandler
	r.Post("/api/tasks/run", tasksHandler.SubmitTask)
	r.Get("/api/tasks/runs", tasksHandler.ListTasks)
	r.Get("/api/tasks/active", tasksHandler.ListActiveRuns)
	r.Get("/api/tasks/runs/{runID}", tasksHandler.GetTaskStatus)
	r.Post("/api/tasks/runs/{runID}/kill", tasksHandler.KillTask)

	// ── LLM Proxy ──────────────────────────────────────────────
	llmProxy := proxy.New(s.db.DB, proxy.DefaultProviderConfigs())
	s.proxy = llmProxy
	tasksHandler.SetProxyStore(llmProxy.Store())

	// Proxy passthrough routes (outside /api to avoid auth collision with agent requests)
	r.Route("/proxy", func(sub chi.Router) {
		sub.Get("/health", llmProxy.Health)
		sub.Post("/{sessionID}/v1/messages", llmProxy.HandleAnthropicMessages)
		sub.Post("/{sessionID}/v1/chat/completions", llmProxy.HandleOpenAIChatCompletions)
		sub.Post("/{sessionID}/v1/responses", llmProxy.HandleOpenAIResponses)
		sub.Post("/{sessionID}/responses", llmProxy.HandleOpenAIResponses)
	})

	// MITM HTTPS proxy for transparent cost tracking (Codex agents set HTTPS_PROXY to this server)
	if ca, err := proxy.EnsureCA(s.cfg.CoralDir()); err != nil {
		log.Printf("[server] MITM proxy CA setup failed (CONNECT proxying disabled): %v", err)
	} else {
		mitmProxy := proxy.NewMITMProxy(ca, llmProxy.Store(), llmProxy.Events())
		r.Connect("/*", mitmProxy.HandleConnect)
		// Create combined cert bundle (system CAs + Coral CA) for agent SSL_CERT_FILE
		if err := proxy.EnsureCABundle(s.cfg.CoralDir()); err != nil {
			log.Printf("[server] CA bundle creation failed: %v", err)
		}
	}

	// Proxy dashboard API
	proxyDash := routes.NewProxyDashboardHandler(llmProxy.Store(), llmProxy.Events())
	r.Get("/api/proxy/pricing", proxyDash.Pricing)
	r.Get("/api/proxy/stats", proxyDash.Stats)
	r.Get("/api/proxy/requests", proxyDash.ListRequests)
	r.Get("/api/proxy/requests/{requestID}", proxyDash.GetRequest)
	r.Get("/api/proxy/session/{sessionID}/cost", proxyDash.SessionCost)
	r.Get("/api/proxy/tasks/runs/{runID}/cost", proxyDash.TaskRunCost)
	r.Get("/ws/proxy", proxyDash.WSProxy)

	// ── Static Files ────────────────────────────────────────────
	staticSub, err := fs.Sub(staticFS, "frontend/static")
	if err != nil {
		log.Fatalf("Failed to embed static files: %v", err)
	}
	// Service worker needs wider scope header
	r.Get("/static/sw.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Service-Worker-Allowed", "/")
		w.Header().Set("Content-Type", "application/javascript")
		http.ServeFileFS(w, r, staticFS, "frontend/static/sw.js")
	})
	r.Handle("/static/*", http.StripPrefix("/static/", noCacheHandler(http.FileServer(http.FS(staticSub)))))

	// API spec for custom views
	r.Get("/api/spec.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		http.ServeFileFS(w, r, staticFS, "frontend/static/api-spec.json")
	})

	// Agent docs — embedded API reference documentation
	agentDocsSub, err := fs.Sub(staticFS, "frontend/static/agent_docs")
	if err != nil {
		log.Printf("Warning: failed to load agent_docs: %v", err)
	} else {
		docsHandler := routes.NewAgentDocsHandler(agentDocsSub)
		r.Get("/api/agent-docs", docsHandler.List)
		r.Get("/api/agent-docs/all", docsHandler.GetAll)
		r.Get("/api/agent-docs/{name}", docsHandler.Get)
	}

	// PWA manifest (also accessible at /static/manifest.json)
	r.Get("/manifest.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/manifest+json")
		http.ServeFileFS(w, r, staticFS, "frontend/static/manifest.json")
	})

	// ── Dashboard SPA ───────────────────────────────────────────
	r.Get("/", s.serveIndex)
	return r
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

func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	// Show activation nag screen if: license required, not yet licensed,
	// it's a nag launch (every 3rd), and user hasn't clicked "skip".
	if s.cfg.LicenseRequired() && !s.licenseMgr.IsValid() &&
		s.launchCounter.IsNagLaunch() && r.URL.Query().Get("skip_activation") != "1" {
		s.serveActivation(w, r)
		return
	}

	if s.indexTmpl == nil {
		w.Write([]byte(`<!DOCTYPE html><html><body>Template not loaded</body></html>`))
		return
	}
	data := templateData{CoralRoot: s.cfg.CoralRoot}
	if err := s.indexTmpl.Execute(w, data); err != nil {
		log.Printf("Error rendering index template: %v", err)
		http.Error(w, "Template render error", http.StatusInternalServerError)
	}
}

func (s *Server) serveActivation(w http.ResponseWriter, r *http.Request) {
	page := activationPage
	page = strings.ReplaceAll(page, "{{STORE_URL}}", config.StoreURL)
	w.Write([]byte(page))
}

const activationPage = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Coral — Activate License</title>
<style>
  *, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }
  body {
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
    background: #0f1117;
    color: #e1e4e8;
    display: flex;
    align-items: center;
    justify-content: center;
    min-height: 100vh;
    padding: 24px;
  }
  .page { max-width: 780px; width: 100%; }
  .page-header { text-align: center; margin-bottom: 32px; }
  .page-header h1 { font-size: 28px; font-weight: 700; margin-bottom: 6px; }
  .page-header p { color: #8b949e; font-size: 15px; }

  /* Pricing cards */
  .pricing-row { display: flex; gap: 16px; margin-bottom: 24px; }
  .price-card {
    flex: 1;
    background: #161b22;
    border: 1px solid #30363d;
    border-radius: 12px;
    padding: 28px 24px;
    position: relative;
  }
  .price-card.featured { border-color: #58a6ff; }
  .price-badge {
    position: absolute; top: -10px; right: 16px;
    background: #58a6ff; color: #fff;
    font-size: 11px; font-weight: 700;
    padding: 3px 10px; border-radius: 10px;
    text-transform: uppercase; letter-spacing: 0.5px;
  }
  .price-card h3 { font-size: 18px; font-weight: 700; margin-bottom: 4px; }
  .price-desc { color: #8b949e; font-size: 13px; margin-bottom: 16px; }
  .price-amount { font-size: 32px; font-weight: 800; margin-bottom: 4px; }
  .price-amount .strike { text-decoration: line-through; color: #484f58; font-size: 20px; font-weight: 500; margin-right: 6px; }
  .price-note { color: #8b949e; font-size: 12px; margin-bottom: 16px; }
  .price-features { list-style: none; margin-bottom: 20px; }
  .price-features li {
    font-size: 13px; color: #c9d1d9;
    padding: 4px 0 4px 22px;
    position: relative;
  }
  .price-features li::before {
    content: '\2713'; color: #3fb950; font-weight: 700;
    position: absolute; left: 0;
  }
  .price-btn {
    display: block; width: 100%; text-align: center;
    padding: 10px; border-radius: 8px;
    font-size: 14px; font-weight: 600;
    text-decoration: none; cursor: pointer;
    transition: background 0.15s, border-color 0.15s;
    border: none;
  }
  .price-btn-primary { background: #58a6ff; color: #fff; }
  .price-btn-primary:hover { background: #4090e0; }
  .price-btn-secondary { background: transparent; color: #58a6ff; border: 1px solid #30363d; }
  .price-btn-secondary:hover { border-color: #58a6ff; }

  /* Activation card */
  .activate-card {
    background: #161b22;
    border: 1px solid #30363d;
    border-radius: 12px;
    padding: 32px 48px;
    text-align: center;
  }
  .activate-card h3 { font-size: 16px; font-weight: 600; margin-bottom: 6px; }
  .activate-card .subtitle { color: #8b949e; font-size: 13px; margin-bottom: 20px; }
  .input-group { position: relative; margin-bottom: 12px; }
  .input-group input {
    width: 100%;
    padding: 11px 16px;
    background: #0d1117;
    border: 1px solid #30363d;
    border-radius: 8px;
    color: #e1e4e8;
    font-size: 14px;
    font-family: monospace;
    letter-spacing: 1px;
    outline: none;
    transition: border-color 0.2s;
  }
  .input-group input:focus { border-color: #58a6ff; }
  .activate-btn {
    width: 100%;
    padding: 11px;
    background: #238636;
    color: #fff;
    border: none;
    border-radius: 8px;
    font-size: 14px;
    font-weight: 600;
    cursor: pointer;
    transition: background 0.2s;
  }
  .activate-btn:hover { background: #2ea043; }
  .activate-btn:disabled { background: #21262d; color: #484f58; cursor: not-allowed; }
  .error { color: #f85149; font-size: 13px; margin-top: 10px; display: none; }
  .success { color: #3fb950; font-size: 13px; margin-top: 10px; display: none; }

  .skip-btn {
    display: block; width: 100%; text-align: center;
    padding: 11px; margin-top: 12px;
    background: transparent; color: #8b949e;
    border: 1px solid #30363d; border-radius: 8px;
    font-size: 13px; cursor: pointer;
    text-decoration: none;
    transition: color 0.15s, border-color 0.15s;
  }
  .skip-btn:hover { color: #c9d1d9; border-color: #484f58; }

  .footer-link { text-align: center; margin-top: 20px; }
  .footer-link a { color: #8b949e; font-size: 13px; text-decoration: none; }
  .footer-link a:hover { color: #58a6ff; }

  @media (max-width: 600px) {
    .pricing-row { flex-direction: column; }
    .activate-card { padding: 24px; }
  }
</style>
</head>
<body>
<div class="page">
  <div class="page-header">
    <h1>Coral</h1>
    <p>Free and fully unlocked. A one-time license supports development and retires this reminder.</p>
  </div>

  <div class="pricing-row">
    <div class="price-card">
      <h3>Free &amp; Fully Unlocked</h3>
      <p class="price-desc">The full product, forever</p>
      <div class="price-amount">Free</div>
      <p class="price-note">No card required &middot; every feature unlocked</p>
      <ul class="price-features">
        <li>Native desktop app (macOS &amp; Linux)</li>
        <li>Every feature, no time limit</li>
        <li>Unlimited Teams &amp; Agents</li>
        <li>Claude, Codex &amp; Gemini support</li>
        <li>Real-time dashboard &amp; message boards</li>
        <li>Skip this screen anytime</li>
      </ul>
      <a href="/?skip_activation=1" class="price-btn price-btn-secondary">Continue Free</a>
    </div>

    <div class="price-card featured">
      <div class="price-badge">Early Adopter</div>
      <h3>Supporter License</h3>
      <p class="price-desc">Back the project &amp; hide this reminder</p>
      <div class="price-amount">$49.99</div>
      <p class="price-note">One-time &middot; early adopter price rises as we add features</p>
      <ul class="price-features">
        <li>Retires this periodic activation reminder</li>
        <li>Activates on 1 machine</li>
        <li>Directly supports ongoing development</li>
        <li>Agent team templates &amp; sharing</li>
        <li>Search chat history</li>
        <li>Lifetime license with updates &amp; email support</li>
      </ul>
      <p style="font-size:11px;color:#484f58;margin-top:0;margin-bottom:12px;text-align:center;">Coral stays free either way &mdash; a license simply backs the project and stops this reminder.</p>
      <a href="{{STORE_URL}}" class="price-btn price-btn-primary" target="_blank">Buy a License</a>
    </div>
  </div>

  <div class="activate-card">
    <h3>Already have a license key?</h3>
    <p class="subtitle">Enter your key below to activate Coral.</p>
    <form id="activate-form">
      <div class="input-group">
        <input type="text" id="license-key" placeholder="XXXXX-XXXXX-XXXXX-XXXXX" autocomplete="off" spellcheck="false" required>
      </div>
      <button type="submit" id="submit-btn" class="activate-btn">Activate License</button>
    </form>
    <div class="error" id="error-msg"></div>
    <div class="success" id="success-msg"></div>
    <a href="/?skip_activation=1" class="skip-btn">Continue with the free version</a>
    <p style="font-size:12px;color:#484f58;margin-top:16px;">Need help? <a href="https://coralai.ai/support.html" target="_blank" style="color:#58a6ff;">Contact Support</a></p>
  </div>

  <div class="footer-link">
    <a href="https://coralai.ai/support.html" target="_blank">Contact Support</a>
  </div>
</div>
<script>
  const form = document.getElementById('activate-form');
  const input = document.getElementById('license-key');
  const btn = document.getElementById('submit-btn');
  const errorEl = document.getElementById('error-msg');
  const successEl = document.getElementById('success-msg');

  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    const key = input.value.trim();
    if (!key) return;

    btn.disabled = true;
    btn.textContent = 'Activating...';
    errorEl.style.display = 'none';
    successEl.style.display = 'none';

    try {
      const resp = await fetch('/api/license/activate', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ license_key: key }),
      });
      const data = await resp.json();
      if (data.valid) {
        successEl.textContent = 'License activated! Redirecting...';
        successEl.style.display = 'block';
        setTimeout(() => window.location.reload(), 1000);
      } else {
        errorEl.textContent = data.error || 'Invalid license key.';
        errorEl.style.display = 'block';
        btn.disabled = false;
        btn.textContent = 'Activate License';
      }
    } catch (err) {
      errorEl.textContent = 'Network error. Please try again.';
      errorEl.style.display = 'block';
      btn.disabled = false;
      btn.textContent = 'Activate License';
    }
  });
</script>
</body>
</html>`

func isLocalhostOrigin(origin string) bool {
	// Match http(s)://localhost or http(s)://127.0.0.1, optionally followed
	// by a port (:1234) or path (/...). Rejects tricky subdomains like
	// localhost.evil.com by requiring the character after the hostname to be
	// ':', '/', or end-of-string.
	for _, prefix := range []string{
		"http://localhost", "https://localhost",
		"http://127.0.0.1", "https://127.0.0.1",
	} {
		if len(origin) >= len(prefix) && origin[:len(prefix)] == prefix {
			if len(origin) == len(prefix) {
				return true
			}
			next := origin[len(prefix)]
			if next == ':' || next == '/' {
				return true
			}
		}
	}
	return false
}
