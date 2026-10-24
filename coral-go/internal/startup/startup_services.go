package startup

import (
	"context"
	"github.com/subgentic/coral/internal/backendchoice"
	"github.com/subgentic/coral/internal/coralnames"
	"log"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/subgentic/coral/internal/agentprio"
	"github.com/subgentic/coral/internal/safego"

	"github.com/jmoiron/sqlx"
	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/background"
	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/proxy"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/secrets"
	"github.com/subgentic/coral/internal/server"
	"github.com/subgentic/coral/internal/server/routes"
	setreg "github.com/subgentic/coral/internal/settings"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/tmux"
)

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
	// Neither has to finish before the dashboard answers (#4129: with 20 agents they took ~440 ms
	// of every start), so they run beside the rest of startup:
	// - panes a tmux server from before #3437 started at normal priority are lowered (and their
	//   children), on our own socket only; they were running already, so a moment later is fine;
	// - how many agents only the default-socket merge finds is logged (#3635).
	safego.Go("startup: existing panes", func() {
		if n := tmuxClient.LowerExistingPanes(context.Background()); n > 0 {
			log.Printf("Lowered the priority of %d existing agent processes (agent_priority=%s)", n, agentprio.Level())
		}
		tmuxClient.LogDefaultSocketMerges(context.Background())
	})
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

// eventReader is the part of the task store that says what an agent last did.
type eventReader interface {
	GetLatestEventTypes(ctx context.Context, sessionIDs []string) (map[string][2]string, error)
	LatestStopFailure(ctx context.Context, sessionID string) (text string, ok bool, err error)
	SessionsWithAnyEvent(ctx context.Context, sessionIDs []string) (map[string]bool, error)
}

// lastEventOfRun is the type of the agent's last event and whether that event is a stop that was a failed turn. A run is recorded finished only on a stop that did not fail, so a read that fails is reported as a
// failed stop (or no event): an unknown result never becomes a success nobody saw (#6928).
func lastEventOfRun(ctx context.Context, r eventReader, sessionID string) (string, bool) {
	last, err := r.GetLatestEventTypes(ctx, []string{sessionID})
	if err != nil {
		return "", true
	}
	typ := last[sessionID][0]
	if typ == "" {
		// GetLatestEventTypes skips status, goal and confidence events: a session whose only events are those did start. "" with no failure is only for a session with no event at all; any event, or a read that fails, is
		// "status" (not a finish, not never-started) (#6978, QA).
		if any, err := r.SessionsWithAnyEvent(ctx, []string{sessionID}); err != nil || any[sessionID] {
			return "status", false
		}
		return "", false
	}
	if typ != "stop" {
		return typ, false
	}
	_, failed, err := r.LatestStopFailure(ctx, sessionID)
	return typ, failed || err != nil
}

func safeGo(ctx context.Context, name string, fn func()) {
	// A background service restarts after a panic, with a backoff (#3998: the one helper now).
	safego.Loop(ctx, name, fn)
}

