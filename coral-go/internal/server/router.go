package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/subgentic/coral/internal/cloud"
	"io/fs"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/cors"

	"github.com/subgentic/coral/internal/auth"
	"github.com/subgentic/coral/internal/background"
	"github.com/subgentic/coral/internal/cliauth"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/httputil"
	"github.com/subgentic/coral/internal/logexit"
	"github.com/subgentic/coral/internal/proxy"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/server/routes"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/pkg/frontend"
)

// The router is built in this order; registration and wiring order are as they were in one
// function (#3825 F2), and buildRouter in server.go calls these in sequence.

// routeHandlers are the handlers more than one register function uses.
type routeHandlers struct {
	sess     *routes.SessionsHandler
	sys      *routes.SystemHandler
	hist     *routes.HistoryHandler
	sched    *routes.ScheduleHandler
	workflow *routes.WorkflowHandler
	theme    *routes.ThemesHandler
	teams    *routes.TeamsHandler // set by registerSystem, used by registerBoard
}

// useMiddleware: the middleware chain, in order: recovery, headers, the Hub guard, CORS, auth and the identity checks.
func (s *Server) useMiddleware(r *chi.Mux) {
	// Middleware
	r.Use(recoverer)            // logs through coral.log's redaction (#3763)
	r.Use(routes.BodyLimit)     // every request body capped (#3998)
	r.Use(routes.WriteDeadline) // a reply to a client that never reads it is cut after 60 s, per write (#4602)
	r.Use(httputil.GzipAPI)     // /api/ JSON over 1 KB, gzipped for a client that accepts it (#3724)
	r.Use(noStoreAPI)
	r.Use(s.buildHeader)

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
	// The auth routes are made here: Sign in with Coral Hub's guard (#3966) runs right after the
	// sign-in middleware, on every route.
	s.authRoutes = auth.NewRoutes(s.keyStore)
	if s.keyStore != nil {
		r.Use(auth.Middleware(s.keyStore))
		// Ending a browser session closes its open WebSockets too (#3966 (c)).
		r.Use(s.keyStore.SessionRequests)
		s.hubSignin = s.newHubSignin(s.authRoutes)
		r.Use(s.hubSignin.HubSessionGuard)
	}

	// Debug request logger (session/ws calls only, when CORAL_DEBUG=1)
	r.Use(routes.DebugRequestLogger)
	// First of the board's guards: a body's identity keys are in one spelling, so every guard
	// below reads what the handler decodes (#4233).
	r.Use(routes.CanonicalIdentityKeys)
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
	r.Use(routes.BoardResolveMemo) // one board resolve per request, shared by the identity layer and the resolver below (#4303)
	r.Use(s.identity.Middleware)
	// One write budget per agent session for the routes an agent can loop on (#4602): a burst of 60, then 240 a minute.
	r.Use(routes.AgentWriteLimit())
	// Nobody but the Operator writes as the Operator; after the identity binding, so its 403 stands (#4207).
	r.Use(routes.RejectOperatorImpersonation)
	// A board's name in /api/board/<key>/... resolves to its id (#3536), after the identity
	// checks so only an authenticated caller sees an ambiguity's candidates.
	r.Use(routes.BoardKeyResolver(s.boardStore))
}

