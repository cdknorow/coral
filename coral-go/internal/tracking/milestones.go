package tracking

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
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
	// Pending holds the frozen snapshot of a one-time event whose delivery has
	// been attempted but not accepted: the time of the original occurrence and
	// its allowlisted properties. Retries (in this process or a later one) send
	// exactly this snapshot, so a later trigger with different properties (for
	// example a successful task after a failed first one) can never rewrite the
	// original event. Removed once the service accepts the event, and cleared
	// when telemetry is switched off so an old event is never replayed.
	Pending map[string]pendingMilestone `json:"pending_events,omitempty"`
}

// pendingMilestone is one frozen one-time event.
type pendingMilestone struct {
	Timestamp string            `json:"timestamp"`
	Props     map[string]string `json:"props,omitempty"`
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

// milestoneInflight guards against two concurrent triggers both delivering the
// same one-time event. It is in-memory and only spans one delivery attempt.
var milestoneInflight = map[string]bool{}

// reserveMilestone records that a milestone has been reached (product state,
// always written) and reports whether its analytics event should be delivered
// now. It does NOT mark the event as sent: that happens only after the service
// accepts it (confirmMilestoneSent), so a failed delivery, an opt-out or a
// keyless build never consumes the install's one-time event.
//
// canSend must already include "telemetry enabled". If the state cannot be
// persisted it returns false, so a broken disk yields no events rather than
// one per launch.
func reserveMilestone(name string, canSend bool, props map[string]string) (send bool, snap pendingMilestone) {
	if !stateDirReady() {
		return false, snap
	}
	milestoneMu.Lock()
	defer milestoneMu.Unlock()

	s := loadMilestones()
	changed := false
	if s.Reached == nil {
		s.Reached = map[string]string{}
	}
	if _, ok := s.Reached[name]; !ok {
		s.Reached[name] = nowUTC()
		changed = true
	}
	// With telemetry off (by any means) nothing pending may survive: an old
	// event must not be replayed after a later opt-in.
	if !telemetryEnabled() && len(s.Pending) > 0 {
		s.Pending = nil
		changed = true
	}
	send = canSend && !milestoneInflight[name]
	if _, fired := s.Fired[name]; fired {
		send = false
	}
	if send {
		if s.Pending == nil {
			s.Pending = map[string]pendingMilestone{}
		}
		if _, ok := s.Pending[name]; !ok {
			s.Pending[name] = pendingMilestone{Timestamp: nowUTC(), Props: freezeProps(name, props)}
			changed = true
		}
		snap = s.Pending[name]
	}
	if changed {
		if err := saveMilestones(s); err != nil {
			logDeliveryFailure(name, 0, "milestone state not persisted: "+err.Error())
			return false, pendingMilestone{}
		}
	}
	if send {
		milestoneInflight[name] = true
	}
	return send, snap
}

// freezeProps validates props against the event's allowlist and keeps only
// what would actually be sent, as strings, so the stored snapshot is safe to
// persist and replay. The active-day key carries a date suffix after "|".
func freezeProps(name string, props map[string]string) map[string]string {
	event := name
	if i := strings.Index(name, "|"); i >= 0 {
		event = name[:i]
	}
	typed, ok := sanitizeProps(event, props)
	if !ok || len(typed) == 0 {
		return nil
	}
	out := make(map[string]string, len(typed))
	for k, v := range typed {
		out[k] = fmt.Sprint(v)
	}
	return out
}

// clearPendingMilestones drops every frozen, unsent one-time event. Called when
// telemetry is switched off; Reached (product state) is untouched.
func clearPendingMilestones() {
	if !stateDirReady() {
		return
	}
	milestoneMu.Lock()
	defer milestoneMu.Unlock()
	s := loadMilestones()
	if len(s.Pending) == 0 {
		return
	}
	s.Pending = nil
	if err := saveMilestones(s); err != nil {
		logDeliveryFailure("pending_milestones", 0, "pending state not cleared: "+err.Error())
	}
}

// releaseMilestone ends a reservation without marking the event sent (the
// delivery failed or was cancelled), leaving it eligible for the next trigger.
func releaseMilestone(name string) {
	milestoneMu.Lock()
	delete(milestoneInflight, name)
	milestoneMu.Unlock()
}

// confirmMilestoneSent persists that the service accepted the one-time event.
func confirmMilestoneSent(name string) {
	milestoneMu.Lock()
	defer milestoneMu.Unlock()
	delete(milestoneInflight, name)
	s := loadMilestones()
	if s.Fired == nil {
		s.Fired = map[string]string{}
	}
	if _, ok := s.Fired[name]; ok {
		return
	}
	s.Fired[name] = nowUTC()
	delete(s.Pending, name)
	if err := saveMilestones(s); err != nil {
		logDeliveryFailure(name, 0, "milestone sent-state not persisted: "+err.Error())
	}
}

// milestoneEventUUID is stable for an install, milestone and frozen snapshot
// time, so a retry in this process or a later one reuses the same PostHog event
// uuid and the service can drop a duplicate if an earlier attempt was in fact
// accepted. A fresh snapshot (after an opt-out cleared the old one) gets a new
// uuid and timestamp.
func milestoneEventUUID(name, snapshotTime string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("coral-milestone|"+getInstallID()+"|"+name+"|"+snapshotTime)).String()
}

// TrackOnce sends a funnel milestone event the first time it is delivered on
// this install and never again. Non-blocking and fire-and-forget: it never
// blocks or fails the caller. The milestone is always recorded as reached
// (product state); the one-time analytics event is marked sent only after the
// service accepts it, and never while telemetry is off or the build is keyless.
func TrackOnce(eventName string, extraProps map[string]string) {
	gen := telemetryGen.Load()
	asyncGo(func() {
		send, snap := reserveMilestone(eventName, posthogKeyPresent() && telemetryValid(gen), extraProps)
		if !send {
			return
		}
		// Send the frozen snapshot, not the current call's properties.
		if emitEvent(eventName, snap.Props, milestoneEventUUID(eventName, snap.Timestamp), snap.Timestamp, gen) {
			confirmMilestoneSent(eventName)
			return
		}
		releaseMilestone(eventName)
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
func trackReturnVisitSync(gen uint64) {
	firstOpen := recordFirstOpen()
	if firstOpen.IsZero() {
		return
	}
	if time.Since(firstOpen) <= returnWindow {
		return
	}
	send, snap := reserveMilestone(EventReturned24h, posthogKeyPresent() && telemetryValid(gen), nil)
	if !send {
		return
	}
	if emitEvent(EventReturned24h, snap.Props, milestoneEventUUID(EventReturned24h, snap.Timestamp), snap.Timestamp, gen) {
		confirmMilestoneSent(EventReturned24h)
		return
	}
	releaseMilestone(EventReturned24h)
}

func nowUTC() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
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
// known. ValueDelivered depends on SetCoralDir having been called first and
// reports false until it has, which would read as "no value delivered yet"
// rather than as "not configured" — a distinction the caller cannot see.
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
