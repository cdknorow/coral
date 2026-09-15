package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
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
	if s.claimSupporterReminder(httptest.NewRequest(http.MethodGet, "/", nil)) {
		t.Fatal("a first-ever launch would have been served the supporter reminder")
	}

	// And once value exists and the cadence comes due, it does show.
	counter.RecordValueAnchor(true)
	for !counter.IsNagLaunch() {
		counter.Increment()
	}
	if !s.claimSupporterReminder(httptest.NewRequest(http.MethodGet, "/", nil)) {
		t.Fatal("the reminder never becomes due, so supporters would never be asked")
	}
}

// The dashboard is reloaded constantly and every load runs this check, so a
// reload must not bring the pricing page back — only the next server start
// can ask again.
func TestTheReminderIsServedOncePerServerStart(t *testing.T) {
	dir := t.TempDir()
	counter := license.NewLaunchCounter(dir)
	counter.Increment()
	counter.RecordValueAnchor(true)
	for !counter.IsNagLaunch() {
		counter.Increment()
	}

	s := &Server{cfg: &config.Config{}, launchCounter: counter}
	if !s.claimSupporterReminder(httptest.NewRequest(http.MethodGet, "/", nil)) {
		t.Fatal("the first page load of a due launch should show the reminder")
	}
	for i := 0; i < 5; i++ {
		if s.claimSupporterReminder(httptest.NewRequest(http.MethodGet, "/", nil)) {
			t.Fatalf("reload %d was served the reminder again", i+1)
		}
	}

	// A fresh server start on the same due launch asks once more.
	restarted := &Server{cfg: &config.Config{}, launchCounter: counter}
	if !restarted.claimSupporterReminder(httptest.NewRequest(http.MethodGet, "/", nil)) {
		t.Fatal("restarting the server should let the reminder ask again")
	}
}

// Concurrent page loads must not each get their own reminder.
func TestConcurrentLoadsClaimTheReminderOnce(t *testing.T) {
	dir := t.TempDir()
	counter := license.NewLaunchCounter(dir)
	counter.Increment()
	counter.RecordValueAnchor(true)
	for !counter.IsNagLaunch() {
		counter.Increment()
	}

	s := &Server{cfg: &config.Config{}, launchCounter: counter}
	var shown atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s.claimSupporterReminder(httptest.NewRequest(http.MethodGet, "/", nil)) {
				shown.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := shown.Load(); got != 1 {
		t.Fatalf("the reminder was served %d times across concurrent loads, want 1", got)
	}
}

// The supporter column listed two features that are free and ungated, on a
// page whose own free column says "every feature unlocked". Nothing in Coral
// is paywalled — license.Middleware passes every request through — so any
// copy implying otherwise is false, not merely oversold.
func TestActivationPageClaimsNoPaidFeatureGate(t *testing.T) {
	rr := httptest.NewRecorder()
	(&Server{}).serveActivation(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rr.Body.String()

	mustNotContain := map[string]string{
		"Agent team templates &amp; sharing": "free feature listed as a supporter benefit",
		"Search chat history":                "free feature listed as a supporter benefit, and full-text search does not work at all",
		"Lifetime license":                   "meaningless on a product where everyone gets updates",
		"Early adopter":                      "manufactured urgency implying future features will be paid",
		"Activates on 1 machine":             "an unverified constraint on a paid product",
	}
	for phrase, why := range mustNotContain {
		if strings.Contains(body, phrase) {
			t.Errorf("activation page still contains %q — %s", phrase, why)
		}
	}

	// The honest benefits must survive, or the page stops explaining what the
	// license is actually for.
	for _, phrase := range []string{
		"Retires this periodic reminder",
		"Priority support",
		"Priority consideration for feature requests",
		"Directly funds ongoing development",
	} {
		if !strings.Contains(body, phrase) {
			t.Errorf("activation page is missing the supporter benefit %q", phrase)
		}
	}

	// The line that makes the whole page coherent.
	if !strings.Contains(body, "because nothing is locked") {
		t.Error("activation page no longer states that nothing is locked")
	}
	// A free path has to stay visible and equally weighted.
	if !strings.Contains(body, "Continue Free") {
		t.Error("activation page has no visible free path")
	}
	// The tab title should not read as a paywall on a free product.
	if strings.Contains(body, "<title>Coral — Activate License</title>") {
		t.Error("page title still reads as a paywall")
	}
}
