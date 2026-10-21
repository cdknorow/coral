package routes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subgentic/coral/internal/tracking"
)

func postTrackingEvent(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/tracking/event", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	NewTrackingHandler(t.TempDir()).TrackEvent(rr, req)
	return rr
}

func TestTrackEventAcceptsABrowserEvent(t *testing.T) {
	rr := postTrackingEvent(t, `{"event":"dashboard_ready","props":{"page_id":"p1"}}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["ok"] != true {
		t.Fatalf("expected ok:true, got %v", resp)
	}
}

func TestTrackEventRejectsEventsOutsideTheAllowlist(t *testing.T) {
	for _, name := range []string{"", "app_opened", "arbitrary_event", "first_agent_launched"} {
		body, _ := json.Marshal(map[string]string{"event": name})
		rr := postTrackingEvent(t, string(body))
		if rr.Code != http.StatusBadRequest {
			t.Errorf("event %q: expected 400, got %d", name, rr.Code)
		}
	}
}

func TestTrackEventRejectsInvalidJSON(t *testing.T) {
	if rr := postTrackingEvent(t, "not json"); rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestTelemetryDisclosureDescribesTheEventsCoralActuallySends(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/system/telemetry", nil)
	rr := httptest.NewRecorder()
	NewTrackingHandler(t.TempDir()).TelemetryDisclosure(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var body struct {
		Enabled      bool                `json:"enabled"`
		Acknowledged bool                `json:"acknowledged"`
		Events       []tracking.EventDoc `json:"events"`
		Properties   []string            `json:"properties"`
		Never        []string            `json:"never_collected"`
		InstallID    string              `json:"install_id_path"`
		FailureLog   string              `json:"failure_log"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}

	if len(body.Events) != len(tracking.AllEvents) {
		t.Fatalf("disclosure lists %d events but Coral sends %d", len(body.Events), len(tracking.AllEvents))
	}
	for i, want := range tracking.AllEvents {
		if body.Events[i].Name != want.Name {
			t.Errorf("event %d: disclosure says %q, Coral sends %q", i, body.Events[i].Name, want.Name)
		}
	}
	if len(body.Properties) == 0 || len(body.Never) == 0 {
		t.Error("disclosure must state both what is sent and what is never sent")
	}
	if !strings.HasSuffix(body.InstallID, ".install_id") {
		t.Errorf("expected the install ID path, got %q", body.InstallID)
	}
	if !strings.HasSuffix(body.FailureLog, "tracking-failures.log") {
		t.Errorf("expected the failure log path, got %q", body.FailureLog)
	}
}

func TestTelemetryDisplayPathHidesHomeDirectory(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	got := telemetryDisplayPath(filepath.Join(home, ".coral", ".install_id"))
	if got != filepath.Join("~", ".coral", ".install_id") {
		t.Fatalf("expected a home-relative display path, got %q", got)
	}
}

// Every event the browser can originate is in the telemetry disclosure.
func TestEveryBrowserOriginatedEventIsDisclosed(t *testing.T) {
	for name := range allowedTrackingEvents {
		found := false
		for _, e := range tracking.AllEvents {
			if e.Name == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("event %q is accepted by /api/tracking/event but is not in the telemetry disclosure", name)
		}
	}
}
