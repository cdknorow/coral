// Package startup provides shared server bootstrap logic used by all Coral
// entry points (coral, coral-tray, launch-coral). It handles database setup,
// terminal backend selection, HTTP server creation, and background service
// wiring so that each cmd/ binary doesn't have to duplicate this code.
package startup

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/cdknorow/coral/internal/agent"
	"github.com/cdknorow/coral/internal/background"
	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/oauth"
	"github.com/cdknorow/coral/internal/proxy"
	"github.com/cdknorow/coral/internal/ptymanager"
	"github.com/cdknorow/coral/internal/server"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/tmux"
)

const devStartupMarker = "DEV-TMUX-DIAGNOSTIC-2026-06-03-01"

// Options configures optional behaviors that differ between entry points.
type Options struct {
	// BackendType is "pty" or "tmux". Default is "tmux".
	BackendType string

	// OnServerError is called when ListenAndServe fails (non-ErrServerClosed).
	// If nil, log.Printf is used.
	OnServerError func(err error)
}

// RunningServer holds all resources created during startup.
// Callers use it to access the HTTP server for shutdown and the backend for cleanup.
type RunningServer struct {
	HTTPServer *http.Server
	Server     *server.Server
	DB         *store.DB
	Backend    ptymanager.TerminalBackend
}

// Shutdown gracefully shuts down the HTTP server and cleans up resources.
func (rs *RunningServer) Shutdown(timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := rs.HTTPServer.Shutdown(ctx); err != nil {
		log.Printf("Shutdown error: %v", err)
	}
	if rs.Backend != nil {
		rs.Backend.Close()
	}
}

// Close closes the database connection.
func (rs *RunningServer) Close() {
	if rs.DB != nil {
		rs.DB.Close()
	}
}

// Start opens the database, selects the terminal backend, creates the HTTP
// server, wires up all background services, and starts listening. The HTTP
// server runs in a background goroutine; callers should wait on ctx.Done()
// then call RunningServer.Shutdown().
func Start(ctx context.Context, cfg *config.Config, opts Options) (*RunningServer, error) {
	if opts.BackendType == "" {
		opts.BackendType = "tmux"
	}
	exe, exeErr := os.Executable()
	log.Printf("[STARTUP] %s version=%q tier=%s backend=%s pid=%d exe=%q exe_err=%v path=%q shell=%q coral_tmux_bin=%q",
		devStartupMarker,
		config.Version,
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
	if err := os.MkdirAll(coralDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create data directory %s: %w", coralDir, err)
	}

	// Ensure DB parent directory exists (may differ from coralDir if overridden)
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create database directory: %w", err)
	}

	// Check if tmux is available when using tmux backend. We don't fail
	// startup if it's missing — the dashboard still loads and the UI shows
	// install instructions. Agent launch will fail until tmux is installed.
	if opts.BackendType == "tmux" {
		if _, ok := tmux.IsAvailable(); !ok {
			log.Printf("[startup] tmux not found — agents cannot be launched until tmux is installed (brew install tmux)")
		}
	}

	// Bind the port now and keep the listener open to avoid a TOCTOU race
	// (another process grabbing the port between check and serve).
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("port %d is already in use: %w", cfg.Port, err)
	}

	// Open database
	db, err := store.Open(cfg.DBPath)
	if err != nil {
		ln.Close()
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Apply terminal_replay_bytes setting from user_settings before any
	// backend touches the replay buffer size.
	applyReplayBytesFromSettings(ctx, db)

	// Select terminal backend and agent runtime
	backend, agentRT, terminal := selectBackend(opts.BackendType, cfg.LogDir, cfg.CoralDir())

	// Build the HTTP server
	srv := server.New(cfg, db, backend, terminal)

	// Reconcile orphaned live sessions: if the app was killed without
	// cleanly sleeping sessions, they remain in live_sessions with
	// is_sleeping=0 but no actual process running. Detect these and
	// mark them as sleeping so the user can wake them. Also wakes
	// sessions that are marked sleeping but still running (e.g. tmux
	// survived a server restart).
	reconcileOrphanedSessions(ctx, db, agentRT)
	reconcileOrphanedTeams(ctx, db)

	// Restore board pause state AFTER reconciliation so it reflects
	// the final sleeping state (including recovered sessions).
	srv.RestoreSleepingBoards()

	httpServer := &http.Server{
		Handler:           srv.Router(),
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 10 * time.Second, // Prevents slowloris attacks
		WriteTimeout:      0,                // Disabled for WebSocket/SSE
		IdleTimeout:       60 * time.Second,
	}

	// Start all background services
	startBackgroundServices(ctx, db, cfg, srv, agentRT)

	// Start HTTP server in background goroutine using the already-bound listener
	onErr := opts.OnServerError
	if onErr == nil {
		onErr = func(err error) { log.Printf("[FATAL] Server error: %v", err) }
	}
	go func() {
		log.Printf("Coral dashboard: http://localhost:%d", cfg.Port)
		if err := httpServer.Serve(ln); err != nil && err != http.ErrServerClosed {
			onErr(err)
		}
	}()

	return &RunningServer{
		HTTPServer: httpServer,
		Server:     srv,
		DB:         db,
		Backend:    backend,
	}, nil
}

