package tracking

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cdknorow/coral/internal/config"
)

type capturedEvent struct {
	Event      string         `json:"event"`
	DistinctID string         `json:"distinct_id"`
	Properties map[string]any `json:"properties"`
}

type recorder struct {
	mu     sync.Mutex
	events []capturedEvent
	status int
}

func (r *recorder) all() []capturedEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]capturedEvent, len(r.events))
	copy(out, r.events)
	return out
}

func (r *recorder) names() []string {
	var out []string
	for _, e := range r.all() {
		out = append(out, e.Event)
	}
	return out
}

func (r *recorder) count(name string) int {
	n := 0
	for _, e := range r.all() {
		if e.Event == name {
			n++
		}
	}
	return n
}

// newTestTracking isolates all tracking state in a temp dir and captures every
// event that would have been posted to PostHog.
func newTestTracking(t *testing.T) (*recorder, string) {
	t.Helper()
	dir := t.TempDir()

	rec := &recorder{status: http.StatusOK}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload capturedEvent
		json.NewDecoder(r.Body).Decode(&payload)
		rec.mu.Lock()
		rec.events = append(rec.events, payload)
		status := rec.status
		rec.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)

	prevURL, prevKey, prevDir, prevID, prevVer := posthogURL, config.PostHogKey, coralDir, cachedInstallID, config.Version
	posthogURL = srv.URL
	config.PostHogKey = "phc_test_key"
	coralDir = dir
	cachedInstallID = "install-under-test"
	installIDOnce.Do(func() {}) // consume the Once so getInstallID uses our value
	t.Cleanup(func() {
		waitForAsync()
		posthogURL, config.PostHogKey, coralDir, cachedInstallID, config.Version = prevURL, prevKey, prevDir, prevID, prevVer
	})

	os.WriteFile(filepath.Join(dir, ".install_id"), []byte("install-under-test"), 0600)
	return rec, dir
}

func TestTrackOnceEmitsOnlyOnceEvenWhenCalledRepeatedly(t *testing.T) {
	rec, _ := newTestTracking(t)

	TrackOnce(EventFirstAgentLaunched, nil)
	TrackOnce(EventFirstAgentLaunched, nil)
	TrackOnce(EventFirstAgentLaunched, nil)
	waitForAsync()

	if got := rec.count(EventFirstAgentLaunched); got != 1 {
		t.Fatalf("expected exactly 1 %s event, got %d (%v)", EventFirstAgentLaunched, got, rec.names())
	}
}

func TestTrackOnceSurvivesARestart(t *testing.T) {
	rec, dir := newTestTracking(t)

	TrackOnce(EventFirstTeamLaunched, nil)
	waitForAsync()

	// A restart loses all in-memory state; the milestone file must still gate.
	milestoneMu.Lock()
	milestoneMu.Unlock()
	coralDir = dir

	TrackOnce(EventFirstTeamLaunched, nil)
	waitForAsync()

	if got := rec.count(EventFirstTeamLaunched); got != 1 {
		t.Fatalf("expected exactly 1 %s event across a restart, got %d", EventFirstTeamLaunched, got)
	}
	if _, err := os.Stat(filepath.Join(dir, milestonesFileName)); err != nil {
		t.Fatalf("expected %s to be persisted: %v", milestonesFileName, err)
	}
}

func TestTrackOnceMilestonesAreIndependent(t *testing.T) {
	rec, _ := newTestTracking(t)

	for _, name := range []string{EventFirstAgentLaunched, EventFirstTeamLaunched, EventFirstTaskCompleted} {
		TrackOnce(name, nil)
		TrackOnce(name, nil)
	}
	waitForAsync()

	for _, name := range []string{EventFirstAgentLaunched, EventFirstTeamLaunched, EventFirstTaskCompleted} {
		if got := rec.count(name); got != 1 {
			t.Errorf("expected exactly 1 %s event, got %d", name, got)
		}
	}
}

func TestTrackOnceIsConcurrencySafe(t *testing.T) {
	rec, _ := newTestTracking(t)

	var wg sync.WaitGroup
	for i := 0; i < 25; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			TrackOnce(EventFirstTaskCompleted, nil)
		}()
	}
	wg.Wait()
	waitForAsync()

	if got := rec.count(EventFirstTaskCompleted); got != 1 {
		t.Fatalf("expected exactly 1 %s event under concurrency, got %d", EventFirstTaskCompleted, got)
	}
}

