package tracking

import (
	"os"
	"strings"
	"testing"
)

// allEventConstants is every event constant declared in this package. AllEvents
// drives the user-facing disclosure, so the two must agree exactly: an event
// missing from AllEvents is an event Coral sends without disclosing.
var allEventConstants = []string{
	EventInstall,
	EventUpgrade,
	EventAppOpened,
	EventSessionLaunched,
	EventTeamLaunched,
	EventFirstAgentLaunched,
	EventFirstTeamLaunched,
	EventFirstTaskCompleted,
	EventReturned24h,
	EventSupporterCheckoutClicked,
	EventLicenseActivated,
}

func TestAllEventsCoversEveryEventConstant(t *testing.T) {
	documented := map[string]bool{}
	for _, e := range AllEvents {
		if documented[e.Name] {
			t.Errorf("event %q is listed twice in AllEvents", e.Name)
		}
		documented[e.Name] = true
		if e.When == "" {
			t.Errorf("event %q has no description; the disclosure would show a blank row", e.Name)
		}
	}
	for _, name := range allEventConstants {
		if !documented[name] {
			t.Errorf("event %q is emitted but missing from AllEvents, so it would not be disclosed", name)
		}
	}
	for name := range documented {
		found := false
		for _, c := range allEventConstants {
			if c == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("AllEvents documents %q, which is not an event constant — the disclosure would describe an event Coral never sends", name)
		}
	}
}

// The written reference and the in-app disclosure must not drift apart.
func TestTelemetryDocListsEveryEvent(t *testing.T) {
	data, err := os.ReadFile("../../agent_docs/telemetry.md")
	if err != nil {
		t.Skipf("telemetry doc not readable from here: %v", err)
	}
	doc := string(data)
	for _, e := range AllEvents {
		if !strings.Contains(doc, "`"+e.Name+"`") {
			t.Errorf("agent_docs/telemetry.md does not document event %q", e.Name)
		}
	}
	for _, never := range []string{"prompts", "source code", "license key"} {
		if !strings.Contains(strings.ToLower(doc), never) {
			t.Errorf("agent_docs/telemetry.md is missing the %q never-collected claim", never)
		}
	}
}

func TestDisclosureAcknowledgementIsPersistentAndIdempotent(t *testing.T) {
	_, dir := newTestTracking(t)

	if DisclosureAcknowledged() {
		t.Fatal("a fresh install should not be marked as having seen the disclosure")
	}
	if err := AcknowledgeDisclosure(); err != nil {
		t.Fatalf("AcknowledgeDisclosure: %v", err)
	}
	if !DisclosureAcknowledged() {
		t.Fatal("acknowledgement did not persist")
	}
	// Acknowledging twice must not fail — the user may double-click.
	if err := AcknowledgeDisclosure(); err != nil {
		t.Fatalf("second AcknowledgeDisclosure: %v", err)
	}
	if !DisclosureAcknowledged() {
		t.Fatal("acknowledgement was lost on the second call")
	}

	// It lives next to .install_id so it survives upgrades.
	if _, err := os.Stat(dir + "/" + disclosureFileName); err != nil {
		t.Fatalf("expected %s in the coral dir: %v", disclosureFileName, err)
	}
}

func TestEnabledFollowsThePostHogKey(t *testing.T) {
	_, _ = newTestTracking(t) // sets a key and restores it afterwards
	if !Enabled() {
		t.Error("expected Enabled() to be true when a key is present")
	}
	config_PostHogKeyTo(t, "")
	if Enabled() {
		t.Error("expected Enabled() to be false for a build with no analytics key")
	}
}
