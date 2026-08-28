package routes

import (
	"net/http"
	"regexp"

	"github.com/cdknorow/coral/internal/tracking"
)

// TrackingHandler exposes the small set of funnel events that can only be
// observed in the browser (link clicks). Everything else is emitted server-side.
type TrackingHandler struct{}

func NewTrackingHandler() *TrackingHandler { return &TrackingHandler{} }

// EventSupporterCheckoutClicked fires when a user clicks any supporter/store link.
const EventSupporterCheckoutClicked = "supporter_checkout_clicked"

// allowedTrackingEvents is a strict allowlist. The endpoint is reachable by
// anything running in the page, so it must never become a general-purpose
// event pipe.
var allowedTrackingEvents = map[string]bool{
	EventSupporterCheckoutClicked: true,
}

// allowedTrackingProps is the allowlist of property keys the browser may set.
// These carry placement and campaign attribution only — never prompts, code,
// repository names, agent output, personal information, or license keys.
var allowedTrackingProps = map[string]bool{
	"surface":  true, // where in the UI the link was clicked
	"campaign": true, // campaign attribution (utm_campaign)
	"source":   true, // campaign attribution (utm_source)
	"medium":   true, // campaign attribution (utm_medium)
}

// trackingPropValue constrains property values to a short slug so no free-form
// user text can be smuggled into an event.
var trackingPropValue = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

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

	props := sanitizeTrackingProps(body.Props)
	tracking.TrackEvent(body.Event, props)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// sanitizeTrackingProps drops every key not on the allowlist and every value
// that is not a short slug. It returns nil when nothing survives.
func sanitizeTrackingProps(in map[string]string) map[string]string {
	var out map[string]string
	for k, v := range in {
		if !allowedTrackingProps[k] || !trackingPropValue.MatchString(v) {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[k] = v
	}
	return out
}
