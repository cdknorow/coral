package tracking

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Funnel milestone event names. Each fires at most once per install.
// These are IN ADDITION TO the per-occurrence events (session_launched,
// team_launched) — they are not replacements.
const (
	EventFirstAgentLaunched = "first_agent_launched"
	EventFirstTeamLaunched  = "first_team_launched"
	EventFirstTaskCompleted = "first_task_completed"
	EventReturned24h        = "returned_24h"
)

// returnWindow is how long after the first open a subsequent open counts as
// a "returned" user.
const returnWindow = 24 * time.Hour

// milestonesFileName is the on-disk record of one-time funnel milestones,
// stored alongside .install_id in the Coral data directory.
const milestonesFileName = ".milestones.json"

// milestoneState is the persisted funnel state. It holds no user content —
// only event names and timestamps.
type milestoneState struct {
	// FirstOpenAt is the RFC3339 UTC time of the first recorded app open.
	FirstOpenAt string `json:"first_open_at,omitempty"`
	// Fired maps a milestone event name to the RFC3339 UTC time its analytics
	// event was sent. Only written when analytics are configured, so a build
	// with no key never consumes an install's one-time events.
	Fired map[string]string `json:"fired,omitempty"`
	// Reached maps a milestone name to the RFC3339 UTC time it happened.
	// Unlike Fired this is product state, not analytics: it records that the
	// user actually did the thing, and is written whether or not analytics are
	// configured. It is what gates the supporter reminder, which must not
	// depend on an analytics key being present.
	Reached map[string]string `json:"reached,omitempty"`
}

// milestoneMu serialises read-modify-write of the milestones file so two
// concurrent launches cannot both decide they are "first".
var milestoneMu sync.Mutex

func milestonesPath() string {
	return filepath.Join(resolveCoralDir(), milestonesFileName)
}

// loadMilestones reads the milestone state. A missing or corrupt file yields
// an empty state — tracking never fails the caller.
func loadMilestones() milestoneState {
	return loadMilestonesAt(milestonesPath())
}

func loadMilestonesAt(path string) milestoneState {
	var s milestoneState
	data, err := os.ReadFile(path)
	if err != nil {
		return milestoneState{}
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return milestoneState{}
	}
	return s
}