// registerAuth: the health check and the sign-in routes.
func (s *Server) registerAuth(r *chi.Mux) {
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
	authRoutes := s.authRoutes
	authRoutes.SetStrictLocal(s.cfg.StrictLocalAuth) // /auth here shows the locked page (#3418)
	// The key routes need the Operator the identity layer verified, strict or not (#4290).
	authRoutes.SetOperatorCheck(func(r *http.Request) bool {
		a, ok := routes.ActorOf(r.Context())
		return ok && a.Kind == routes.ActorOperator && a.IsOperator()
	})
	r.Get("/auth", authRoutes.AuthPage)
	r.Post("/auth/key", authRoutes.ValidateKey)
	r.Post("/auth/logout", authRoutes.Logout)
	r.Get("/api/system/api-key", authRoutes.GetAPIKey)
	r.Post("/api/system/api-key/regenerate", authRoutes.RegenerateKey)
	r.Get("/api/system/auth-status", authRoutes.AuthStatus)
	// The password (#3949): never in Coral Hub's tunnel table; an agent's token gets none of
	// them (operatorOnlyRoutes).
	authRoutes.SetCLIVerified(cliauth.Verified)
	authRoutes.SetAfterReset(func() { // a reset also replaces the command-line token (#4041)
		if s.cloudHandler != nil {
			s.cloudHandler.RotateCLIToken()
		}
	})
	peerUser := func(r *http.Request) (bool, bool) {
		if s.cloudHandler == nil {
			return false, false
		}
		return s.cloudHandler.SameUser(r)
	}
	authRoutes.SetPeerUser(peerUser)
	s.keyStore.SetPeerUser(peerUser) // a sign-in link opened by another OS user is refused (#3970)
	r.Post("/auth/password", authRoutes.SignInWithPassword)
	r.Post("/auth/password/create", authRoutes.CreatePassword)
	r.Get("/api/auth/setup-code", authRoutes.GetSetupCode)
	r.Post("/api/auth/password/set", authRoutes.SetPassword)
	r.Post("/api/auth/password/change", authRoutes.ChangePassword)
	r.Post("/api/auth/password/reset", authRoutes.ResetPassword)
	r.Get("/api/auth/sessions", authRoutes.Sessions)
	r.Post("/api/auth/sessions/sign-out-others", authRoutes.SignOutOthers)
	r.Delete("/api/auth/sessions/{id}", authRoutes.RevokeSessionByID)
	// Sign in with Coral Hub (#3966): never in the tunnel's table; agent tokens are refused.
	if hs := s.hubSignin; hs != nil {
		r.Post("/auth/hub/start", hs.Start)
		r.Get("/auth/hub/callback", hs.Callback)
		r.Post("/auth/hub/link", hs.Link)
		r.Get("/auth/hub/link/status", hs.LinkStatus)
		r.Get("/api/auth/hub-signin", hs.Settings)
		r.Put("/api/auth/hub-signin", hs.SetSettings)
		r.Post("/api/auth/hub-signin/dismiss", hs.DismissOffer)
		r.Get("/api/auth/hub-origins", hs.GetOrigins)
		r.Put("/api/auth/hub-origins", hs.SetOrigins)
		s.goBackground(hs.Run)
	}
}

// newRouteHandlers makes the API handlers and seeds what they serve.
func (s *Server) newRouteHandlers() *routeHandlers {
	// ── API Routes ──────────────────────────────────────────────
	sessHandler := routes.NewSessionsHandler(s.db, s.cfg, s.backend, s.terminal, s.boardStore)
	s.sessHandler = sessHandler
	// The automated-input gate reads agents' state through it (#3431).
	sessHandler.InstallAutomatedInputHooks()
	// Coral's own data folder (wherever --home or CORAL_DATA_DIR put it) is never served by Browse or a file read (#4531).
	routes.ProtectDataDir(s.cfg.CoralDir())
	sysHandler := routes.NewSystemHandler(s.db, s.cfg)
	sysHandler.SetTerminal(s.terminal)
	if s.boardStore != nil {
		sysHandler.SetBoardAuthor(func(ctx context.Context, project string, id int64) (string, bool, error) {
			m, err := s.boardStore.GetMessageByID(ctx, project, id)
			if errors.Is(err, sql.ErrNoRows) {
				return "", false, nil
			}
			if err != nil {
				return "", false, err
			}
			return m.JobTitle, true, nil
		})
	}
	s.systemHandler = sysHandler
	histHandler := routes.NewHistoryHandler(s.db, s.cfg, s.boardStore)
	s.historyHandler = histHandler
	schedHandler := routes.NewScheduleHandler(s.db, s.cfg)
	workflowHandler := routes.NewWorkflowHandler(s.db, s.cfg)
	s.workflowHandler = workflowHandler
	themeHandler := routes.NewThemesHandler(s.cfg)
	themeHandler.SetDB(s.db)
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
	return &routeHandlers{sess: sessHandler, sys: sysHandler, hist: histHandler, sched: schedHandler, workflow: workflowHandler, theme: themeHandler}
}

