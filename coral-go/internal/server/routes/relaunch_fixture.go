//go:build relaunchfixture

package routes

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/subgentic/coral/internal/safego"
	"github.com/subgentic/coral/internal/secfile"
)

// The relaunch test seam (#5452), coral-hub's way to prove the whole relaunch path against a real Coral without a real updater. It exists only in a build made with the
// relaunchfixture tag (nothing that ships builds it: a lint holds that). The fake updater is DATA ONLY: it reads ONE fixture file from a folder named by
// CORAL_RELAUNCH_FIXTURE_DIR, in the vocabularies the real status already allows (no path, URL, command or free text is accepted), writes one marker file when a relaunch
// is applied, and, only if the fixture says so, ends the process with code 0 once the reply is sent. It runs nothing and reads nothing else.
const (
	fixtureDirEnv    = "CORAL_RELAUNCH_FIXTURE_DIR"
	fixtureFile      = "relaunch.json"
	fixtureMarker    = "apply.marker.json"
	fixtureMaxBytes  = 16 << 10
	fixtureTrustWord = "relaunchfixture" // the trust fields are fixed words, not fixture content
)

// fixtureDoc is the fixture: every field optional, none unknown (strict).
type fixtureDoc struct {
	Enabled           bool   `json:"enabled"`
	Installed         string `json:"installed"`
	State             string `json:"state"`
	Supported         bool   `json:"supported"`
	TmuxAgentsRestart bool   `json:"tmux_agents_restart"`
	Staged            *struct {
		Version  string `json:"version"`
		StagedAt string `json:"staged_at"`
		Verified bool   `json:"verified"`
	} `json:"staged"`
	ManifestSequence int64  `json:"manifest_sequence"`
	HubRelaunched    string `json:"hub_relaunched"`
	LastHubRelaunch  string `json:"last_hub_relaunch"`
	LastResult       *struct {
		At     string `json:"at"`
		From   string `json:"from"`
		To     string `json:"to"`
		Result string `json:"result"`
		By     string `json:"by"`
	} `json:"last_result"`
	CheckedAt    string `json:"checked_at"`
	InstallKind  string `json:"install_kind"`
	NotReady     string `json:"not_ready_reason"`
	Latest       string `json:"latest"`
	LastRollback *struct {
		At            string `json:"at"`
		From          string `json:"from"`
		To            string `json:"to"`
		Reason        string `json:"reason"`
		RestartFailed bool   `json:"restart_failed"`
	} `json:"last_rollback"`
	// Apply says what a relaunch does: Exit ends the process (code 0) after the reply; StateAfter is the state the status shows from then on (applying, failed, none or staged).
	Apply struct {
		Exit       bool   `json:"exit"`
		StateAfter string `json:"state_after"`
	} `json:"apply"`
}

// fixtureDir is the folder named by CORAL_RELAUNCH_FIXTURE_DIR, checked: absolute, not a link, a folder, mode 0700, owned by this user.
func fixtureDir() (string, error) {
	d := os.Getenv(fixtureDirEnv)
	if d == "" {
		return "", fmt.Errorf("%s is not set", fixtureDirEnv)
	}
	if !filepath.IsAbs(d) {
		return "", fmt.Errorf("%s must be an absolute path", fixtureDirEnv)
	}
	fi, err := os.Lstat(d)
	if err != nil {
		return "", fmt.Errorf("the fixture folder: %w", err)
	}
	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		return "", errors.New("the fixture folder is a link")
	case !fi.IsDir():
		return "", errors.New("the fixture folder is not a folder")
	case fi.Mode().Perm() != 0o700:
		return "", fmt.Errorf("the fixture folder is mode %04o, it must be 0700 (owner only)", fi.Mode().Perm())
	}
	if uid, ok := ownerUID(fi); !ok || uid != fixtureUID() {
		return "", errors.New("the fixture folder is not owned by this user")
	}
	return d, nil
}