// saveMilestones writes the milestone state atomically via a temp file so a
// crash mid-write cannot leave a truncated file that loses milestones.
func saveMilestones(s milestoneState) error {
	dir := resolveCoralDir()
	if dir == "" {
		return errors.New("tracking data directory is not configured")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, milestonesFileName+".*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0600); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, milestonesPath()); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// markMilestone records that a milestone has been reached and reports whether
// its analytics event should be sent now.
//
// Two records are kept, deliberately independent:
//   - Reached is product state, always written. It says the user did the thing.
//   - Fired is analytics state, written only when canSend is true. It says the
//     event was sent, so a build with no analytics key never consumes an
//     install's one-time events.
//
// The send result is true only the first time for a given name on an install.
// If the state cannot be persisted it returns false, so a broken disk produces
// no events rather than one per launch.
func markMilestone(name string, canSend bool) bool {
	if !stateDirReady() {
		return false
	}
	milestoneMu.Lock()
	defer milestoneMu.Unlock()

	s := loadMilestones()
	if s.Reached == nil {
		s.Reached = map[string]string{}
	}
	if s.Fired == nil {
		s.Fired = map[string]string{}
	}

	changed := false
	if _, ok := s.Reached[name]; !ok {
		s.Reached[name] = nowUTC()
		changed = true
	}

	send := false
	if _, ok := s.Fired[name]; !ok && canSend {
		s.Fired[name] = nowUTC()
		changed = true
		send = true
	}

	if !changed {
		return false
	}
	if err := saveMilestones(s); err != nil {
		logDeliveryFailure(name, 0, "milestone state not persisted: "+err.Error())
		return false
	}
	return send
}

// TrackOnce sends a funnel milestone event the first time it happens on this
// install and never again. Non-blocking and fire-and-forget: it never blocks
// or fails the caller, matching the rest of this package.
func TrackOnce(eventName string, extraProps map[string]string) {
	asyncGo(func() {
		// The milestone is recorded either way — it is product state that gates
		// the supporter reminder. The event is only sent, and only marked as
		// sent, on a build that has an analytics key, so a source build cannot
		// permanently consume an install's one-time events.
		if !markMilestone(eventName, posthogKeyPresent()) {
			return
		}
		trackEventSync(eventName, extraProps)
	})
}

// recordFirstOpen stores the first-open timestamp if it is not already set and
// returns it. A zero time means the timestamp is unknown or unwritable.
func recordFirstOpen() time.Time {
	if !stateDirReady() {
		return time.Time{}
	}
	milestoneMu.Lock()
	defer milestoneMu.Unlock()

	s := loadMilestones()
	if s.FirstOpenAt != "" {
		t, err := time.Parse(time.RFC3339, s.FirstOpenAt)
		if err != nil {
			return time.Time{}
		}
		return t
	}
	now := time.Now().UTC()
	s.FirstOpenAt = now.Format(time.RFC3339)
	if err := saveMilestones(s); err != nil {
		logDeliveryFailure("first_open", 0, "first-open timestamp not persisted: "+err.Error())
		return time.Time{}
	}
	return now
}

// trackReturnVisitSync records the first open and, when the current open is
// more than returnWindow after it, emits returned_24h at most once.
func trackReturnVisitSync() {
	firstOpen := recordFirstOpen()
	if firstOpen.IsZero() {
		return
	}
	if time.Since(firstOpen) <= returnWindow {
		return
	}
	if !markMilestone(EventReturned24h, posthogKeyPresent()) {
		return
	}
	trackEventSync(EventReturned24h, nil)
}

func nowUTC() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// ── Telemetry disclosure ─────────────────────────────────────────────────

// disclosureFileName records that the user has seen the telemetry disclosure.
// It sits alongside .install_id so it survives upgrades and is trivial to
// inspect or delete.
const disclosureFileName = ".telemetry_disclosed"

func disclosurePath() string {
	return filepath.Join(resolveCoralDir(), disclosureFileName)
}

// DisclosureAcknowledged reports whether the telemetry disclosure has been
// acknowledged on this install.
func DisclosureAcknowledged() bool {
	if !stateDirReady() {
		return false
	}
	_, err := os.Stat(disclosurePath())
	return err == nil
}

// AcknowledgeDisclosure records that the user has seen the disclosure. It is
// idempotent.
func AcknowledgeDisclosure() error {
	dir := resolveCoralDir()
	if dir == "" {
		return errors.New("tracking data directory is not configured")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return os.WriteFile(disclosurePath(), []byte(nowUTC()+"\n"), 0600)
}

// ── Demonstrated value ───────────────────────────────────────────────────

// ValueMilestones are the milestones that mean Coral has actually done
// something for the user. The supporter reminder is gated on one of these
// having happened, so the ask always follows a result rather than preceding it.
var ValueMilestones = []string{
	EventFirstAgentLaunched,
	EventFirstTaskCompleted,
}

// ValueDelivered reports whether Coral has produced a real result for this
// user yet, using the directory set by SetCoralDir.
func ValueDelivered() bool { return ValueDeliveredIn(resolveCoralDir()) }

// ValueDeliveredIn reports whether Coral has produced a real result for the
// install rooted at dir.
//
// Prefer this over ValueDelivered wherever the data directory is already
// known. ValueDelivered depends on SetCoralDir having been called first, and a
// caller that runs before it would silently read ~/.coral — letting one
// install's state decide another install's behaviour.
//
// It reads the same milestone state the funnel uses and works on builds with
// no analytics key: whether we ask a user for support is a product decision
// and must not depend on analytics being configured.
func ValueDeliveredIn(dir string) bool {
	if dir == "" {
		return false
	}
	milestoneMu.Lock()
	defer milestoneMu.Unlock()

	s := loadMilestonesAt(filepath.Join(dir, milestonesFileName))
	for _, name := range ValueMilestones {
		if _, ok := s.Reached[name]; ok {
			return true
		}
	}
	return false
}