// registerSessions: the live-session, agent task, note, event and WebSocket routes.
func (s *Server) registerSessions(r *chi.Mux, h *routeHandlers) {
	sessHandler := h.sess
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
	r.Get("/api/sessions/live/{name}/attachment/{ref}", sessHandler.ChatAttachment) // a pasted image of the chat, for its chip (#5032)
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
	r.Post("/api/sessions/live/{name}/model", sessHandler.SwitchModel) // the live model and thinking-level switch (#4969)
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
	r.Post("/api/sessions/live/{sessionID}/refresh-hooks", sessHandler.RefreshHooks) // #3665
	r.Get("/api/system/hooks-status", sessHandler.HooksStatus)                       // #3665
	// #3636: the start's wake of the agents running when a pty Coral stopped.
	r.Get("/api/system/auto-wake", sessHandler.AutoWakeStatus)
	r.Post("/api/system/auto-wake/dismiss", sessHandler.DismissAutoWake)
	r.Post("/api/sessions/live/{sessionID}/archive", sessHandler.ArchiveSession)
	r.Post("/api/sessions/live/{sessionID}/unarchive", sessHandler.UnarchiveSession)

	// Agent tasks
	sessHandler.RegisterAgentArtifacts(r)
	sessHandler.RegisterArtifactComments(r) // #5176
	sessHandler.RegisterTerminalLogs(r)
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
	r.Post("/api/sessions/live/{name}/pending-tool", sessHandler.SetPendingTool)
	r.Post("/api/sessions/live/{name}/answer-prompt", sessHandler.AnswerPrompt)
	r.Get("/api/sessions/live/{name}/events/counts", sessHandler.EventCounts)
	r.Delete("/api/sessions/live/{name}/events", sessHandler.ClearEvents)
	r.Post("/api/sessions/live/{name}/token-usage", sessHandler.RecordTokenUsage)

	// WebSocket
	r.Get("/ws/coral", sessHandler.WSCoral)
	r.Get("/ws/terminal/{name}", sessHandler.WSTerminal)
}

// registerCloud: the Coral Hub connector and the system routes the cloud handler serves.
func (s *Server) registerCloud(r *chi.Mux) {
	// Cloud Hub connector settings (never reachable through the tunnel)
	s.cloudHandler = routes.NewCloudHandler(store.NewSessionStore(s.db), s.cfg.CoralDir())
	s.cloudHandler.OfferAtLaunch = s.cfg.OffersCloudConnect()
	r.Get("/api/cloud/status", s.cloudHandler.GetStatus)
	r.Post("/api/cloud/connect", s.cloudHandler.Connect)
	r.Post("/api/cloud/connect/cancel", s.cloudHandler.CancelConnect)
	r.Post("/api/cloud/disconnect", s.cloudHandler.Disconnect)
	r.Post("/api/cloud/rekey", s.cloudHandler.Rekey) // #3698
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
	r.Put("/api/system/identity-actor", s.cloudHandler.SetIdentityActor)
	routes.LoadIdentityActor(context.Background(), store.NewSessionStore(s.db))
	r.Put("/api/cloud/max-mode", s.cloudHandler.SetMaxMode)
	r.Put("/api/cloud/allow-auto-accept", s.cloudHandler.SetAllowHubAutoAccept)
	r.Put("/api/cloud/redact-tokens", s.cloudHandler.SetRedactTokens)
	r.Put("/api/cloud/hub-relaunch", s.cloudHandler.SetHubRelaunch)    // #5280
	r.Put("/api/cloud/hub-cross-team", s.cloudHandler.SetHubCrossTeam) // #5572
	// Coral Hub's relaunch into a staged update (#5280): 404 until the feature is switched on and the updater exists.
	// Where the owner routes read the owner's label recorded at enrolment (#5307).
	routes.SetHubOwnerLabel(store.NewCloudStore(s.db, store.NewRemoteServerStore(s.db, s.cfg.CoralDir())).OwnerUser)
	upd := routes.NewUpdateRelaunchHandler(store.NewSessionStore(s.db), s.cfg.CoralDir(), func() string { return routes.TerminalKind(s.terminal) }, routes.BuildRelaunchUpdater())
	r.Get("/api/system/update", upd.Status)
	r.Post("/api/system/update/relaunch", upd.Relaunch)
	r.Post("/api/cloud/redeem", s.cloudHandler.Redeem)
	r.Post("/api/cloud/cli-hello", s.cloudHandler.CLIHello)
}