// readFixture reads and validates the fixture file: a regular file of mode 0600 owned by this user, opened without following a link, at most fixtureMaxBytes, strict JSON,
// every value in the vocabulary the real status allows.
func readFixture(dir string) (*fixtureDoc, error) {
	path := filepath.Join(dir, fixtureFile)
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("the fixture: %w", err)
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
		return nil, errors.New("the fixture is not a regular file of mode 0600")
	}
	if uid, ok := ownerUID(fi); !ok || uid != fixtureUID() {
		return nil, errors.New("the fixture is not owned by this user")
	}
	f, err := secfile.OpenNoFollow(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, fmt.Errorf("the fixture: %w", err)
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0o600 {
		return nil, errors.New("the fixture is not a regular file of mode 0600")
	}
	b, err := io.ReadAll(io.LimitReader(f, fixtureMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > fixtureMaxBytes {
		return nil, fmt.Errorf("the fixture is larger than %d bytes", fixtureMaxBytes)
	}
	var d fixtureDoc
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("the fixture: %w", err)
	}
	if dec.More() {
		return nil, errors.New("the fixture holds more than one document")
	}
	if err := d.validate(); err != nil {
		return nil, fmt.Errorf("the fixture: %w", err)
	}
	return &d, nil
}

func fixtureTime(field, s string, required bool) error {
	if s == "" {
		if required {
			return fmt.Errorf("%s is required", field)
		}
		return nil
	}
	if _, err := parseFixtureTime(s); err != nil {
		return fmt.Errorf("%s: %w", field, err)
	}
	return nil
}

func parseFixtureTime(s string) (time.Time, error) {
	if len(s) == 0 || s[len(s)-1] != 'Z' {
		return time.Time{}, fmt.Errorf("%q is not an RFC 3339 UTC time", s)
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}

func fixtureVersion(field, s string, required bool) error {
	if s == "" && !required {
		return nil
	}
	if !updateVersionRe.MatchString(s) {
		return fmt.Errorf("%s %q is not a version", field, s)
	}
	return nil
}

func fixtureWord(field, s string, allowed map[string]bool) error {
	if s != "" && !allowed[s] {
		return fmt.Errorf("%s %q is not one of the words the status allows", field, s)
	}
	return nil
}

var (
	fixtureStates      = map[string]bool{updNone: true, updStaged: true, updApplying: true, updFailed: true}
	fixtureResults     = map[string]bool{resultRelaunched: true, resultRefused: true, resultRolledBack: true}
	fixtureStateAfters = map[string]bool{updNone: true, updStaged: true, updApplying: true, updFailed: true}
)

func (d *fixtureDoc) validate() error {
	if d.State == "" {
		d.State = updNone
	}
	checks := []error{
		fixtureVersion("installed", d.Installed, true),
		fixtureWord("state", d.State, fixtureStates),
		fixtureVersion("hub_relaunched", d.HubRelaunched, false),
		fixtureTime("last_hub_relaunch", d.LastHubRelaunch, false),
		fixtureTime("checked_at", d.CheckedAt, false),
		fixtureWord("install_kind", d.InstallKind, installKinds),
		fixtureWord("not_ready_reason", d.NotReady, notReadyReasons),
		fixtureVersion("latest", d.Latest, false),
		fixtureWord("apply.state_after", d.Apply.StateAfter, fixtureStateAfters),
	}
	if d.ManifestSequence < 0 {
		checks = append(checks, errors.New("manifest_sequence is below 0"))
	}
	if s := d.Staged; s != nil {
		checks = append(checks, fixtureVersion("staged.version", s.Version, true), fixtureTime("staged.staged_at", s.StagedAt, true))
	}
	if r := d.LastResult; r != nil {
		checks = append(checks, fixtureTime("last_result.at", r.At, true), fixtureVersion("last_result.from", r.From, true), fixtureVersion("last_result.to", r.To, true),
			fixtureWord("last_result.result", r.Result, fixtureResults))
		if r.Result == "" {
			checks = append(checks, errors.New("last_result.result is required"))
		}
		if !hubByRe.MatchString(r.By) {
			checks = append(checks, fmt.Errorf("last_result.by %q is not local or a hub pseudonym", r.By))
		}
	}
	if r := d.LastRollback; r != nil {
		checks = append(checks, fixtureTime("last_rollback.at", r.At, true), fixtureVersion("last_rollback.from", r.From, true), fixtureVersion("last_rollback.to", r.To, true),
			fixtureWord("last_rollback.reason", r.Reason, rollbackReasons))
	}
	for _, e := range checks {
		if e != nil {
			return e
		}
	}
	return nil
}

// CheckRelaunchFixture is the startup check of a relaunchfixture build: the fixture folder and file must be valid, or the server does not start.
func CheckRelaunchFixture() error {
	dir, err := fixtureDir()
	if err != nil {
		return err
	}
	_, err = readFixture(dir)
	return err
}

// fixturePseudonymRe is the form of the hub user the handler hands to Apply (the pseudonym, without the "hub:" a history entry adds).
var fixturePseudonymRe = regexp.MustCompile(`^hub-user-[0-9a-f]{8}$`)

// exitProcess ends the process after a relaunch whose fixture says so (a test replaces it).
var exitProcess = os.Exit

// fixtureUpdater is the fake updater: RelaunchUpdater answered from the fixture, re-read at every call so a test can change it between requests.
type fixtureUpdater struct {
	mu      sync.Mutex
	applied *struct {
		version string
		at      time.Time
		state   string
	}
	audits []RelaunchAudit
}

func (u *fixtureUpdater) Record(ctx context.Context) (UpdaterRecord, error) {
	dir, err := fixtureDir()
	if err != nil {
		return UpdaterRecord{}, err
	}
	d, err := readFixture(dir)
	if err != nil {
		return UpdaterRecord{}, err
	}
	rec := UpdaterRecord{
		Enabled: d.Enabled, Installed: d.Installed, State: d.State, Supported: d.Supported, TmuxAgentsRestart: d.TmuxAgentsRestart,
		HubRelaunched: d.HubRelaunched, InstallKind: d.InstallKind, NotReady: d.NotReady, Latest: d.Latest,
		Trust: UpdateTrust{Issuer: fixtureTrustWord, Signer: fixtureTrustWord, ManifestSequence: d.ManifestSequence},
	}
	now := time.Now().UTC()
	rec.Trust.PromotedAt, rec.Trust.ExpireAt = now.Add(-24*time.Hour), now.Add(24*time.Hour)
	if t, err := parseFixtureTime(d.LastHubRelaunch); err == nil {
		rec.LastHubRelaunch = t
	}
	if t, err := parseFixtureTime(d.CheckedAt); err == nil {
		rec.CheckedAt = t
	}
	if s := d.Staged; s != nil {
		at, _ := parseFixtureTime(s.StagedAt)
		rec.Staged = &StagedUpdate{Version: s.Version, StagedAt: at, Verified: s.Verified}
	}
	if r := d.LastResult; r != nil {
		at, _ := parseFixtureTime(r.At)
		rec.LastResult = &UpdateResult{At: at, From: r.From, To: r.To, Result: r.Result, By: r.By}
	}
	if r := d.LastRollback; r != nil {
		at, _ := parseFixtureTime(r.At)
		rec.LastRollback = &UpdateRollback{At: at, From: r.From, To: r.To, Reason: r.Reason, RestartFailed: r.RestartFailed}
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if a := u.applied; a != nil { // a relaunch was applied in this process: the status shows it
		rec.HubRelaunched, rec.LastHubRelaunch = a.version, a.at
		if a.state != "" {
			rec.State = a.state
		}
	}
	return rec, nil
}

// Apply writes the marker (operation id, the acknowledged version, the hub pseudonym) into the fixture folder, atomically and 0600, and, only if the fixture says so, ends the
// process with code 0 half a second later, after the reply has gone. It executes nothing and reads nothing else.
func (u *fixtureUpdater) Apply(ctx context.Context, by string) (string, error) {
	if !fixturePseudonymRe.MatchString(by) {
		return "", errors.New("not a hub pseudonym")
	}
	dir, err := fixtureDir()
	if err != nil {
		return "", err
	}
	d, err := readFixture(dir)
	if err != nil {
		return "", err
	}
	if d.State != updStaged || d.Staged == nil {
		return "", errors.New("nothing is staged in the fixture")
	}
	id := make([]byte, 8)
	if _, err := rand.Read(id); err != nil {
		return "", err
	}
	op := "fx-" + hex.EncodeToString(id)
	b, err := json.Marshal(map[string]string{"operation": op, "version": d.Staged.Version, "by": by, "at": time.Now().UTC().Format(time.RFC3339)})
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".marker-*")
	if err != nil {
		return "", err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return "", err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(name, filepath.Join(dir, fixtureMarker)); err != nil {
		return "", err
	}
	u.mu.Lock()
	u.applied = &struct {
		version string
		at      time.Time
		state   string
	}{d.Staged.Version, time.Now().UTC(), d.Apply.StateAfter}
	u.mu.Unlock()
	if d.Apply.Exit {
		go func() {
			defer safego.Recover("routes/relaunch_fixture:exit")
			time.Sleep(500 * time.Millisecond)
			exitProcess(0)
		}()
	}
	return op, nil
}

// Audit keeps the attempts in memory (the last 50): the fixture updater writes nothing but the marker.
func (u *fixtureUpdater) Audit(ctx context.Context, a RelaunchAudit) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.audits = append(u.audits, a)
	if len(u.audits) > 50 {
		u.audits = u.audits[len(u.audits)-50:]
	}
	return nil
}

// Check: the fixture has no network and runs nothing; a check ends at once with nothing to say.
func (f *fixtureUpdater) Check(context.Context) (string, error) { return "none", nil }
