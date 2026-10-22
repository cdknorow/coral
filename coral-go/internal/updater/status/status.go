// Package status is the shape of "coral-update status --json" (#5420): the one answer the updater gives about itself, written by cmd/coral-update and read by the server's relaunch
// routes (#5280). It holds words, versions and times only, and no dependency on the verifier, so the server may link it (the verifier is linked only into coral-update).
package status

// Out is the whole of the JSON.
type Out struct {
	Feature      string    `json:"feature"`              // on or off
	OffReason    string    `json:"off_reason,omitempty"` // build_tier, no_version or env_switch
	ApplyEnabled bool      `json:"apply_enabled"`        // the apply's off switch (#5325)
	Installed    string    `json:"installed,omitempty"`  // the version this program was built as
	StateFile    string    `json:"state_file"`           // present, missing or unreadable
	Accepted     *Accepted `json:"accepted,omitempty"`   // the last version that came up and its promote sequence
	Revoked      int       `json:"revoked"`
	Staged       *Stage    `json:"staged,omitempty"`
	LastCheck    string    `json:"last_check,omitempty"`
	LastFailure  *Fail     `json:"last_failure,omitempty"`
	Backoff      *Back     `json:"backoff,omitempty"`
	LastApply    *Apply    `json:"last_apply,omitempty"`
	LastRolled   *Apply    `json:"last_rollback,omitempty"`
	RunRecord    string    `json:"run_record"`        // present, missing or unreadable (Linux: how Coral is started again after an update)
	Install      *Install  `json:"install,omitempty"` // what this install is and whether the updater can replace it (the status command fills it; nil when it could not be told)
}

// Accepted is the last version that started and was accepted.
type Accepted struct {
	Version  string `json:"version,omitempty"`
	Sequence int64  `json:"sequence"`
}

// Stage is the version that is downloaded and checked.
type Stage struct {
	Version  string `json:"version"`
	StagedAt string `json:"staged_at,omitempty"`
}

// Fail is the last refusal.
type Fail struct {
	Version string `json:"version,omitempty"`
	Stage   string `json:"stage"`
	Reason  string `json:"-"` // the stored text: plain output only, never the JSON
	At      string `json:"at,omitempty"`
}

// Back is a wait before the next attempt.
type Back struct {
	Version  string `json:"version,omitempty"`
	Until    string `json:"until"`
	Waiting  bool   `json:"waiting"` // until is still in the future
	Attempts int    `json:"attempts,omitempty"`
	Kind     string `json:"kind,omitempty"` // network or verify
}

// Apply is one install attempt.
type Apply struct {
	At     string `json:"at"`
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
	By     string `json:"by,omitempty"`
	Result string `json:"result"`
	Reason string `json:"reason,omitempty"`
}

// Install is how this Coral was installed, as the updater's own platform code finds it: Kind is one of app, tarball, homebrew or unwritable; Unsupported is the apply's fixed word for an
// install it will not touch ("" when it will); Writable is whether this user can replace it.
type Install struct {
	Kind        string `json:"kind"`
	Unsupported string `json:"unsupported,omitempty"`
	Writable    bool   `json:"writable"`
}

// ReadyFile is the marker, in <data dir>/updates, that "coral-update apply" writes once everything that can be done while Coral still runs is done: the staged package verified again, copied,
// built into a new install beside the old one and that one verified where it will run (#5982). The server that started the apply ends itself only when it sees this, so a package that fails any
// of those steps never costs the person their running Coral. It is removed when the apply ends.
const ReadyFile = "ready-to-swap.json"

// Ready is the marker's content: the version that is ready, and the pid of the apply that says so (the server accepts only the apply it started).
type Ready struct {
	Version string `json:"version"`
	PID     int    `json:"pid"`
	At      string `json:"at,omitempty"`
}
