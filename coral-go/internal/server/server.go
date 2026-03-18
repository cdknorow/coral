// Package server provides the HTTP server, router, and middleware for Coral.
package server

import (
	"embed"
	"html/template"
	"io/fs"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"github.com/cdknorow/coral/internal/config"
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
	cfg       *config.Config
	db        *store.DB
	router    chi.Router
	indexTmpl *template.Template
	diffTmpl  *template.Template
}

// templateData is passed to Go templates during rendering.
type templateData struct {
	CoralRoot string
}

// New creates a Server with all routes registered.
func New(cfg *config.Config, db *store.DB) *Server {
	s := &Server{
		cfg: cfg,
		db:  db,
	}

	// Parse Go templates from embedded FS
	indexTmpl, err := template.ParseFS(templateFS,
		"frontend/templates/index.html",
		"frontend/templates/includes/sidebar.html",
		"frontend/templates/includes/modals.html",
		"frontend/templates/includes/views/live_session.html",
		"frontend/templates/includes/views/history_session.html",
		"frontend/templates/includes/views/message_board.html",
	)
	if err != nil {
		log.Printf("Warning: failed to parse index template: %v (serving placeholder)", err)
	}
	s.indexTmpl = indexTmpl

	diffTmpl, err := template.ParseFS(templateFS, "frontend/templates/diff.html")
	if err != nil {
		log.Printf("Warning: failed to parse diff template: %v (serving placeholder)", err)
	}
	s.diffTmpl = diffTmpl

	s.router = s.buildRouter()
	return s
}

// Router returns the configured chi.Router for use with http.Server.
func (s *Server) Router() chi.Router {
	return s.router
}

func (s *Server) buildRouter() chi.Router {
	r := chi.NewRouter()

	// Middleware
	r.Use(middleware.Recoverer)
	r.Use(middleware.RealIP)
	r.Use(cors.Handler(cors.Options{
		AllowOriginFunc: func(r *http.Request, origin string) bool {
			// Allow localhost origins only (matches Python CORS config)
			return isLocalhostOrigin(origin)
		},
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"*"},
		AllowCredentials: true,
	}))

	// ── API Routes ──────────────────────────────────────────────
	sessHandler := routes.NewSessionsHandler(s.db, s.cfg)
	sysHandler := routes.NewSystemHandler(s.db, s.cfg)
	histHandler := routes.NewHistoryHandler(s.db, s.cfg)
	schedHandler := routes.NewScheduleHandler(s.db, s.cfg)
	whHandler := routes.NewWebhooksHandler(s.db, s.cfg)
	themeHandler := routes.NewThemesHandler(s.cfg)

	// Live sessions
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
	r.Post("/api/sessions/launch", sessHandler.Launch)
	r.Post("/api/sessions/launch-team", sessHandler.LaunchTeam)

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

	// WebSocket
	r.Get("/ws/coral", sessHandler.WSCoral)
	r.Get("/ws/terminal/{name}", sessHandler.WSTerminal)

	// System / settings
	r.Get("/api/settings", sysHandler.GetSettings)
	r.Put("/api/settings", sysHandler.PutSettings)
	r.Get("/api/system/status", sysHandler.Status)
	r.Get("/api/filesystem/list", sysHandler.ListFilesystem)
	r.Post("/api/indexer/refresh", sysHandler.RefreshIndexer)

	// Tags
	r.Get("/api/tags", sysHandler.ListTags)
	r.Post("/api/tags", sysHandler.CreateTag)
	r.Delete("/api/tags/{tagID}", sysHandler.DeleteTag)
	r.Post("/api/sessions/{sessionID}/tags", sysHandler.AddSessionTag)
	r.Delete("/api/sessions/{sessionID}/tags/{tagID}", sysHandler.RemoveSessionTag)

	// History
	r.Get("/api/sessions/history", histHandler.ListSessions)
	r.Get("/api/sessions/{sessionID}/notes", histHandler.GetSessionNotes)
	r.Put("/api/sessions/{sessionID}/notes", histHandler.SaveSessionNotes)
	r.Post("/api/sessions/{sessionID}/resummarize", histHandler.Resummarize)
	r.Get("/api/sessions/{sessionID}/tags", histHandler.GetSessionTags)
	r.Get("/api/sessions/{sessionID}/git", histHandler.GetSessionGit)
	r.Get("/api/sessions/{sessionID}/events", histHandler.GetSessionEvents)
	r.Get("/api/sessions/{sessionID}/tasks", histHandler.GetSessionTasks)

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

	// ── Static Files ────────────────────────────────────────────
	staticSub, err := fs.Sub(staticFS, "frontend/static")
	if err != nil {
		log.Fatalf("Failed to embed static files: %v", err)
	}
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))

	// ── Dashboard SPA ───────────────────────────────────────────
	r.Get("/", s.serveIndex)
	r.Get("/diff", s.serveDiff)

	return r
}

func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
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

func (s *Server) serveDiff(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if s.diffTmpl == nil {
		w.Write([]byte(`<!DOCTYPE html><html><body>Template not loaded</body></html>`))
		return
	}
	if err := s.diffTmpl.Execute(w, nil); err != nil {
		log.Printf("Error rendering diff template: %v", err)
		http.Error(w, "Template render error", http.StatusInternalServerError)
	}
}

func isLocalhostOrigin(origin string) bool {
	// Match http(s)://localhost:PORT or http(s)://127.0.0.1:PORT
	if len(origin) < 16 {
		return false
	}
	for _, prefix := range []string{
		"http://localhost", "https://localhost",
		"http://127.0.0.1", "https://127.0.0.1",
	} {
		if len(origin) >= len(prefix) && origin[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}