// reconcileOrphanedSessions checks all non-sleeping live sessions against
// the runtime to see if their processes are actually running. Any session
// that exists in the DB but has no running process is marked as sleeping
// so the user can wake it from the UI.
func reconcileOrphanedSessions(ctx context.Context, db *store.DB, agentRT background.AgentRuntime) {
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
		if err := ss.SetSessionSleeping(ctx, ls.SessionID, true); err != nil {
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

		// Clean up worktree if applicable
		if team.IsWorktree == 1 && team.WorkingDir != "" {
			// Find the parent repo via git rev-parse from the worktree itself
			cleanCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			repoOut, repoErr := exec.CommandContext(cleanCtx, "git", "-C", team.WorkingDir, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
			if repoErr == nil {
				repoPath := filepath.Dir(strings.TrimSpace(string(repoOut)))
				cmd := exec.CommandContext(cleanCtx, "git", "-C", repoPath, "worktree", "remove", "--force", team.WorkingDir)
				if out, err := cmd.CombinedOutput(); err != nil {
					log.Printf("[startup] failed to remove orphaned worktree %s: %s", team.WorkingDir, string(out))
				} else {
					log.Printf("[startup] cleaned up orphaned worktree: %s", team.WorkingDir)
				}
			} else {
				log.Printf("[startup] cannot find parent repo for worktree %s, removing directory", team.WorkingDir)
				os.RemoveAll(team.WorkingDir)
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
	tmuxBackend := ptymanager.NewTmuxBackend(tmuxClient, logDir)
	log.Println("Using tmux terminal backend")
	return tmuxBackend, background.NewTmuxRuntime(tmuxClient), ptymanager.NewTmuxSessionTerminal(tmuxClient)
}

// safeGo runs fn in a goroutine with panic recovery. If fn panics, the panic
// and stack trace are logged, and fn is restarted after a short delay. This
// prevents a single background service crash from taking down the entire
// server process. When the context is cancelled (normal shutdown), the
// goroutine exits without restarting.
func safeGo(ctx context.Context, name string, fn func()) {
	go func() {
		for {
			panicked := false
			func() {
				defer func() {
					if r := recover(); r != nil {
						panicked = true
						log.Printf("[CRASH] background service %q panicked: %v\n%s", name, r, debug.Stack())
					}
				}()
				fn()
			}()
			// Normal shutdown — don't restart.
			if ctx.Err() != nil {
				return
			}
			if !panicked {
				// fn returned without panic and context isn't done — unexpected.
				log.Printf("[WARN] background service %q returned unexpectedly", name)
			}
			log.Printf("[RESTART] background service %q restarting in 5s...", name)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
	}()
}

func startBackgroundServices(ctx context.Context, db *store.DB, cfg *config.Config, srv *server.Server, agentRT background.AgentRuntime) {
	gitStore := store.NewGitStore(db)
	webhookStore := store.NewWebhookStore(db)
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
		// Look up display names from live_sessions for stable board subscriber_id
		for i, a := range agents {
			if ls, err := sessStore.GetLiveSession(ctx, a.SessionID); err == nil && ls != nil && ls.DisplayName != nil {
				agents[i].DisplayName = *ls.DisplayName
			}
		}
		return agents, nil
	}

	// Git poller
	gitPoller := background.NewGitPoller(gitStore, agentRT, time.Duration(cfg.GitPollerIntervalS)*time.Second)
	safeGo(ctx, "git_poller", func() { gitPoller.Run(ctx) })

	// Session indexer
	scanners := []agent.HistoryScanner{
		&agent.ClaudeAgent{},
		&agent.GeminiAgent{},
		&agent.CodexAgent{},
	}
	indexer := background.NewSessionIndexer(
		sessStore, scanners,
		time.Duration(cfg.IndexerIntervalS)*time.Second,
		time.Duration(cfg.IndexerStartupDelayS)*time.Second,
	)
	safeGo(ctx, "session_indexer", func() { indexer.Run(ctx) })
	srv.SetIndexer(indexer)

	// Idle detector
	idleDetector := background.NewIdleDetector(taskStore, webhookStore, time.Duration(cfg.IdleDetectorIntervalS)*time.Second)
	idleDetector.SetSessionStore(sessStore)
	idleDetector.SetDiscoverFn(discoverFn)
	safeGo(ctx, "idle_detector", func() { idleDetector.Run(ctx) })

	// Webhook dispatcher
	webhookDispatcher := background.NewWebhookDispatcher(webhookStore, time.Duration(cfg.WebhookDispatcherIntervalS)*time.Second)
	safeGo(ctx, "webhook_dispatcher", func() { webhookDispatcher.Run(ctx) })

	// Job scheduler
	scheduler := background.NewJobScheduler(schedStore, 30*time.Second)
	launcher := background.NewAgentLauncher(agentRT, sessStore, cfg.Port)
	// Wire proxy session upstream callback for universal reroute
	if p := srv.Proxy(); p != nil {
		launcher.SetSessionUpstreamFn(p.SetSessionUpstream)
	}
	scheduler.SetLaunchFn(launcher.BuildSchedulerLaunchFn(schedStore))
	scheduler.SetSessionStore(sessStore)
	scheduler.SetRuntime(agentRT)
	scheduler.SetNextFireTimeFn(background.NextFireTime)
	safeGo(ctx, "job_scheduler", func() { scheduler.Run(ctx) })
	srv.SetScheduler(scheduler)

	// Board notifier
	boardNotifier := background.NewBoardNotifier(srv.BoardStore(), agentRT, time.Duration(cfg.BoardNotifierIntervalS)*time.Second)
	boardNotifier.SetDiscoverFn(discoverFn)
	if bh := srv.BoardHandler(); bh != nil {
		boardNotifier.SetIsPausedFn(bh.IsPaused)
		bh.SetNotifyFn(boardNotifier.NotifyNow)
	}
	boardNotifier.SeedFromDB(ctx)
	safeGo(ctx, "board_notifier", func() { boardNotifier.Run(ctx) })

	// Remote board poller
	remotePoller := background.NewRemoteBoardPoller(rbStore, agentRT, 30*time.Second)
	remotePoller.SetDiscoverFn(discoverFn)
	safeGo(ctx, "remote_board_poller", func() { remotePoller.Run(ctx) })

	// Batch summarizer — only runs if auto_summarize setting is enabled
	summarizeFn := background.BuildSummarizeFn(sessStore)
	settings, _ := sessStore.GetSettings(ctx)
	if settings["auto_summarize"] == "true" {
		batchSummarizer := background.NewBatchSummarizer(sessStore, summarizeFn)
		safeGo(ctx, "batch_summarizer", func() { batchSummarizer.Run(ctx) })
	}
	srv.SetSummarizeFn(summarizeFn)

	// Session reconciler — periodically detects crashed agents and marks them sleeping
	reconciler := background.NewSessionReconciler(sessStore, agentRT, 30*time.Second)
	safeGo(ctx, "session_reconciler", func() { reconciler.Run(ctx) })

	// Token poller — extracts token usage from Codex/Claude transcripts for cost tracking
	tokenUsageStore := store.NewTokenUsageStore(db)
	tokenPoller := background.NewTokenPoller(sessStore, tokenUsageStore, 30*time.Second)
	safeGo(ctx, "token_poller", func() { tokenPoller.Run(ctx) })

	// Wire proxy → token_usage table so proxy-captured tokens appear in the unified API
	if p := srv.Proxy(); p != nil {
		p.SetTokenUsageRecorder(func(ctx context.Context, rec *proxy.TokenUsageRecord) error {
			return tokenUsageStore.RecordUsage(ctx, &store.TokenUsage{
				SessionID:        rec.SessionID,
				AgentName:        rec.AgentName,
				AgentType:        rec.AgentType,
				InputTokens:      rec.InputTokens,
				OutputTokens:     rec.OutputTokens,
				CacheReadTokens:  rec.CacheReadTokens,
				CacheWriteTokens: rec.CacheWriteTokens,
				TotalTokens:      rec.InputTokens + rec.OutputTokens + rec.CacheReadTokens + rec.CacheWriteTokens,
				CostUSD:          rec.CostUSD,
				RecordedAt:       rec.RecordedAt,
				Source:           rec.Source,
			})
		})
	}

	// Workflow runner — executes multi-step workflows (shell + agent)
	wfStore := store.NewWorkflowStore(db)
	connAppStore := store.NewConnectedAppStore(db)
	oauthRegistry := oauth.NewRegistry()
	flowManager := oauth.NewFlowManager(oauthRegistry)
	wfRunner := background.NewWorkflowRunner(wfStore, launcher, agentRT, connAppStore, flowManager, cfg.CoralDir(), cfg.Host, cfg.Port)
	srv.SetWorkflowRunner(wfRunner)
	scheduler.SetWorkflowRunner(wfRunner, wfStore)

	log.Printf("Started 10 background services + workflow runner (git poller, indexer, idle detector, webhook dispatcher, scheduler, board notifier, remote board poller, batch summarizer, session reconciler, token poller)")
}