func TestTrackOnceWithoutPostHogKeySendsNothingAndDoesNotBurnTheMilestone(t *testing.T) {
	rec, _ := newTestTracking(t)
	config.PostHogKey = ""

	TrackOnce(EventFirstAgentLaunched, nil)
	waitForAsync()

	if got := len(rec.all()); got != 0 {
		t.Fatalf("expected no events without a PostHog key, got %d", got)
	}
	// The milestone is still recorded as reached — it is product state that
	// gates the supporter reminder — but it is not marked as sent.
	s := loadMilestones()
	if _, ok := s.Reached[EventFirstAgentLaunched]; !ok {
		t.Error("the milestone should be recorded as reached even with no analytics key")
	}
	if _, ok := s.Fired[EventFirstAgentLaunched]; ok {
		t.Error("the milestone must not be marked as sent when nothing was sent")
	}

	// Once a key is present the event is still available to fire.
	config.PostHogKey = "phc_test_key"
	TrackOnce(EventFirstAgentLaunched, nil)
	waitForAsync()
	if got := rec.count(EventFirstAgentLaunched); got != 1 {
		t.Fatalf("expected 1 %s event after the key appears, got %d", EventFirstAgentLaunched, got)
	}
}

func TestValueDeliveredTracksTheValueMilestones(t *testing.T) {
	newTestTracking(t)

	if ValueDelivered() {
		t.Fatal("a fresh install has not been delivered any value yet")
	}

	// A milestone that is not a value milestone does not count.
	TrackOnce(EventReturned24h, nil)
	waitForAsync()
	if ValueDelivered() {
		t.Fatal("returned_24h is not a demonstration of value")
	}

	TrackOnce(EventFirstAgentLaunched, nil)
	waitForAsync()
	if !ValueDelivered() {
		t.Fatal("launching the first agent should count as value delivered")
	}
}

// The supporter reminder is a product decision and must work on a build that
// sends no analytics at all.
func TestValueDeliveredWorksWithoutAnAnalyticsKey(t *testing.T) {
	newTestTracking(t)
	config.PostHogKey = ""

	TrackOnce(EventFirstTaskCompleted, nil)
	waitForAsync()

	if !ValueDelivered() {
		t.Fatal("value must be recorded even when analytics are not configured")
	}
}

func TestEveryEventCarriesTheStandardProperties(t *testing.T) {
	rec, _ := newTestTracking(t)
	config.Version = "9.9.9-test"

	TrackOnce(EventFirstTeamLaunched, map[string]string{"agent_count": "3"})
	waitForAsync()

	events := rec.all()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	props := events[0].Properties
	for _, key := range []string{"version", "edition", "os", "arch"} {
		if _, ok := props[key]; !ok {
			t.Errorf("event is missing standard property %q; got %v", key, props)
		}
	}
	if props["agent_count"] != "3" {
		t.Errorf("expected agent_count=3, got %v", props["agent_count"])
	}
	if events[0].DistinctID != "install-under-test" {
		t.Errorf("expected the install ID as distinct_id, got %q", events[0].DistinctID)
	}
}

func TestReturnVisitFiresOnceAfterTwentyFourHours(t *testing.T) {
	rec, dir := newTestTracking(t)

	// First open: records the timestamp, fires nothing.
	trackReturnVisitSync()
	if got := rec.count(EventReturned24h); got != 0 {
		t.Fatalf("expected no %s on the first open, got %d", EventReturned24h, got)
	}
	s := loadMilestones()
	if s.FirstOpenAt == "" {
		t.Fatal("expected the first-open timestamp to be recorded")
	}

	// An open the same day still does not count as a return.
	trackReturnVisitSync()
	if got := rec.count(EventReturned24h); got != 0 {
		t.Fatalf("expected no %s within the 24h window, got %d", EventReturned24h, got)
	}

	// Backdate the first open past the window.
	s.FirstOpenAt = time.Now().UTC().Add(-25 * time.Hour).Format(time.RFC3339)
	if err := saveMilestones(s); err != nil {
		t.Fatalf("saveMilestones: %v", err)
	}

	trackReturnVisitSync()
	trackReturnVisitSync()
	trackReturnVisitSync()

	if got := rec.count(EventReturned24h); got != 1 {
		t.Fatalf("expected exactly 1 %s event, got %d", EventReturned24h, got)
	}
	if _, err := os.Stat(filepath.Join(dir, milestonesFileName)); err != nil {
		t.Fatalf("expected the milestone file to exist: %v", err)
	}
}

