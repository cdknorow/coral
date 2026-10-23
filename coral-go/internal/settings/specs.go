package settings

import "math"

// Key names that other packages refer to by constant. They are declared here so there is one spelling.
const (
	ArtifactCommentsHubWrite = "artifact_comments_hub_write"
	ArtifactCommentsOwnerID  = "artifact_comments_owner_id"
	TelemetryEnabled         = "telemetry_enabled"
	RemoteAccessEnabled      = "remote_access_enabled"
	BoardHealthMonitor       = "board_health_monitor"
	AutoSummarize            = "auto_summarize"
	DebugInputLog            = "debug_input_log"
	AgentPriority            = "agent_priority"
	AutoGoals                = "auto_goals"
	RequireAuthOnLoopback    = "require_auth_on_loopback"
	GitPollIntervalS         = "git_poll_interval_s"
	KnowledgeTimeoutS        = "knowledge_timeout_s"
	CallMetricsRetentionDay  = "call_metrics_retention_days"
	UploadsRetentionDays     = "uploads_retention_days"
	UploadsMaxMB             = "uploads_max_mb"
	FileSearchLimit          = "file_search_limit"
	GitDiffMode              = "git_diff_mode"
	DefaultAgentType         = "default_agent_type"
	TerminalReplayBytes      = "terminal_replay_bytes"
	TerminalBackend          = "terminal_backend"
	TerminalDefaultNotice    = "terminal_default_notice"
	HubMode                  = "hub_mode"
	HubOperatorDisabled      = "hub_operator_disabled"
	LegacyHubOperator        = "hub_acts_as_operator"
	HubRedactTokens          = "hub_redact_tokens"
	CloudAllowHubAutoAccept  = "cloud_allow_hub_auto_accept"
	HubRelaunchUpdates       = "hub_relaunch_updates"
	UpdateAuto               = "update_auto"
	CodexTrustCoralHooks     = "codex_trust_coral_hooks"
	HubCrossTeamSettings     = "hub_cross_team_settings"
	CrossTeamLinksSeen       = "cross_team_links_seen"
	HubAutoAcceptChanged     = "cloud_hub_auto_accept_changed"
	UpdateChannel            = "update_channel"
	CloudMaxMode             = "cloud_max_mode"
	IdentityActor            = "identity_actor"
	AgentLaunchLimit         = "agent_launch_limit"
	AutoResumeAfterAPIError  = "auto_resume_after_api_error"
	AutoWakeAfterRestart     = "auto_wake_after_restart"
	AutoWakeCap              = "auto_wake_cap"
	GateSignalsClaude        = "gate_signals_claude"
	BoardAutoAttach          = "board_auto_attach"
	CrossTeamAllowlist       = "cross_team_allowlist"
	TurnEndHandoff           = "turn_end_handoff"
	SavedTeamTemplates       = "saved_team_templates"
	DefaultPromptOrch        = "default_prompt_orchestrator"
	DefaultPromptWorker      = "default_prompt_worker"
	ArtifactStoreMaxBytes    = "artifact_store_max_bytes"
	ArtifactStoreAutoRaised  = "artifact_store_auto_raised"
	ReceivedArtifactsMax     = "received_artifacts_max_bytes"
	ReceivedCapNoticeAck     = "received_cap_notice_ack"
	ArtifactRetentionDays    = "artifact_retention_days"
	ArtifactRetentionKinds   = "artifact_retention_kinds"
	ArtifactRetentionEnable  = "artifact_retention_enabled_at"
	ArtifactRetentionSeen    = "artifact_retention_seen_days"
	ArtifactRetentionAckAt   = "artifact_retention_ack_at"
	ArtifactRetentionLast    = "artifact_retention_last"
)

// Prefixes of the families.
const (
	CLIPathPrefix         = "cli_path_"
	DefaultModelPrefix    = "default_model_"
	DefaultThinkingPrefix = "default_thinking_"
)

