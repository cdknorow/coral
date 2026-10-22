package routes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTelemetryInstallIDIsReadOnlyAndInstanceScoped(t *testing.T) {
	t.Setenv("CORAL_TELEMETRY_DISABLED", "1")
	dir := t.TempDir()
	if id, status := telemetryInstallID(dir); id != "" || status != "missing" {
		t.Fatalf("missing: %q %q", id, status)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatal("viewing created local state")
	}
	id := "11111111-2222-4333-8444-555555555555"
	file := filepath.Join(dir, ".install_id")
	if err := os.WriteFile(file, []byte(id+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	t.Setenv("CORAL_DATA_DIR", other)
	if got, status := telemetryInstallID(dir); got != id || status != "available" {
		t.Fatalf("existing while opted out: %q %q", got, status)
	}
	if b, _ := os.ReadFile(file); string(b) != id+"\n" {
		t.Fatal("viewing changed the ID")
	}
	if got, status := telemetryInstallID(other); got != "" || status != "missing" {
		t.Fatalf("wrong instance: %q %q", got, status)
	}
}

func TestTelemetryDisclosureInstallIDOnlyForOperator(t *testing.T) {
	dir := t.TempDir()
	id := "11111111-2222-4333-8444-555555555555"
	if err := os.WriteFile(filepath.Join(dir, ".install_id"), []byte(id), 0600); err != nil {
		t.Fatal(err)
	}
	for _, actor := range []Actor{{Kind: ActorOperator, Name: "Operator"}, {Kind: ActorToken, Name: "Agent"}, {Kind: ActorClaimed, Name: "Operator"}} {
		r := withActor(httptest.NewRequest(http.MethodGet, "/api/system/telemetry", nil), actor)
		w := httptest.NewRecorder()
		NewTrackingHandler(dir).TelemetryDisclosure(w, r)
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if actor.IsOperator() {
			if body["install_id"] != id {
				t.Fatalf("Operator missing ID: %v", body)
			}
		} else if body["install_id"] != "" || strings.Contains(w.Body.String(), id) {
			t.Fatal("identifier disclosed to non-Operator")
		}
		if !strings.Contains(body["service_location"].(string), "approximate location") {
			t.Fatal("service location disclosure missing")
		}
		for _, item := range body["never_collected"].([]any) {
			if strings.Contains(item.(string), "location") {
				t.Fatal("runtime claims location never collected")
			}
		}
	}
}

func TestTelemetryInstallIDDoesNotExposeReplacementContents(t *testing.T) {
	for _, text := range []string{"not-an-id", strings.Repeat("x", 1000)} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".install_id"), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		if id, status := telemetryInstallID(dir); id != "" || status != "unavailable" {
			t.Fatalf("invalid exposed: %q %q", id, status)
		}
	}
	if runtime.GOOS == "windows" {
		return
	} // creating links requires a Windows privilege not assumed here
	dir, target := t.TempDir(), filepath.Join(t.TempDir(), "other")
	if err := os.WriteFile(target, []byte("11111111-2222-4333-8444-555555555555"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, ".install_id")); err != nil {
		t.Fatal(err)
	}
	if id, status := telemetryInstallID(dir); id != "" || status != "unavailable" {
		t.Fatalf("symlink exposed: %q %q", id, status)
	}
}