func TestDeliveryFailuresAreRecordedLocally(t *testing.T) {
	rec, dir := newTestTracking(t)
	rec.mu.Lock()
	rec.status = http.StatusInternalServerError
	rec.mu.Unlock()

	TrackEvent("app_opened", nil)
	waitForAsync()

	data, err := os.ReadFile(filepath.Join(dir, "tracking-failures.log"))
	if err != nil {
		t.Fatalf("expected a local delivery-failure log: %v", err)
	}
	if !containsAll(string(data), "app_opened", "status=500") {
		t.Fatalf("failure log did not describe the failure: %q", string(data))
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// config_PostHogKeyTo sets the analytics key for the rest of the test.
// newTestTracking's cleanup restores the original.
func config_PostHogKeyTo(t *testing.T, key string) {
	t.Helper()
	config.PostHogKey = key
}

// Regression: the supporter reminder reads value state during server startup,
// which happens before SetCoralDir is called. Reading the ambient default
// there meant a test instance run with --home consulted the production
// install's milestones. ValueDeliveredIn must read only the directory it is
// given.
func TestValueDeliveredInReadsOnlyTheDirectoryItIsGiven(t *testing.T) {
	newTestTracking(t) // coralDir is the ambient temp dir for this test

	TrackOnce(EventFirstAgentLaunched, nil)
	waitForAsync()
	if !ValueDelivered() {
		t.Fatal("expected value delivered in the ambient directory")
	}

	other := t.TempDir()
	if ValueDeliveredIn(other) {
		t.Fatal("ValueDeliveredIn read state from outside the directory it was given")
	}
}

// Regression, and the reason there is no ~/.coral fallback any more: the test
// suite exercises the launch and task handlers, which call TrackOnce. With a
// guessed default directory those calls wrote milestone state into the user's
// real ~/.coral install — silently changing the supporter-reminder state of a
// production install from a unit test run.
func TestTrackingWritesNothingWhenNoDataDirectoryIsConfigured(t *testing.T) {
	prevDir, prevKey, prevID := coralDir, config.PostHogKey, cachedInstallID
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory to guard")
	}
	guarded := filepath.Join(home, ".coral")
	before := dirSnapshot(guarded)

	coralDir = "" // as if SetCoralDir had never been called
	config.PostHogKey = "phc_test_key"
	cachedInstallID = "install-under-test"
	t.Cleanup(func() {
		waitForAsync()
		coralDir, config.PostHogKey, cachedInstallID = prevDir, prevKey, prevID
	})

	TrackOnce(EventFirstAgentLaunched, nil)
	TrackOnce(EventFirstTaskCompleted, nil)
	TrackEvent(EventSessionLaunched, nil)
	trackReturnVisitSync()
	trackInstall()
	if err := AcknowledgeDisclosure(); err == nil {
		t.Error("AcknowledgeDisclosure should refuse when no data directory is configured")
	}
	waitForAsync()

	if after := dirSnapshot(guarded); after != before {
		t.Errorf("tracking wrote into %s with no data directory configured\nbefore: %v\nafter:  %v",
			guarded, before, after)
	}
	if ValueDelivered() {
		t.Error("ValueDelivered must not consult a guessed directory")
	}
	if DisclosureAcknowledged() {
		t.Error("DisclosureAcknowledged must not consult a guessed directory")
	}
}

// dirSnapshot lists the tracking state files in dir with their sizes, so a
// test can assert nothing about them changed.
func dirSnapshot(dir string) string {
	names := []string{".milestones.json", ".install_id", ".install_version", ".telemetry_disclosed", "tracking-failures.log"}
	var b []string
	for _, n := range names {
		fi, err := os.Stat(filepath.Join(dir, n))
		if err != nil {
			b = append(b, n+"=absent")
			continue
		}
		b = append(b, fmt.Sprintf("%s=%d@%d", n, fi.Size(), fi.ModTime().UnixNano()))
	}
	return strings.Join(b, " ")
}
