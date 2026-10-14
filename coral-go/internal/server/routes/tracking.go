package routes

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/cdknorow/coral/internal/tracking"
)

// TrackingHandler exposes the small set of funnel events that can only be
// observed in the browser (the dashboard loading, failing, a prompt submitted). Everything else is emitted server-side.
type TrackingHandler struct {
	// coralDir is this instance's data directory, used only to show the user
	// where their install ID and failure log live. Passed in rather than read
	// from package state so the paths shown always belong to this instance.
	coralDir string
}

func NewTrackingHandler(coralDir string) *TrackingHandler {
	return &TrackingHandler{coralDir: coralDir}
}

// allowedTrackingEvents is a strict allowlist: the dashboard-observed events
// only. The endpoint is reachable by anything running in the page, so it must
// never become a general-purpose event pipe, and server lifecycle events
// (launches, tasks, first_* milestones) are rejected here.
var allowedTrackingEvents = tracking.BrowserEvents

// TrackEvent records a browser-observed funnel event.
// POST /api/tracking/event
func (h *TrackingHandler) TrackEvent(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Event string            `json:"event"`
		Props map[string]string `json:"props"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if !allowedTrackingEvents[body.Event] {
		errBadRequest(w, "unknown event")
		return
	}

	if len(body.Props) > 8 {
		errBadRequest(w, "too many properties")
		return
	}
	// Typed validation, per-event server-side dedupe and caps live in the
	// tracking package; unknown properties and invalid values are dropped there.
	tracking.TrackBrowserEvent(body.Event, body.Props)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ── Telemetry disclosure ─────────────────────────────────────────────────

// TelemetryDisclosure returns everything the first-run disclosure needs to
// render, generated from the tracking package's own event list so the UI can
// never describe a different set of events than the one Coral sends.
// GET /api/system/telemetry
func (h *TrackingHandler) TelemetryDisclosure(w http.ResponseWriter, r *http.Request) {
	installIDPath := telemetryDisplayPath(filepath.Join(h.coralDir, ".install_id"))
	failureLogPath := telemetryDisplayPath(filepath.Join(h.coralDir, "tracking-failures.log"))
	writeJSON(w, http.StatusOK, map[string]any{
		// enabled is false for builds compiled from source, which carry no
		// analytics key and send nothing. The disclosure is not shown then:
		// warning users about collection that is not happening is misleading.
		"enabled":         tracking.Enabled(),
		"acknowledged":    tracking.DisclosureAcknowledged(),
		"events":          tracking.AllEvents,
		"properties":      tracking.StandardProperties,
		"never_collected": tracking.NeverCollected,
		"install_id_path": installIDPath,
		"failure_log":     failureLogPath,
	})
}

// telemetryDisplayPath keeps the disclosure useful without embedding the local
// account name in text that may be copied into screenshots or bug reports.
func telemetryDisplayPath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	prefix := home + string(filepath.Separator)
	if strings.HasPrefix(path, prefix) {
		return filepath.Join("~", strings.TrimPrefix(path, prefix))
	}
	return path
}

// AcknowledgeTelemetryDisclosure records that the user has seen the
// disclosure, so it does not appear again.
// POST /api/system/telemetry/acknowledge
func (h *TrackingHandler) AcknowledgeTelemetryDisclosure(w http.ResponseWriter, r *http.Request) {
	if err := tracking.AcknowledgeDisclosure(); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "acknowledged": true})
}
