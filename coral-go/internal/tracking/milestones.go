package tracking

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/cdknorow/coral/internal/config"
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
	// Fired maps a milestone event name to the RFC3339 UTC time it fired.
	Fired map[string]string `json:"fired,omitempty"`
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
	var s milestoneState
	data, err := os.ReadFile(milestonesPath())
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

// markMilestone records that a milestone has been reached. It returns true
// only the first time it is called for a given name on this install; every
// later call returns false. If the state cannot be persisted, it returns
// false so a broken disk produces no events rather than an event per launch.
func markMilestone(name string) bool {
	milestoneMu.Lock()
	defer milestoneMu.Unlock()

	s := loadMilestones()
	if s.Fired == nil {
		s.Fired = map[string]string{}
	}
	if _, ok := s.Fired[name]; ok {
		return false
	}
	s.Fired[name] = nowUTC()
	if err := saveMilestones(s); err != nil {
		logDeliveryFailure(name, 0, "milestone state not persisted: "+err.Error())
		return false
	}
	return true
}

// TrackOnce sends a funnel milestone event the first time it happens on this
// install and never again. Non-blocking and fire-and-forget: it never blocks
// or fails the caller, matching the rest of this package.
func TrackOnce(eventName string, extraProps map[string]string) {
	// Builds without a PostHog key send nothing, so do not burn the milestone —
	// otherwise a source build would permanently consume the "first" event.
	if config.PostHogKey == "" {
		return
	}
	asyncGo(func() {
		if !markMilestone(eventName) {
			return
		}
		trackEventSync(eventName, extraProps)
	})
}

// recordFirstOpen stores the first-open timestamp if it is not already set and
// returns it. A zero time means the timestamp is unknown or unwritable.
func recordFirstOpen() time.Time {
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
	if !markMilestone(EventReturned24h) {
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
	_, err := os.Stat(disclosurePath())
	return err == nil
}

// AcknowledgeDisclosure records that the user has seen the disclosure. It is
// idempotent.
func AcknowledgeDisclosure() error {
	dir := resolveCoralDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return os.WriteFile(disclosurePath(), []byte(nowUTC()+"\n"), 0600)
}
