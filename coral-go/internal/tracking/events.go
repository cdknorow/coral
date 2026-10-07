package tracking

// Every event Coral can send is named here. The user-facing telemetry
// disclosure is generated from AllEvents, so an event that is missing from
// this file is an event Coral does not disclose. Adding an emit site without
// adding it here fails TestAllEventsCoversEveryEventConstant.

const (
	// Lifecycle.
	EventInstall   = "install"
	EventUpgrade   = "upgrade"
	EventAppOpened = "app_opened"

	// Usage.
	EventSessionLaunched = "session_launched"
	EventTeamLaunched    = "team_launched"

	// Conversion.
	EventSupporterCheckoutClicked = "supporter_checkout_clicked"
	EventLicenseActivated         = "license_activated"
)

// EventDoc describes one event in terms a user can check against the source.
type EventDoc struct {
	Name string `json:"name"`
	// When says what causes the event, in plain language.
	When string `json:"when"`
	// Extra names properties this event carries beyond the standard four
	// (version, edition, os, arch). Empty when it carries only those.
	Extra string `json:"extra,omitempty"`
}

// StandardProperties are attached to every event without exception.
var StandardProperties = []string{
	"version — the Coral version you are running",
	"edition — the build tier (prod, beta, dev)",
	"os — your operating system (darwin, linux, windows)",
	"arch — your CPU architecture (amd64, arm64)",
	"platform — your client platform (darwin, linux, windows, wsl2)",
	"schema_version — the version of this event format",
	"run_id — a random ID for this run of Coral, new every time it starts (not your install ID)",
	"entrypoint — which Coral program is running (coral, coral-tray, launch-coral)",
}

// NeverCollected is the explicit list of things no event carries. It is stated
// positively in the disclosure because a short, complete list of what is sent
// is only reassuring alongside an equally explicit list of what is not.
var NeverCollected = []string{
	"Your prompts",
	"Your source code",
	"Repository, branch, and file names",
	"Agent output",
	"Your name, email address, or IP-derived location",
	"Your license key",
}

// AllEvents is the complete, ordered list of every event Coral can send.
var AllEvents = []EventDoc{
	{Name: EventInstall, When: "The first time Coral runs on this machine."},
	{Name: EventUpgrade, When: "The first run after Coral's version changes."},
	{Name: EventAppOpened, When: "Every time the Coral server starts."},
	{Name: EventSessionLaunched, When: "Every time you launch a single agent."},
	{Name: EventTeamLaunched, When: "Every time a team launch starts at least one agent.", Extra: "agent_count — how many agents started; requested_agents, started_agents, failed_agents"},
	{Name: EventFirstAgentLaunched, When: "Once ever: the first agent you launch."},
	{Name: EventFirstTeamLaunched, When: "Once ever: the first team you launch.", Extra: "agent_count — how many agents started"},
	{Name: EventFirstTaskCompleted, When: "Once ever: the first message-board task marked complete, whether it succeeded or failed.", Extra: "outcome — success or failed"},
	{Name: EventFirstTaskSucceeded, When: "Once ever: the first message-board task completed successfully."},
	{Name: EventTaskCompleted, When: "Every time a message-board task is marked complete.", Extra: "outcome — success or failed"},
	{Name: EventLaunchRequested, When: "Every time you start launching an agent or a team.", Extra: "kind, attempt_id (random per launch), provider (claude, codex, gemini, other, mixed), backend (tmux, pty), requested_agents, resume"},
	{Name: EventLaunchResult, When: "When that launch finishes.", Extra: "the launch_requested properties plus outcome (success, failure, partial), failure_category (a fixed list such as limit_reached or spawn_failed — never the error text), duration_ms, started_agents, failed_agents"},
	{Name: EventDashboardReady, When: "Each time the dashboard page finishes loading and shows your agents.", Extra: "page_id — a random ID for that page load"},
	{Name: EventDashboardActiveDay, When: "At most once per UTC day that you have the dashboard open."},
	{Name: EventDashboardFailed, When: "When the dashboard cannot load its agent list or status at startup.", Extra: "code — a fixed code such as sessions_fetch_http or init_failed"},
	{Name: EventPrerequisiteCheck, When: "When Coral checks whether tmux, the Claude CLI or the Codex CLI is available: at most once per run for each result and each place that checked. Coral does not install anything, so a missing tool is not an installation failure.", Extra: "tool (tmux, claude, codex), status (available, missing, probe_failed, timeout), source (system_status, cli_check, cli_recheck) — never a path or a version"},
	{Name: EventPromptSubmitRequested, When: "Once ever: the first time you send a prompt from the dashboard composer.", Extra: "source — dashboard_composer; never the prompt"},
	{Name: EventFirstPromptSubmitted, When: "Once ever: the first time a prompt is accepted by an agent terminal over the dashboard's HTTP send.", Extra: "source — http_send; never the prompt"},
	{Name: EventReturned24h, When: "Once ever: the first time you open Coral more than 24 hours after your first open."},
	{Name: EventSupporterCheckoutClicked, When: "Every time you click a link to the supporter store.", Extra: "surface, campaign, source, medium — which link was clicked and where it came from"},
	{Name: EventLicenseActivated, When: "Every time a license key is activated successfully.", Extra: "product_name, variant_name — never the key, your name, or your email"},
}

// Enabled reports whether this build can send anything at all. Builds compiled
// from source carry no analytics key and send nothing.
func Enabled() bool { return posthogKeyPresent() }