// registerSystem: settings, teams, token usage, tags, history, schedules, workflows, themes, templates, uploads, views and (in hub mode) the remote servers.
func (s *Server) registerSystem(r *chi.Mux, h *routeHandlers) {
	sessHandler, sysHandler, histHandler := h.sess, h.sys, h.hist
	schedHandler, workflowHandler, themeHandler := h.sched, h.workflow, h.theme
	// System / settings
	r.Get("/api/settings", sysHandler.GetSettings)
	r.Put("/api/settings", sysHandler.PutSettings)
	r.Get("/api/custom-models", sysHandler.GetCustomModels)
	r.Put("/api/custom-models", sysHandler.PutCustomModels)
	r.Put("/api/settings/git-diff-mode", sysHandler.PutGitDiffMode)
	r.Get("/api/system/privacy", sysHandler.GetPrivacyStatus)
	// The models each agent CLI offers (#4065): the cached list, and the Operator's Refresh.
	r.Get("/api/agents/models", sysHandler.GetAgentCLIModels)
	r.Post("/api/agents/models/refresh", sysHandler.RefreshAgentCLIModels)
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
	// Save for Later (#4510): the Operator's bookmarks on chat and board messages.
	r.Get("/api/saved", sysHandler.SavedList)
	r.Post("/api/saved", sysHandler.SavedAdd)
	r.Patch("/api/saved/{id}", sysHandler.SavedUpdate)
	r.Delete("/api/saved/{id}", sysHandler.SavedDelete)

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
	r.Get("/api/upload/{name}", routes.GetUpload) // an uploaded image, for the chat chips (#5028)
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
	h.teams = teamsHandler
}