func startBackgroundServices(ctx context.Context, db *store.DB, cfg *config.Config, srv *server.Server, agentRT background.AgentRuntime) (*background.WorkflowRunner, *background.JobScheduler) {
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
	// "Trust Coral's hooks in Codex", read on each Codex launch (#153).
	agent.SetCodexHookTrust(routes.CodexTrustFromSettings(sessStore))
	// The version of the Codex a launch uses decides which of Coral's hooks it gets (#6615: PermissionRequest only for one that has it). Read from the CLI's own --version, remembered per binary.
	agent.CodexVersionProbe = func(cliPath string) string {
		return agent.CLIVersion(map[string]string{agent.CLIPathSettingKey("codex"): cliPath}, "codex")
	}

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
	agent.SetStartStagger(true)                         // two Codex starts at once raced its shared state database: every launch path spaces them (#6848)
	launcher.SetServer(cfg.AgentHost(), cfg.CoralDir()) // the agents it starts dial this server and use this data folder (#6767)
	scheduler.SetLaunchFn(launcher.BuildSchedulerLaunchFn(schedStore))
	scheduler.SetSessionStore(sessStore)
	scheduler.SetLastEventType(func(ctx context.Context, sessionID string) (string, bool) { // what a run's agent last did, for a run that ended while Coral was down (#6898)
		return lastEventOfRun(ctx, taskStore, sessionID)
	})
	scheduler.SetEventCheck(func(ctx context.Context, sessionID string) bool { // a run whose agent ended within seconds without a single event is recorded failed (#6842)
		got, err := taskStore.SessionsWithAnyEvent(ctx, []string{sessionID})
		return err != nil || got[sessionID] // a read that fails never judges a run
	})
	scheduler.SetRuntime(agentRT)
	scheduler.SetNextFireTimeFn(background.NextFireTime)
	scheduler.SetWorktreeRoot(coralnames.WorktreesRoot(cfg.CoralDir()))
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
	if boardHealthMonitorEnabled(boardSettings) {
		// Every 5 minutes; each team's own interval (10 by default) decides when its check goes.
		healthMonitor := background.NewBoardHealthMonitor(srv.BoardStore(), 5*time.Minute)
		healthMonitor.SetAgents(taskStore, agentRT, discoverFn)
		healthMonitor.SetIdleThresholds(time.Duration(cfg.TaskIdleReminderS)*time.Second, time.Duration(cfg.TaskIdleEscalationS)*time.Second)
		safeGo(ctx, "board_health_monitor", func() { healthMonitor.Run(ctx) })
		// Coral acts on the mechanical items itself (#4175): claim and status nudges.
		healthNudges := background.NewHealthNudges(srv.BoardStore(), taskStore, agentRT, discoverFn)
		safeGo(ctx, "health_nudges", func() { healthNudges.Run(ctx) })
	}

	// Tasks' one-time reminders (task add --remind-at, #4175 step 3): asked for by a person or
	// the orchestrator, so they go out whatever the health monitor setting says.
	taskReminders := background.NewTaskReminders(srv.BoardStore(), agentRT, discoverFn)
	safeGo(ctx, "task_reminders", func() { taskReminders.Run(ctx) })

	// Remote board poller
	remotePoller := background.NewRemoteBoardPoller(rbStore, agentRT, 30*time.Second)
	remotePoller.SetDiscoverFn(discoverFn)
	safeGo(ctx, "remote_board_poller", func() { remotePoller.Run(ctx) })

	// Batch summarizer — only runs if auto_summarize setting is enabled
	summarizeFn := background.BuildSummarizeFn(sessStore)
	settings, _ := sessStore.GetSettings(ctx)
	if setreg.Bool(settings, setreg.AutoSummarize) { // PUT /api/settings stores a JSON true as "True"
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
	// Raw token usage older than 30 days becomes hourly rows; totals stay the same (#4163).
	tokenRetention := background.NewTokenUsageRetention(db)
	safeGo(ctx, "token_usage_retention", func() { tokenRetention.Run(ctx) })
	// The Cost view's hourly aggregate of token usage, recomputed for the hours that changed (#4752).
	tokenHourAgg := background.NewTokenUsageHourAgg(db)
	safeGo(ctx, "token_usage_hour_agg", func() { tokenHourAgg.Run(ctx) })

	// PRAGMA optimize on both databases, a little after startup and every few hours (#4142).
	optimizeTargets := map[string]background.Optimizer{"sessions.db": db}
	if bs := srv.BoardStore(); bs != nil {
		optimizeTargets["messageboard.db"] = bs
	}
	dbOptimize := background.NewDBOptimize(optimizeTargets)
	safeGo(ctx, "db_optimize", func() { dbOptimize.Run(ctx) })
	// PRAGMA quick_check on both databases, once, a minute after start (#4157): damage is
	// logged and shown on the dashboard; nothing is repaired or deleted.
	integrityTargets := map[string]*sqlx.DB{"sessions.db": db.Reader()}
	if bs := srv.BoardStore(); bs != nil {
		integrityTargets["messageboard.db"] = bs.Reader()
	}
	dbIntegrity := background.NewDBIntegrity(integrityTargets).ConfirmOn(map[string]*sqlx.DB{"sessions.db": db.DB}) // a problem is checked again with writes paused (#4735)
	safego.Go("db_integrity", func() { dbIntegrity.Run(ctx) })

	// A pty Coral wakes the agents that were running when it last stopped (#3636): one pass per
	// start. Restart, not safeGo: safeGo restarts a service that returns, so the pass (and its
	// "agents were running" notice) ran again every few seconds (#4033).
	if cfg.TerminalBackend == "pty" {
		safego.Restart("startup/auto_wake", func() { srv.AutoWakeAfterRestart(ctx) })
	}

	// Uploads retention: pasted/dropped images expire after uploads_retention_days
	// (30 by default) and the folder stays under uploads_max_mb (#3364).
	uploadsRetention := background.NewUploadsRetention(db, cfg.CoralDir())
	uploadsRetention.PurgePasted = srv.PurgePastedImages // the artifact copies of pasted images follow it (#5284)
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
	return wfRunner, scheduler
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
	keywordSwitch := routes.RedactKeywordsFromSettings(ss)
	routes.SetKeywordSwitch(keywordSwitch) // the hub-bound paths of the routes read the same switch (#5172)
	mgr := cloud.New(cloud.Options{
		HubURL:       hubURL,
		Store:        store.NewCloudStore(db, store.NewRemoteServerStore(db, cfg.CoralDir())),
		LocalBaseURL: "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.Port)),
		MaxMode:      routes.MaxModeFromSettings(ss),
		RedactTokens: routes.RedactTokensFromSettings(ss),
		Redactor:     secrets.HubRedactor(keywordSwitch), // Redactor plus the keyword rules (#5141)
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

// ResolveBackend picks this run's terminal backend without asking what is running (the pure rules are in backendchoice, #6064).
func ResolveBackend(flagValue, saved, goos string) (backend string, forced bool) {
	backend, forced, _ = backendchoice.Resolve(flagValue, saved, goos, nil)
	return backend, forced
}

// boardHealthMonitorEnabled reports whether the all-teams health service runs: on unless the setting is false. The
// Settings dialog saves a JSON boolean, which the settings table keeps as sent ("False"), so the word is compared
// without regard to case (#5128).
func boardHealthMonitorEnabled(settings map[string]string) bool {
	return setreg.Bool(settings, setreg.BoardHealthMonitor)
}