// AgentTypes are the agent types a default or a per-type setting may name.
var AgentTypes = []string{"claude", "codex", "agy", "antigravity", "gemini", "pi", "terminal"}

var permissionModes = []string{"default", "plan", "acceptEdits", "auto", "bypassPermissions", "dontAsk"}

func boolOn(key string, a Apply, doc string) {
	register(Spec{Key: key, Kind: KindBool, Default: "true", Apply: a, Doc: doc})
}
func boolOff(key string, a Apply, doc string) {
	register(Spec{Key: key, Kind: KindBool, Default: "false", Apply: a, Doc: doc})
}
func text(key string, a Apply, doc string) {
	register(Spec{Key: key, Kind: KindText, Apply: a, Doc: doc})
}
func ownRoute(key string, k Kind, def string, doc string) {
	register(Spec{Key: key, Kind: k, Default: def, Class: ClassOwnRoute, Apply: ApplyLive, Doc: doc})
}
func coral(key string, doc string) {
	register(Spec{Key: key, Kind: KindOpaque, Class: ClassCoral, Apply: ApplyNone, Doc: doc})
}

// list is a JSON list kept as text; its write cap is 2 MiB.
func list(key, doc string) {
	register(Spec{Key: key, Kind: KindList, Apply: ApplyClient, MaxLen: 2 << 20, Doc: doc})
}

// whole is a whole-number setting with write limits lo..hi and a read mode.
func whole(key, def string, lo, hi int64, rm ReadMode, a Apply, doc string) Spec {
	return Spec{Key: key, Kind: KindInt, Default: def, Min: lo, Max: hi, Read: rm, Apply: a, Doc: doc}
}