// registerBoard: the board, board remotes and one-shot task routes, and the board handler's wiring.
func (s *Server) registerBoard(r *chi.Mux, h *routeHandlers) {
	sessHandler, teamsHandler := h.sess, h.teams
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
	boardHandler.SetLiveSessionLookup(routes.LiveSessionLookup(store.NewSessionStore(s.db)))
	boardHandler.SetTaskArtifactWriter(s.cfg.CoralDir(), sessHandler.PersistTaskChanges)
	sessStore := store.NewSessionStore(s.db)
	boardHandler.SetAllowlistFlag(routes.AllowlistEnabled(sessStore))
	boardHandler.SetHubCrossTeamLock(routes.HubCrossTeamLockFromSettings(sessStore.GetSettings))
	boardHandler.SetLinksSeen(routes.LinksSeenFromSettings(sessStore.GetSettings), routes.MarkLinksSeen(sessStore.SetSetting))
	boardHandler.SetOwnServerID(func(ctx context.Context) (string, error) {
		c, err := store.NewCloudStore(s.db, store.NewRemoteServerStore(s.db, s.cfg.CoralDir())).Get(ctx)
		if err != nil || c == nil {
			return "", err
		}
		return c.ServerID, nil
	})
	boardHandler.SetAutoAttacher(routes.NewAutoAttacher(s.cfg.CoralDir(), routes.AutoAttachEnabled(sessStore), routes.SessionDirs(sessStore)))
	s.goBackground(boardHandler.RecoverTaskNotifications)
	s.goBackground(func(ctx context.Context) {
		if err := boardHandler.RestoreSubscriberReminders(ctx); err != nil {
			log.Printf("restore subscriber reminders: %v", err)
		}
	})
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
	autoAccept.Start(s.bgCtx)
	s.goBackground(autoAccept.Run)
	s.goBackground(sessHandler.RecoverPersonalTaskNotifications)
	// Sessions launched before their CLI version was recorded get it now (#3573).
	s.goBackground(sessHandler.BackfillCLIVersions)
	notifHandler := routes.NewNotificationHandler(notifStore)
	r.Post("/api/notifications", notifHandler.Create)

	// Board routes need an open board store; when messageboard.db could not be opened they answer 503
	// instead of dereferencing a nil store (#4924). Routes that work without a board stay outside this
	// group: team availability self-guards on a nil board, and session artifacts use the artifact store.
	r.Group(func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if s.boardStore == nil {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = w.Write([]byte(`{"error":"board unavailable","code":"board_unavailable"}`))
					return
				}
				next.ServeHTTP(w, req)
			})
		})
		r.Get("/api/board/projects", boardHandler.ListProjects)
		r.Post("/api/board/{project}/subscribe", boardHandler.Subscribe)
		r.Delete("/api/board/{project}/subscribe", boardHandler.Unsubscribe)
		r.Post("/api/board/{project}/messages", boardHandler.PostMessage)
		r.Get("/api/board/{project}/messages", boardHandler.ReadMessages)
		r.Get("/api/board/{project}/messages/all", boardHandler.ListAllMessages)
		r.Get("/api/board/{project}/messages/search", boardHandler.SearchMessages)
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
		r.Post("/api/board/{project}/external-artifacts", boardHandler.PostExternalArtifact)
		// To another Coral through Coral Hub (#3489); this computer only (not in the tunnel's table).
		r.Post("/api/board/{project}/relay-messages", boardHandler.PostRelayMessage)
		r.Get("/api/board/{project}/relay-targets", boardHandler.ListRelayTargets)
		r.Post("/api/board/{project}/hub-notice", boardHandler.PostHubNotice)
		// Coral Hub's full list of cross-team links (#5572): only through this computer's own connector, only from Coral Hub.
		r.Post("/api/cloud/cross-team-links", boardHandler.PostCrossTeamLinks)
		// Coral Hub's owner turns a team's two cross-team switches on or off (#5572): Launch tier, the owner marker, the local lock.
		r.Get("/api/board/{project}/cross-team/settings", boardHandler.GetCrossTeamSettingsForHub)
		r.Post("/api/board/{project}/cross-team/enable", boardHandler.HubCrossTeamEnable)
		r.Post("/api/board/{project}/cross-team/disable", boardHandler.HubCrossTeamDisable)
		r.Post("/api/board/{project}/cross-team/files/enable", boardHandler.HubCrossTeamFilesEnable)
		r.Post("/api/board/{project}/cross-team/files/disable", boardHandler.HubCrossTeamFilesDisable)
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
		r.Get("/api/board/{project}/working-mode", boardHandler.GetWorkingMode)
		r.Put("/api/board/{project}/working-mode", boardHandler.SetWorkingMode)
		r.Get("/api/board/{project}/cross-team-state", boardHandler.GetCrossTeamState)
		r.Get("/api/board/{project}/cross-team-candidates", boardHandler.GetCrossTeamCandidates)
		r.Get("/api/board/{project}/settings/log", boardHandler.GetCrossTeamLog)
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
		r.Post("/api/board/{project}/tasks/{taskID}/complete", boardHandler.CompleteTaskByID)
		r.Post("/api/board/{project}/tasks/{taskID}/submit-review", boardHandler.SubmitCompletionReview)
		r.Post("/api/board/{project}/tasks/{taskID}/release-review", boardHandler.ReleaseCompletionReview)
		r.Post("/api/board/{project}/tasks/{taskID}/review", boardHandler.DelegateReview) // #3725
		r.Post("/api/board/{project}/tasks/{taskID}/approve", boardHandler.ApproveReview) // #3725
		r.Post("/api/board/{project}/tasks/{taskID}/reject", boardHandler.RejectReview)   // #3725
		r.Post("/api/board/{project}/tasks/{taskID}/attach", boardHandler.AttachPatches)  // the merge queue (#5689)
		r.Post("/api/board/{project}/tasks/{taskID}/send-back", boardHandler.SendBack)    // the merge queue (#5689)
		r.Post("/api/board/{project}/tasks/{taskID}/cancel", boardHandler.CancelTaskByID)
		r.Patch("/api/board/{project}/tasks/{taskID}", boardHandler.UpdateTask)
		r.Patch("/api/board/{project}/tasks/{taskID}/amend", boardHandler.AmendTask)
		r.Post("/api/board/{project}/tasks/{taskID}/reassign", boardHandler.ReassignTask)
		r.Post("/api/board/{project}/tasks/{taskID}/nudge", boardHandler.NudgeTask)
		r.Post("/api/board/{project}/tasks/{taskID}/reply", boardHandler.ReplyToTask) // a message linked to the task, the creator mentioned (#5463)
		r.Post("/api/board/{project}/tasks/{taskID}/snooze", boardHandler.SnoozeTaskReminder)
		r.Post("/api/board/{project}/tasks/{taskID}/park", boardHandler.ParkTask)
		r.Delete("/api/board/{project}/tasks/{taskID}/park", boardHandler.ParkTask)
		r.Post("/api/board/{project}/health/ack", boardHandler.AckHealthItem)
		r.Post("/api/board/{project}/tasks/{taskID}/reminder", boardHandler.RemindTask)
		r.Delete("/api/board/{project}/tasks/{taskID}/reminder", boardHandler.RemindTask)
		r.Post("/api/board/{project}/reminder", boardHandler.RemindSubscriber)
		r.Delete("/api/board/{project}/reminder", boardHandler.RemindSubscriber)
		r.Post("/api/board/{project}/tasks/{taskID}/publish", boardHandler.PublishTask)
		r.Get("/api/board/{project}/tasks/{taskID}/cost", boardHandler.TaskLiveCost)
		r.Get("/api/board/{project}/tasks/{taskID}/changes.diff", boardHandler.TaskChangesDiff)
	})

	// These work without an open board, so they stay outside the board group (#4924): team
	// availability self-guards on a nil board store, and session artifacts come from the artifact store.
	r.Get("/api/board/{project}/status", sessHandler.TeamAvailability)
	r.Get("/api/session-artifacts", boardHandler.ListSessionArtifacts)

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
}

