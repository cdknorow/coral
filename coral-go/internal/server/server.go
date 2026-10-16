// Package server provides the HTTP server, router, and middleware for Coral.
package server

import (
	"context"
	"errors"
	"github.com/cdknorow/coral/internal/cloud"
	"github.com/cdknorow/coral/internal/safego"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/cdknorow/coral/internal/auth"
	"github.com/cdknorow/coral/internal/background"
	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/logexit"
	"github.com/cdknorow/coral/internal/machinehealth"
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

// staticFS is the shared UI assets from pkg/frontend, so the Cloud Hub serves
// the identical frontend.
var staticFS = frontend.Static

// Server holds dependencies and exposes the HTTP router.
type Server struct {
	cloudHandler    *routes.CloudHandler
	cloudMgr        atomic.Pointer[cloud.Manager] // the connector, once SetCloud runs (#3966)
	hubSignin       *auth.HubSignin               // Sign in with Coral Hub (#3966)
	authRoutes      *auth.Routes                  // made in useMiddleware (the Hub sign-in guard needs it), routed in registerAuth
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

	// The server's own background work (recoveries, the auto-accept loop) runs on bgCtx and
	// is waited for by Close, before the board database closes (#3914).
	bgCtx     context.Context
	bgStop    context.CancelFunc
	bg        sync.WaitGroup
	closeOnce sync.Once
}

// remoteAgentHubConfig tunes the hub's remote poller. Zero values mean the
// production defaults; only in-package tests set it (to poll quickly).
var remoteAgentHubConfig background.RemoteAgentHubConfig

// templateData is passed to Go templates during rendering.
type templateData = frontend.PageData

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
	// connected_apps (OAuth secrets and tokens of a retired feature) is dropped, for real (#3628 F8).
	if n, err := store.DropConnectedApps(context.Background(), db); err != nil {
		log.Printf("[store] dropping the retired connected_apps table: %v", err)
	} else if n > 0 {
		log.Printf("[store] dropped the retired connected_apps table (%d rows)", n-1)
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
	// Browser sign-ins live in sessions.db, so they survive a restart (#3949).
	if db != nil {
		keyStore.SetSessionBackend(store.NewBrowserSessions(db))
		keyStore.ApplyRevokeMarker()
	}
	if p := os.Getenv("CORAL_INITIAL_PASSWORD_FILE"); p != "" {
		keyStore.ApplyInitialPasswordFile(p)
	}
	keyStore.SetupCode() // no password yet: the setup code goes to the console (never coral.log)
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
			auth.SetRequireAuthOnLoopback(config.Truthy(st[auth.RequireAuthOnLoopbackSetting]))
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
	s.bgCtx, s.bgStop = context.WithCancel(context.Background())

	// Parse Go templates from embedded FS
	indexTmpl, err := frontend.ParseIndex()
	if err != nil {
		log.Printf("Warning: failed to parse index template: %v (serving placeholder)", err)
	}
	s.indexTmpl = indexTmpl

	s.router = s.buildRouter()

	return s
}

// goBackground runs f on the server's background context, tracked for Close.
func (s *Server) goBackground(f func(ctx context.Context)) {
	s.bg.Add(1)
	go func() {
		defer safego.Recover("server/server:189")
		defer s.bg.Done()
		f(s.bgCtx)
	}()
}

// closeWait bounds how long Close waits for the background work to stop.
const closeWait = 5 * time.Second

// Close stops the server's own background work and closes its board database (#3914). The
// sessions database belongs to the caller, which closes it after this. Call it after the HTTP
// server has stopped; it is safe to call more than once.
func (s *Server) Close() {
	s.closeOnce.Do(func() {
		if s.bgStop != nil {
			s.bgStop()
			done := make(chan struct{})
			go func() {
				defer safego.Recover("server/server:206")
				s.bg.Wait()
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(closeWait):
				log.Printf("[server] background work still running after %s; closing the board database anyway", closeWait)
			}
		}
		if s.boardStore != nil {
			if err := s.boardStore.Close(); err != nil {
				log.Printf("[server] closing the board database: %v", err)
			}
		}
	})
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
	s.cloudMgr.Store(m)
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
		auth.RemoveLoginTokenFile(s.cfg.CoralDir()) // no token to give: none left from a run before
		return "", nil
	}
	return s.keyStore.WriteLoginTokenFile(s.cfg.CoralDir())
}

// RemoveLoginToken removes <data>/login_token at a clean shutdown (#3628 F7).
func (s *Server) RemoveLoginToken() {
	auth.RemoveLoginTokenFile(s.cfg.CoralDir())
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
	s.boardHandler.LoadPaused(ctx) // the Operator's pauses survive a restart (#3668)
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
	s.useMiddleware(r)
	s.registerAuth(r)
	h := s.newRouteHandlers()
	s.registerSessions(r, h)
	s.registerCloud(r)
	s.registerSystem(r, h)
	s.registerBoard(r, h)
	s.registerStatic(r)
	if err := routes.ValidateGuardedRoutes(r, s.cfg.HubMode); err != nil {
		panic(err) // an Operator-only route the guard could never match (#3670)
	}
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
//   - script-src 'self' (#3623 stage 1, #3645 stage 2; Coral Hub's copy has the same): scripts
//     only from this server, and no inline script or on* handler runs. Controls name a data-on-*
//     action that actions.js runs (tests/frontend/action_census.test.mjs keeps the UI free of
//     inline handlers). No eval: nothing in the dashboard or its vendored libraries uses eval or
//     new Function.
//   - form-action 'self': a form can only post to this server.
//   - media-src 'self' blob: data: (Security's SHOULD on #3622): audio and video only from this
//     server, so remote media can't be fetched from the page (agent content can't carry media
//     tags at all; this is the second layer).
const dashboardCSP = "script-src 'self'; form-action 'self'; media-src 'self' blob: data:; " +
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
		MaxAge:   30 * 24 * 60 * 60, // a flag, not an identity: if it lapses the purge runs once more (#3682)
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

// noStoreAPI marks every /api/ and /ws response Cache-Control: no-store unless its handler says
// otherwise (#3682, the browser storage audit's finding 4): board content, file previews,
// terminal captures and the rest of Coral's JSON are never kept by a browser or a shared cache.
// It is set before the handler runs, so a handler that chooses its own caching (content-addressed
// artifacts, an ETag-validated reply) replaces it.
func noStoreAPI(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := r.URL.Path; strings.HasPrefix(p, "/api/") || p == "/ws" || strings.HasPrefix(p, "/ws/") {
			w.Header().Set("Cache-Control", "no-store")
			// Another page can't load an answer as an image or a script, and a browser never
			// guesses another type for it (#4032, Security's #4031 finding 2).
			w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
			w.Header().Set("X-Content-Type-Options", "nosniff")
		}
		next.ServeHTTP(w, r)
	})
}

// buildHeaderName carries this server's static build (the /static/b/<build>/ id) on every /api/ answer.
// A dashboard tab left open across a server update still runs the old scripts; it compares the header
// with the build it loaded from and offers a reload (update_banner.js, #3751). Nothing is sent while the
// build is unknown, and it is the same id the page's own URLs carry, so it says nothing new.
const buildHeaderName = "X-Coral-Build"

func (s *Server) buildHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v := s.staticVersion; v != "" && strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set(buildHeaderName, v)
		}
		next.ServeHTTP(w, r)
	})
}