func init() {
	// Settings > General and the terminal (the dashboard reads these; the server keeps them).
	register(Spec{Key: "theme", Kind: KindEnum, Values: []string{"dark", "light", "system"}, Apply: ApplyClient, Doc: "Colour scheme."})
	text("custom_theme", ApplyClient, "The custom theme's colours.")
	text("default_renderer", ApplyClient, "The renderer a new terminal uses.")
	register(Spec{Key: DefaultAgentType, Kind: KindEnum, Values: AgentTypes, Apply: ApplyLive, Doc: "The agent type a launch without one uses."})
	register(Spec{Key: "default_permission_mode", Kind: KindEnum, Values: permissionModes, Apply: ApplyClient, Doc: "The permission mode the launch form starts with."})
	text("default_working_dir", ApplyClient, "The folder the launch form starts in.")
	boolOff("fit_pane_width", ApplyClient, "Fit the terminal to the pane's width.")
	boolOff("notify_needs_input", ApplyClient, "Notify when an agent needs input.")
	boolOff("show_scrollbars", ApplyClient, "Show scrollbars.")
	register(whole("terminal_font_size", "", 6, 72, ReadAny, ApplyClient, "Terminal font size."))
	register(whole("terminal_scrollback", "", 0, 1_000_000, ReadAny, ApplyClient, "Terminal scrollback lines."))
	s := whole(TerminalReplayBytes, "", 4<<10, 16<<20, ReadClamp, ApplyRestart, "Bytes of terminal output replayed to a new viewer.")
	s.WriteClamp = true
	register(s)
	register(Spec{Key: "file_search_mode", Kind: KindEnum, Values: []string{"directory", "fuzzy"}, Apply: ApplyClient, Doc: "How the file search matches."})
	s = whole(FileSearchLimit, "500", 1, 100_000, ReadDefault, ApplyLive, "Most results a file search returns.")
	s.ReadBounded, s.ReadMin, s.ReadMax = true, 1, math.MaxInt64
	register(s)
	register(Spec{Key: GitDiffMode, Kind: KindEnum, Values: []string{"branch_point", "previous_commit", "main_head"}, Default: "branch_point", Apply: ApplyLive, Doc: "What a session's diff is taken against."})
	s = whole(GitPollIntervalS, "", 0, 86_400, ReadAny, ApplyLive, "Seconds between git polls; 0 is off. Unset takes the --git-poll-interval flag.")
	register(s)
	register(Spec{Key: AgentPriority, Kind: KindOpaque, Apply: ApplyLive, Doc: "Process priority of agents (validated by agentprio)."})
	s = whole(AgentLaunchLimit, "64", 0, 100_000, ReadDefault, ApplyLive, "Live agents an agent token may add; 0 is no limit.")
	s.ReadBounded, s.ReadMin, s.ReadMax = true, 0, math.MaxInt64
	register(s)
	boolOn(TelemetryEnabled, ApplyLive, "Anonymous usage statistics.")
	boolOff(BoardAutoAttach, ApplyLive, "Attach files named on the board to the artifact store.")
	boolOff(CrossTeamAllowlist, ApplyLive, "Put the cross-team allowlist in force: a board takes messages and files from another team only from a sender on its list (default deny). Off keeps today's rules and only counts what the list would have refused.")
	boolOff(DebugInputLog, ApplyLive, "Log the keystrokes sent to agents.")
	boolOff(RemoteAccessEnabled, ApplyRestart, "Listen on the network, not just this computer.")
	optIn(RemoteAccessEnabled)
	boolOff("refresh_files_on_switch", ApplyClient, "Refresh the file list when switching agents.")
	boolOff("show_session_server", ApplyClient, "Show each agent's server.")
	boolOff("show_session_summary", ApplyClient, "Show each agent's summary.")
	boolOn(BoardHealthMonitor, ApplyRestart, "Team health checks for every team.")
	boolOff(AutoSummarize, ApplyRestart, "Summarise sessions automatically.")
	boolOn(GateSignalsClaude, ApplyLive, "Phase 2 of the paste gate for Claude.")
	register(Spec{Key: UpdateChannel, Kind: KindEnum, Values: []string{"approved", "agentic"}, Default: "approved", Apply: ApplyLive, Doc: "Which releases the update notice and the tray offer: approved (the release marked latest) or agentic (every published release, approved or not). Notice only: the updater installs approved releases whatever this says (#5557)."})
	register(Spec{Key: TurnEndHandoff, Kind: KindEnum, Values: []string{"off", "on"}, Default: "on", Apply: ApplyLive, Doc: "Hand the turn on at its end."})
	boolOn(AutoGoals, ApplyLive, "Run the goals service.")
	ownCheck(AutoGoals)
	boolOff(AutoResumeAfterAPIError, ApplyLive, "Resume an agent after an API error.")
	ownCheck(AutoResumeAfterAPIError)
	boolOn(AutoWakeAfterRestart, ApplyLive, "Wake agents after Coral restarts.")
	ownCheck(AutoWakeAfterRestart)
	register(whole(AutoWakeCap, "8", 1, 50, ReadDefault, ApplyLive, "Most agents woken after a restart."))
	ownCheck(AutoWakeCap)
	boolOff(RequireAuthOnLoopback, ApplyLive, "Ask for a credential on this computer too.")
	ownCheck(RequireAuthOnLoopback)

	// The first-launch screen.
	register(Spec{Key: "welcome_choice", Kind: KindEnum, Values: []string{"skipped", "connected", "own-hub"}, Apply: ApplyClient, Doc: "What was chosen on the first-launch screen."})
	boolOff("welcome_pending", ApplyClient, "The first-launch screen is still to be shown.")
	boolOff("home_editions_hidden", ApplyClient, "Hide the editions card on Home.")

	// Storage.
	register(Spec{Key: ArtifactStoreMaxBytes, Kind: KindInt, Apply: ApplyLive, Doc: "Cap of the artifact store in bytes."})
	register(Spec{Key: ReceivedArtifactsMax, Kind: KindInt, Apply: ApplyLive, Doc: "Cap of received artifacts in bytes; 0 is none."})
	register(Spec{Key: ReceivedCapNoticeAck, Kind: KindInt, Apply: ApplyLive, Doc: "The cap the Operator has been told about at 80%."})
	register(Spec{Key: ArtifactRetentionDays, Kind: KindInt, Apply: ApplyLive, Doc: "Delete artifacts older than this many days (see artifact_retention.go)."})
	register(Spec{Key: ArtifactRetentionKinds, Kind: KindOpaque, Apply: ApplyLive, Doc: "Which kinds the retention applies to."})
	register(whole(UploadsRetentionDays, "30", 1, 365, ReadClamp, ApplyLive, "Days an upload is kept."))
	register(whole(UploadsMaxMB, "1024", 50, 102_400, ReadClamp, ApplyLive, "Cap of the uploads folder in MB."))
	register(whole(CallMetricsRetentionDay, "14", 1, 90, ReadClamp, ApplyLive, "Days call metrics are kept."))
	s = whole(KnowledgeTimeoutS, "120", 1, 86_400, ReadDefault, ApplyLive, "Seconds the knowledge indexer gives one call; below 30 reads as the default.")
	s.ReadBounded, s.ReadMin, s.ReadMax = true, 30, math.MaxInt64
	register(s)

	// Coral Hub request pools. Their limits are per pool (hub_limits.go).
	for _, hl := range []struct {
		key    string
		lo, hi int64
		doc    string
	}{
		{"hub_limit_global", 8, 1024, "Coral Hub requests in flight, all pools; 0 is the built-in size."},
		{"hub_limit_long", 2, 256, "Coral Hub long requests in flight; 0 is the built-in size."},
		{"hub_limit_search", 1, 64, "Coral Hub searches in flight; 0 is the built-in size."},
	} {
		register(whole(hl.key, "0", hl.lo, hl.hi, ReadDefault, ApplyLive, hl.doc))
		ownCheck(hl.key)
	}

	// Prompts.
	text(DefaultPromptOrch, ApplyLive, "The default orchestrator prompt.")
	text(DefaultPromptWorker, ApplyLive, "The default worker prompt.")
	text("team_reminder_orchestrator", ApplyLive, "The reminder an orchestrator gets.")
	text("team_reminder_worker", ApplyLive, "The reminder a worker gets.")

	// JSON lists kept as text.
	list("macros", "Saved macros.")
	list("saved_personas", "Saved personas.")
	list(SavedTeamTemplates, "Saved team templates.")
	list("session_order", "Order of the session list.")
	list("group_order", "Order of the groups.")

	// Set only with their own routes.
	register(Spec{Key: TerminalBackend, Kind: KindEnum, Values: []string{"pty", "tmux"}, Class: ClassOwnRoute, Apply: ApplyRestart, Doc: "Terminal backend."})
	ownRoute(HubOperatorDisabled, KindBool, "false", "The Hub may not act as the Operator.")
	ownRoute(LegacyHubOperator, KindBool, "false", "Old spelling of the opposite of hub_operator_disabled.")
	ownRoute(HubMode, KindBool, "false", "Coral is a Coral Hub's server.")
	optIn(HubMode)
	register(Spec{Key: IdentityActor, Kind: KindEnum, Values: []string{"log", "enforce"}, Default: "log", Class: ClassOwnRoute, Apply: ApplyLive, Doc: "Whether the actor header is enforced."})
	ownRoute(CloudMaxMode, KindText, "", "The most access Coral Hub may have.")
	ownRoute(CloudAllowHubAutoAccept, KindBool, "true", "The Hub may auto-accept, or start an agent that skips permission prompts (on unless the owner turned it off; the Hub owner may change it, #5840).")
	ownRoute(CodexTrustCoralHooks, KindBool, "true", "Coral trusts its own hooks for each Codex it launches, when only an administrator can change them (#153); off, Codex asks the person to review them.")
	ownRoute(UpdateAuto, KindBool, "true", "Coral checks for updates in the background and keeps a verified one ready (#5280); installing one always needs a person to relaunch it.")
	ownRoute(HubRelaunchUpdates, KindBool, "false", "The Hub's owner may relaunch this Coral to install a staged update (#5280). Off until the person at this computer turns it on (#6290): an install the person here was not asked about is never the default.")
	optIn(HubRelaunchUpdates)
	ownRoute(HubCrossTeamSettings, KindBool, "true", "Coral Hub's owner may turn cross-team messaging and file sharing on or off for this computer's teams (#5572); off, Coral Hub changes neither, in either direction.")
	coral(HubAutoAcceptChanged, "Who last changed the Coral Hub auto-accept switch, the Coral Hub owner or this computer, and when, as JSON: written by Coral, shown beside the switch in Settings > Network and Hub (#5840).")
	coral(CrossTeamLinksSeen, "Set once, by Coral, when a Coral Hub advertised cross_team_links/1 to this computer: from then on a connection without it is refused, never sent back to the old path (#5572).")
	ownRoute(HubRedactTokens, KindBool, "true", "Redact tokens in what the Hub sees.")
	ownRoute("hub_signin_enabled", KindBool, "false", "Sign in with the Hub.")
	optIn("hub_signin_enabled")
	ownRoute("hub_signin_origins", KindText, "", "Origins allowed for the Hub sign-in.")
	ownRoute("hub_signin_offer_dismissed", KindBool, "false", "The sign-in offer was dismissed.")
	ownRoute(ArtifactRetentionAckAt, KindOpaque, "", "When the Operator acknowledged a dry run.")

	// Artifact comments (#5176): Coral Hub viewers may comment (off until coral-hub settles a stable identity), and the install's owner id.
	boolOn("hub_redact_keywords", ApplyLive, "Redact the value after a name such as api_key or password in what Coral Hub sees (#5141).")

	boolOff(ArtifactCommentsHubWrite, ApplyLive, "Coral Hub viewers may comment on artifacts.")
	optIn(ArtifactCommentsHubWrite)

	// Coral's own.
	coral(TerminalDefaultNotice, "Whether the one line \"Agents now stop when you quit Coral\" is owed (pending) or was shown and dismissed (seen) or is not owed (none): set once, by Coral, at the first start on the built-in terminal default of an install that ran on the old tmux default (#6064).")
	coral(ArtifactCommentsOwnerID, "The random id the owner's artifact comments are stored under.")
	coral(ArtifactRetentionEnable, "When Coral saw the retention days change.")
	coral(ArtifactStoreAutoRaised, "The cap was raised automatically.")
	coral(ArtifactRetentionSeen, "The days the retention last ran with.")
	coral(ArtifactRetentionLast, "The last retention run's summary (JSON).")
	coral("cloud_bootstrap_status", "Coral Hub bootstrap.")
	coral("demo_workflows_seeded", "The demo workflows were created.")
	coral("self_test", "Written by the self-test.")

	families = []Family{
		{Prefix: CLIPathPrefix, Kind: KindText, Apply: ApplyLive, Doc: "Path of an agent's CLI."},
		{Prefix: DefaultModelPrefix, Kind: KindText, Apply: ApplyLive, Doc: "The model a launch of that agent type uses."},
		{Prefix: DefaultThinkingPrefix, Kind: KindText, Apply: ApplyLive, Doc: "The thinking level a launch of that agent type uses."},
	}
}

// ownCheck marks an already registered key as checked by PutSettings' own rule.
func ownCheck(key string) {
	s := specs[key]
	s.OwnCheck = true
	specs[key] = s
}

// optIn marks an already registered bool as one only the word true turns on.
func optIn(key string) {
	s := specs[key]
	s.OptIn = true
	specs[key] = s
}