// registerStatic: static files, the API spec, agent docs, the manifest and the dashboard pages.
func (s *Server) registerStatic(r *chi.Mux) {
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
	// The bundled family (#4228): app.js's module graph and the shell's stylesheets as one file each, next to the unchanged files.
	var bundleFS fs.FS
	if s.staticVersion != "" && !frontend.BundleDisabled() {
		index, _ := fs.ReadFile(frontend.Templates, "templates/index.html")
		if b, err := frontend.BuildBundle(staticSub, index); err != nil {
			log.Printf("bundled dashboard unavailable, serving the original files: %v", err)
		} else {
			bundleFS = b.FS(staticSub)
			// The build id names the bytes served, not only the sources: the bundler, its options and
			// the stylesheet order change them with the tree untouched (#4320). It is ONE id for both
			// families, the pop-out's /static/b/ and the bundled /static/bb/, and the X-Coral-Build
			// header the update banner compares with, so they stay the same name for one build.
			s.staticVersion = bundleVersion(s.staticVersion, b)
			s.bundleBase = "/static/bb/" + s.staticVersion + "/"
		}
	}
	// The build id of the frontend this binary serves (it hashes every embedded static file and the bundle's bytes): two
	// servers that print the same id serve the same JavaScript, whatever tree they were built from (#4528).
	if s.staticVersion != "" {
		family := "bundled"
		if s.bundleBase == "" {
			family = "original files"
		}
		log.Printf("[STARTUP] dashboard build %s (%s)", s.staticVersion, family)
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
	if bundleFS != nil {
		r.Handle("/static/bb/*", versionedStaticHandlerAt("/static/bb/", bundleFS, s.staticVersion))
	}

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
		log.Print(agentDocsStartupLine(agentDocsSub)) // a synced copy: say what this build carries (agent_docs_digest.go)
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
}
