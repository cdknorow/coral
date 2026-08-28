package routes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func postTrackingEvent(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/tracking/event", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	NewTrackingHandler().TrackEvent(rr, req)
	return rr
}

func TestTrackEventAcceptsTheSupporterCheckoutEvent(t *testing.T) {
	rr := postTrackingEvent(t, `{"event":"supporter_checkout_clicked","props":{"surface":"settings_tier_badge"}}`)
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

func TestSanitizeTrackingPropsDropsAnythingNotAllowlisted(t *testing.T) {
	tests := []struct {
		name string
		in   map[string]string
		want map[string]string
	}{
		{
			name: "keeps allowlisted keys with slug values",
			in:   map[string]string{"surface": "activation_nag", "campaign": "readme", "source": "github", "medium": "link"},
			want: map[string]string{"surface": "activation_nag", "campaign": "readme", "source": "github", "medium": "link"},
		},
		{
			name: "drops keys that are not allowlisted",
			in:   map[string]string{"surface": "activation_nag", "prompt": "my secret prompt", "repo": "acme/private"},
			want: map[string]string{"surface": "activation_nag"},
		},
		{
			name: "drops values that are not short slugs",
			in:   map[string]string{"surface": "has spaces", "campaign": "has/slash", "source": strings.Repeat("a", 65)},
			want: nil,
		},
		{
			name: "drops empty values",
			in:   map[string]string{"surface": ""},
			want: nil,
		},
		{
			name: "no props at all",
			in:   nil,
			want: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeTrackingProps(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
