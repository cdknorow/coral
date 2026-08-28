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
	{Name: EventTeamLaunched, When: "Every time you launch a team.", Extra: "agent_count — how many agents were in the team"},
	{Name: EventFirstAgentLaunched, When: "Once ever: the first agent you launch."},
	{Name: EventFirstTeamLaunched, When: "Once ever: the first team you launch.", Extra: "agent_count — how many agents were in the team"},
	{Name: EventFirstTaskCompleted, When: "Once ever: the first message-board task marked complete."},
	{Name: EventReturned24h, When: "Once ever: the first time you open Coral more than 24 hours after your first open."},
	{Name: EventSupporterCheckoutClicked, When: "Every time you click a link to the supporter store.", Extra: "surface, campaign, source, medium — which link was clicked and where it came from"},
	{Name: EventLicenseActivated, When: "Every time a license key is activated successfully.", Extra: "product_name, variant_name — never the key, your name, or your email"},
}

// Enabled reports whether this build can send anything at all. Builds compiled
// from source carry no analytics key and send nothing.
func Enabled() bool { return posthogKeyPresent() }
