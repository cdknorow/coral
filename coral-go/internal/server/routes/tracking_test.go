package routes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/cdknorow/coral/internal/config"
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

func TestSupporterCheckoutURLAttachesAttribution(t *testing.T) {
	prev := config.Version
	config.Version = "1.0.8"
	t.Cleanup(func() { config.Version = prev })

	got := SupporterCheckoutURL("https://store.coralai.ai/checkout/buy/abc-123", SurfaceActivationNag)

	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("result is not a valid URL: %v", err)
	}
	if u.Host != "store.coralai.ai" || u.Path != "/checkout/buy/abc-123" {
		t.Fatalf("host/path changed: %s", got)
	}
	q := u.Query()
	want := map[string]string{
		"checkout[custom][source]":   "coral_app",
		"checkout[custom][surface]":  SurfaceActivationNag,
		"checkout[custom][campaign]": "in_app",
		"checkout[custom][version]":  "1.0.8",
		"utm_source":                 "coral_app",
		"utm_medium":                 "in_app",
		"utm_campaign":               "in_app",
	}
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("param %q = %q, want %q", k, q.Get(k), v)
		}
	}
}

func TestSupporterCheckoutURLOmitsAnUnsetVersion(t *testing.T) {
	prev := config.Version
	config.Version = ""
	t.Cleanup(func() { config.Version = prev })

	u, _ := url.Parse(SupporterCheckoutURL("https://store.coralai.ai/checkout/buy/abc", SurfaceSettingsTierBadge))
	if _, ok := u.Query()["checkout[custom][version]"]; ok {
		t.Fatal("expected no version parameter when config.Version is unset")
	}
}

func TestSupporterCheckoutURLPreservesExistingQueryParameters(t *testing.T) {
	u, err := url.Parse(SupporterCheckoutURL("https://store.coralai.ai/checkout/buy/abc?discount=FREE", SurfaceLicenseSettingsPanel))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if u.Query().Get("discount") != "FREE" {
		t.Fatalf("existing query parameter was dropped: %s", u.String())
	}
	if u.Query().Get("checkout[custom][surface]") != SurfaceLicenseSettingsPanel {
		t.Fatalf("attribution missing: %s", u.String())
	}
}

func TestSupporterCheckoutURLReturnsAnUnusableBaseUnchanged(t *testing.T) {
	for _, base := range []string{"", "not a url", "/relative/path"} {
		if got := SupporterCheckoutURL(base, SurfaceActivationNag); got != base {
			t.Errorf("base %q: expected it returned unchanged, got %q", base, got)
		}
	}
}

func TestSupporterCheckoutURLsCoversEverySurface(t *testing.T) {
	urls := SupporterCheckoutURLs("https://store.coralai.ai/checkout/buy/abc")
	for _, surface := range []string{SurfaceSettingsTierBadge, SurfaceLicenseSettingsPanel, SurfaceActivationNag} {
		got, ok := urls[surface]
		if !ok {
			t.Fatalf("surface %q missing from SupporterCheckoutURLs", surface)
		}
		u, _ := url.Parse(got)
		if u.Query().Get("checkout[custom][surface]") != surface {
			t.Errorf("surface %q: wrong attribution in %q", surface, got)
		}
	}
	if len(urls) != 3 {
		t.Errorf("expected 3 surfaces, got %d", len(urls))
	}
}

// Every surface constant must be an accepted value of the event's `surface`
// property, or the link attribution and the click event will disagree.
func TestSurfaceConstantsAreValidEventPropertyValues(t *testing.T) {
	for _, surface := range []string{SurfaceSettingsTierBadge, SurfaceLicenseSettingsPanel, SurfaceActivationNag} {
		got := sanitizeTrackingProps(map[string]string{"surface": surface})
		if got["surface"] != surface {
			t.Errorf("surface %q is rejected by the event property allowlist", surface)
		}
	}
}
