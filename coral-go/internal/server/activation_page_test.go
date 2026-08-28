package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/license"
	"github.com/cdknorow/coral/internal/server/routes"
)

// The activation page is the first thing an unlicensed user sees, and its
// "Buy a License" button is a supporter surface. It must carry the same
// attribution as the in-app links and report its click like they do.
func TestActivationPageCarriesSupporterAttribution(t *testing.T) {
	s := &Server{}
	rr := httptest.NewRecorder()
	s.serveActivation(rr, httptest.NewRequest(http.MethodGet, "/", nil))

	body := rr.Body.String()

	if strings.Contains(body, "{{STORE_URL}}") {
		t.Fatal("the STORE_URL placeholder was not substituted")
	}
	want := routes.SupporterCheckoutURL(config.StoreURL, routes.SurfaceActivationNag)
	// The URL is HTML-escaped into an href, so compare on the escaped form's
	// distinguishing parts rather than the raw string.
	for _, fragment := range []string{
		"checkout%5Bcustom%5D%5Bsurface%5D=" + routes.SurfaceActivationNag,
		"utm_source=coral_app",
	} {
		if !strings.Contains(body, fragment) {
			t.Errorf("activation page href is missing %q\nexpected URL: %s", fragment, want)
		}
	}
	if !strings.Contains(body, "trackSupporterClick('activation_nag')") {
		t.Error("activation page does not report its supporter click")
	}
	if !strings.Contains(body, "/api/tracking/event") {
		t.Error("activation page has no click-reporting endpoint")
	}
}

// A bare ampersand in an href is invalid HTML and some parsers truncate the
// URL at it, which would silently drop the attribution.
func TestActivationPageEscapesTheSupporterURL(t *testing.T) {
	s := &Server{}
	rr := httptest.NewRecorder()
	s.serveActivation(rr, httptest.NewRequest(http.MethodGet, "/", nil))

	body := rr.Body.String()
	start := strings.Index(body, `href="`+strings.Split(config.StoreURL, "?")[0])
	if start < 0 {
		t.Fatalf("could not find the supporter href in the page")
	}
	end := strings.Index(body[start+6:], `"`)
	href := body[start+6 : start+6+end]
	if strings.Contains(href, "&") && !strings.Contains(href, "&amp;") {
		t.Errorf("supporter href contains unescaped ampersands: %s", href)
	}
}

// The four clauses of the supporter-reminder rule, each isolated.
func TestSupporterReminderRule(t *testing.T) {
	tests := []struct {
		name                                        string
		licenseRequired, licensed, cadence, dismiss bool
		want                                        bool
	}{
		{"due", true, false, true, false, true},
		// Criterion 4: an activated supporter is never asked again.
		{"activated supporter is never asked", true, true, true, false, false},
		// Criterion 2: no result delivered yet means no cadence, so no ask.
		{"no value delivered yet", true, false, false, false, false},
		{"dismissed with Continue Free", true, false, true, true, false},
		{"dev and beta builds never ask", false, false, true, false, false},
		{"licensed and dismissed", true, true, true, true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := supporterReminderDue(tc.licenseRequired, tc.licensed, tc.cadence, tc.dismiss)
			if got != tc.want {
				t.Fatalf("supporterReminderDue(%v,%v,%v,%v) = %v, want %v",
					tc.licenseRequired, tc.licensed, tc.cadence, tc.dismiss, got, tc.want)
			}
		})
	}
}

// End to end through the real handler: a brand-new install gets the dashboard,
// not a pricing page.
func TestFirstEverLaunchServesTheDashboardNotThePricingPage(t *testing.T) {
	dir := t.TempDir()
	counter := license.NewLaunchCounter(dir)
	counter.Increment()
	counter.RecordValueAnchor(false) // nothing delivered yet

	s := &Server{cfg: &config.Config{}, launchCounter: counter}
	if s.shouldShowSupporterReminder(httptest.NewRequest(http.MethodGet, "/", nil)) {
		t.Fatal("a first-ever launch would have been served the supporter reminder")
	}

	// And once value exists and the cadence comes due, it does show.
	counter.RecordValueAnchor(true)
	for i := 0; i < 3; i++ {
		counter.Increment()
	}
	if !s.shouldShowSupporterReminder(httptest.NewRequest(http.MethodGet, "/", nil)) {
		t.Fatal("the reminder never becomes due, so supporters would never be asked")
	}
	// "Continue Free" still works.
	if s.shouldShowSupporterReminder(httptest.NewRequest(http.MethodGet, "/?skip_activation=1", nil)) {
		t.Fatal("Continue Free did not dismiss the reminder")
	}
}
