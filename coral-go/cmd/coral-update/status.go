package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
	"unicode"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/updater/runrecord"
	"github.com/subgentic/coral/internal/updater/stage"
	"github.com/subgentic/coral/internal/updater/verify"
)

// "coral-update status [--json]" says what the updater knows (#5420), for the rollout and for a person asking why nothing happened. It only reads: the state file and the
// run record in <data dir>/updates, no network, no trusted root, nothing written, and it answers with the feature switched off too (it says it is off, and why).
// The JSON holds words, versions and times only: a stage or a result is a fixed word (anything else is "other"), and the stored text of a refusal (a Go error message, which
// can carry a home path, a hash or a URL) is NOT in it. The plain text, which is for the person at their own terminal, may show that stored text, as printable text of
// bounded length.

type statusOut struct {
	Feature      string `json:"feature"`              // on or off
	OffReason    string `json:"off_reason,omitempty"` // build_tier, no_version or env_switch
	ApplyEnabled bool   `json:"apply_enabled"`        // the apply's off switch (#5325)
	Installed    string `json:"installed,omitempty"`  // the version this program was built as
	StateFile    string `json:"state_file"`           // present, missing or unreadable
	Accepted     *struct {
		Version  string `json:"version,omitempty"`
		Sequence int64  `json:"sequence"`
	} `json:"accepted,omitempty"` // the last version that came up and its promote sequence
	Revoked     int          `json:"revoked"`
	Staged      *statusStage `json:"staged,omitempty"`
	LastCheck   string       `json:"last_check,omitempty"`
	LastFailure *statusFail  `json:"last_failure,omitempty"`
	Backoff     *statusBack  `json:"backoff,omitempty"`
	LastApply   *statusApply `json:"last_apply,omitempty"`
	LastRolled  *statusApply `json:"last_rollback,omitempty"`
	RunRecord   string       `json:"run_record"` // present, missing or unreadable (Linux: how Coral is started again after an update)
}

type statusStage struct {
	Version  string `json:"version"`
	StagedAt string `json:"staged_at,omitempty"`
}

type statusFail struct {
	Version string `json:"version,omitempty"`
	Stage   string `json:"stage"`
	Reason  string `json:"-"` // the stored text: plain output only, never the JSON
	At      string `json:"at,omitempty"`
}

type statusBack struct {
	Version  string `json:"version,omitempty"`
	Until    string `json:"until"`
	Waiting  bool   `json:"waiting"` // until is still in the future
	Attempts int    `json:"attempts,omitempty"`
	Kind     string `json:"kind,omitempty"` // network or verify
}

type statusApply struct {
	At     string `json:"at"`
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
	By     string `json:"by,omitempty"`
	Result string `json:"result"`
	Reason string `json:"reason,omitempty"`
}

// maxStatusText bounds any text taken from the state.
const maxStatusText = 200

// text keeps display text: printable, one line, bounded.
func text(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, s)
	if r := []rune(strings.TrimSpace(s)); len(r) > maxStatusText {
		return string(r[:maxStatusText]) + "..."
	}
	return strings.TrimSpace(s)
}

// word keeps a fixed word (lower-case letters, digits and underscores, at most 40): a stage, a result, a kind. Anything else is "other".
func word(s string) string {
	if s == "" {
		return ""
	}
	if len(s) > 40 {
		return "other"
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
			return "other"
		}
	}
	return s
}

// ver keeps a version (verify.ParseVersion accepts it) as it is, and shows anything else as "unknown": a hand-edited state cannot put text into a version field.
func ver(s string) string {
	if s == "" {
		return ""
	}
	if _, err := verify.ParseVersion(s); err != nil {
		return "unknown"
	}
	return s
}

// by keeps who started an attempt: "local" or a hub pseudonym, else "other".
func by(s string) string {
	if s == "" || s == "local" || hubBy.MatchString(s) {
		return s
	}
	return "other"
}

var hubBy = regexp.MustCompile(`^hub:hub-user-[0-9a-f]{8}$`)

// timeText keeps an RFC 3339 time as written, or nothing when it is not one.
func timeText(s string) string {
	if _, err := time.Parse(time.RFC3339, s); err != nil {
		return ""
	}
	return s
}

func collectStatus(getenv func(string) string, dataDir string, now time.Time) statusOut {
	out := statusOut{Feature: "on", ApplyEnabled: applyOn(runtime.GOOS), Installed: config.Version}
	switch {
	case !config.TierChecksForUpdates:
		out.Feature, out.OffReason = "off", "build_tier"
	case config.Version == "":
		out.Feature, out.OffReason = "off", "no_version"
	case getenv("CORAL_AUTO_UPDATE") == "off":
		out.Feature, out.OffReason = "off", "env_switch"
	}
	dir := filepath.Join(dataDir, stage.UpdatesDir)

	st, err := verify.LoadState(dir)
	switch {
	case err == nil:
		out.StateFile = "present"
		if _, serr := os.Lstat(filepath.Join(dir, verify.StateFile)); errors.Is(serr, fs.ErrNotExist) {
			out.StateFile = "missing" // a first run: nothing has checked yet
		}
	default:
		out.StateFile = "unreadable" // a zero state follows: nothing below is claimed
	}
	if st.Version != "" || st.Sequence != 0 {
		out.Accepted = &struct {
			Version  string `json:"version,omitempty"`
			Sequence int64  `json:"sequence"`
		}{Version: st.Version, Sequence: st.Sequence}
	}
	out.Revoked = len(st.Revoked)
	out.LastCheck = timeText(st.LastCheck)
	if f := st.LastFailure; f != nil {
		out.LastFailure = &statusFail{Version: ver(f.Version), Stage: word(f.Stage), Reason: text(f.Reason), At: timeText(f.At)}
	}
	if s := st.Stage; s != nil {
		if s.Staged != "" {
			out.Staged = &statusStage{Version: ver(s.Staged), StagedAt: timeText(s.StagedAt)}
		}
		if until := timeText(s.NextAttempt); until != "" {
			t, _ := time.Parse(time.RFC3339, until)
			out.Backoff = &statusBack{Version: ver(s.Version), Until: until, Waiting: t.After(now), Attempts: s.Attempts, Kind: word(s.ErrorKind)}
		}
	}
	for i := len(st.History) - 1; i >= 0; i-- {
		h := st.History[i]
		a := &statusApply{At: timeText(h.At), From: ver(h.From), To: ver(h.To), By: by(h.By), Result: word(h.Result), Reason: word(h.Reason)}
		if out.LastApply == nil {
			out.LastApply = a
		}
		if out.LastRolled == nil && (h.Result == "failed_rolled_back" || h.Result == "restart_failed") { // restart_failed: rolled back, and the previous Coral could not be started again (#5419)
			out.LastRolled = a
		}
		if out.LastApply != nil && out.LastRolled != nil {
			break
		}
	}

	out.RunRecord = "missing"
	p := filepath.Join(dir, runrecord.File)
	if fi, err := os.Lstat(p); err == nil {
		out.RunRecord = "unreadable"
		if fi.Mode().IsRegular() {
			if _, err := verify.ReadPrivate(p); err == nil {
				out.RunRecord = "present"
			}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		out.RunRecord = "unreadable"
	}
	return out
}

// plain is the status in words.
func (s statusOut) plain(w io.Writer) {
	p := func(format string, a ...any) { fmt.Fprintf(w, format+"\n", a...) }
	if s.Feature == "on" {
		p("Automatic updates: on")
	} else {
		p("Automatic updates: off (%s)", map[string]string{"build_tier": "this build does not update itself", "no_version": "this build has no version",
			"env_switch": "CORAL_AUTO_UPDATE=off"}[s.OffReason])
	}
	if s.ApplyEnabled {
		p("Installing an update: enabled")
	} else {
		p("Installing an update: not enabled yet in this build (updates are downloaded and checked, not installed)")
	}
	if s.Installed != "" {
		p("Installed version: %s", s.Installed)
	}
	switch s.StateFile {
	case "missing":
		p("The updater has not run yet: it has no record of a check.")
	case "unreadable":
		p("The updater's record cannot be read, so nothing below is known; the next check starts again.")
	}
	if s.Accepted != nil {
		p("Last version that started and was accepted: %s (promote sequence %d)", orNone(s.Accepted.Version), s.Accepted.Sequence)
	}
	if s.Revoked > 0 {
		p("Versions revoked: %d", s.Revoked)
	}
	if s.Staged != nil {
		p("Ready to install: %s (downloaded and checked %s)", s.Staged.Version, orNone(s.Staged.StagedAt))
	} else if s.StateFile == "present" {
		p("Ready to install: nothing")
	}
	p("Last check: %s", orNone(s.LastCheck))
	if s.LastFailure != nil {
		p("Last refusal: version %s stopped at %s%s%s", orNone(s.LastFailure.Version), s.LastFailure.Stage, atText(s.LastFailure.At), reasonText(s.LastFailure.Reason))
	}
	if b := s.Backoff; b != nil {
		if b.Waiting {
			p("Waiting before trying version %s again until %s (%d failure(s) in a row, %s)", orNone(b.Version), b.Until, b.Attempts, orNone(b.Kind))
		} else {
			p("The wait before trying version %s again ended at %s", orNone(b.Version), b.Until)
		}
	}
	if a := s.LastApply; a != nil {
		p("Last install attempt: %s -> %s, %s%s%s", orNone(a.From), orNone(a.To), a.Result, atText(a.At), reasonText(a.Reason))
	} else {
		p("Last install attempt: none")
	}
	if r := s.LastRolled; r != nil {
		if r.Result == "restart_failed" {
			p("Last roll-back: version %s did not come up%s; the previous version was put back but Coral could NOT be started again, so nothing was running afterwards (stopped at: %s)", orNone(r.To), atText(r.At), orNone(r.Reason))
		} else {
			p("Last roll-back: version %s did not come up%s; the previous version was put back (stopped at: %s)", orNone(r.To), atText(r.At), orNone(r.Reason))
		}
	}
	switch s.RunRecord {
	case "present":
		p("Run record (how Coral is started again after an update, Linux): present")
	case "missing":
		p("Run record (how Coral is started again after an update, Linux): not there (written when Coral starts; not used on macOS or Windows)")
	default:
		p("Run record (how Coral is started again after an update, Linux): present but unreadable")
	}
}

func orNone(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

func atText(at string) string {
	if at == "" {
		return ""
	}
	return " (" + at + ")"
}

func reasonText(r string) string {
	if r == "" {
		return ""
	}
	return ": " + r
}

// runStatus answers "status" and "status --json"; the caller has checked the argv.
func runStatus(args []string, getenv func(string) string, stdout io.Writer) int {
	out := collectStatus(getenv, config.ResolveCoralDir(""), time.Now())
	if len(args) == 2 {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
		return 0
	}
	out.plain(stdout)
	return 0
}
