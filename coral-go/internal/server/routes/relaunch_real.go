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
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/subgentic/coral/internal/safego"
	"github.com/subgentic/coral/internal/updater/runrecord"
	"github.com/subgentic/coral/internal/updater/status"
)

// The real updater behind the relaunch routes (#5280, step 3 A): it runs nothing of its own. What the updater knows is read from "coral-update status --json" (a fixed argv, the program
// that sits beside this one, no input from a request) and an update is started by running "coral-update apply" (the same, no arguments), which waits for this server to end, replaces the
// install, checks the new one and starts it. This server then ends through its own graceful shutdown. The verifier is linked only into coral-update, never here.
//
// It is built into every release and used by nothing there: the routes answer 404 while updateRelaunchFeature is false (relaunch_feature_off.go), and coral-update's apply is off per platform.

const (
	relaunchStateFile = "relaunch.json"
	relaunchAuditFile = "relaunch_audit.json"
	maxAuditRows      = 50
	maxStateBytes     = 64 << 10
	statusTimeout     = 10 * time.Second
	statusMaxBytes    = 64 << 10
	recordCacheFor    = 2 * time.Second
	// applyingFor is how long a started relaunch counts as running when the updater has recorded no result for it.
	applyingFor = 20 * time.Minute
	// quitAfter gives the reply to the relaunch request time to leave before this server ends.
	quitAfter = 2 * time.Second
)

// updaterProgram is the two things the updater program is asked: what it knows, and to apply (a test replaces it; the real one is execProgram).
type updaterProgram interface {
	Status(ctx context.Context) ([]byte, error)
	StartApply() error
}

// relaunchState is what this updater itself keeps: the version a hub relaunch was started for (once per version) and when, and the attempt in flight.
type relaunchState struct {
	HubRelaunched   string    `json:"hub_relaunched,omitempty"`
	LastHubRelaunch time.Time `json:"last_hub_relaunch,omitempty"`
	StartedAt       time.Time `json:"started_at,omitempty"`
	From            string    `json:"from,omitempty"`
	To              string    `json:"to,omitempty"`
	Operation       string    `json:"operation,omitempty"`
	By              string    `json:"by,omitempty"`
}

type realUpdater struct {
	dir  updatesDir
	prog updaterProgram
	quit func()
	now  func() time.Time
	// quitDelay is how long after a started relaunch quit is called (quitAfter unless a test says otherwise).
	quitDelay time.Duration

	mu       sync.Mutex
	cached   UpdaterRecord
	cachedAt time.Time
	cachedOK bool
}

// NewRealUpdater is the updater of a server whose data folder is coralDir: the program beside this one answers its questions, and quit ends this server (its graceful shutdown) after
// a relaunch was started.
func NewRealUpdater(coralDir string, quit func()) RelaunchUpdater {
	return &realUpdater{dir: updatesDir{filepath.Join(coralDir, updatesDirName)}, prog: execProgram{}, quit: quit, now: time.Now}
}

var _ RelaunchUpdater = (*realUpdater)(nil)

func (u *realUpdater) clock() time.Time {
	if u.now != nil {
		return u.now()
	}
	return time.Now()
}

// Record is what the updater says now, in the relaunch routes' vocabulary. A second call within a moment is answered from the first (a status is a process to start).
func (u *realUpdater) Record(ctx context.Context) (UpdaterRecord, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.cachedOK && u.clock().Sub(u.cachedAt) < recordCacheFor && u.clock().After(u.cachedAt) {
		return u.cached, nil
	}
	rec, err := u.read(ctx)
	if err != nil {
		return UpdaterRecord{}, err
	}
	u.cached, u.cachedAt, u.cachedOK = rec, u.clock(), true
	return rec, nil
}

func (u *realUpdater) read(ctx context.Context) (UpdaterRecord, error) {
	raw, err := u.prog.Status(ctx)
	if err != nil {
		return UpdaterRecord{}, fmt.Errorf("the updater did not answer: %w", err)
	}
	var out status.Out
	if err := json.Unmarshal(raw, &out); err != nil {
		return UpdaterRecord{}, errors.New("the updater's answer is not the status")
	}
	st, err := u.loadState()
	if err != nil {
		return UpdaterRecord{}, err
	}
	return recordFrom(out, st, u.clock()), nil
}

// recordFrom maps the updater's status onto the record the routes read. A staged version is verified: it is there only after the verifier approved it, and the apply checks it again.
// The tmux restart flag and the trust display are not in the status, so they stay empty.
func recordFrom(out status.Out, st relaunchState, now time.Time) UpdaterRecord {
	rec := UpdaterRecord{
		Enabled:         out.Feature == "on" && out.ApplyEnabled,
		Installed:       out.Installed,
		State:           updNone,
		HubRelaunched:   st.HubRelaunched,
		LastHubRelaunch: st.LastHubRelaunch,
	}
	if out.Accepted != nil {
		rec.Trust.ManifestSequence = out.Accepted.Sequence
	}
	rec.CheckedAt = parseTime(out.LastCheck)
	if i := out.Install; i != nil {
		rec.InstallKind = i.Kind
		rec.Supported = i.Unsupported == "" && i.Writable
	}
	if s := out.Staged; s != nil && updateVersionRe.MatchString(s.Version) {
		rec.State = updStaged
		rec.Staged = &StagedUpdate{Version: s.Version, StagedAt: parseTime(s.StagedAt), Verified: true}
		rec.Latest = s.Version
	}
	if a := out.LastApply; a != nil {
		rec.LastResult = &UpdateResult{At: parseTime(a.At), From: a.From, To: a.To, By: a.By, Result: resultWord(a.Result)}
		if rec.State != updStaged && (a.Result == "failed" || a.Result == "failed_rolled_back" || a.Result == "restart_failed") {
			rec.State = updFailed
		}
	}
	if r := out.LastRolled; r != nil {
		reason := r.Reason
		if !rollbackReasons[reason] {
			reason = rollbackOtherTag
		}
		rec.LastRollback = &UpdateRollback{At: parseTime(r.At), From: r.From, To: r.To, Reason: reason, RestartFailed: r.Result == "restart_failed"}
	}
	// A relaunch this process started counts as running until the updater records a result for it, or too long has passed.
	if !st.StartedAt.IsZero() && now.Sub(st.StartedAt) < applyingFor && out.Installed == st.From {
		finished := out.LastApply != nil && parseTime(out.LastApply.At).After(st.StartedAt)
		if !finished {
			rec.State = updApplying
		}
	}
	rec.NotReady = notReady(out, rec, now)
	return rec
}

// notReady is why nothing is staged, in the status' vocabulary (notReadyReasons); "" when something is.
func notReady(out status.Out, rec UpdaterRecord, now time.Time) string {
	switch {
	case rec.State == updApplying:
		return "applying"
	case rec.Staged != nil:
		return ""
	case out.Feature != "on" || !out.ApplyEnabled:
		return "switch_off"
	case out.Install != nil && !rec.Supported:
		return "unsupported_install"
	case out.StateFile != "present" || out.LastCheck == "":
		return "not_checked"
	case out.Backoff != nil && out.Backoff.Waiting:
		return "backoff"
	case out.LastFailure != nil:
		return "check_failed"
	}
	return "up_to_date"
}

// resultWord is an apply's result word in the relaunch vocabulary: relaunched, failed_rolled_back, anything else a refusal.
func resultWord(r string) string {
	switch r {
	case "relaunched":
		return resultRelaunched
	case "failed_rolled_back", "restart_failed":
		return resultRolledBack
	}
	return resultRefused
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func (u *realUpdater) loadState() (relaunchState, error) {
	var st relaunchState
	b, err := u.dir.read(relaunchStateFile, maxStateBytes)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, fmt.Errorf("the relaunch record is unreadable: %w", err)
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return relaunchState{}, errors.New("the relaunch record is damaged")
	}
	return st, nil
}

func (u *realUpdater) saveState(st relaunchState) error {
	if err := u.dir.ensure(); err != nil {
		return err
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return u.dir.write(relaunchStateFile, b)
}

// Apply starts the apply for a relaunch by by ("local" or a hub pseudonym): it records that this version was started (once per version), starts the updater program, and ends this server
// shortly after, so the program, which waits for it to end, can replace the install. Nothing in the request chooses what is installed.
func (u *realUpdater) Apply(ctx context.Context, by string) (string, error) {
	u.mu.Lock()
	u.cachedOK = false // read it fresh: this decides what is installed
	u.mu.Unlock()
	rec, err := u.read(ctx)
	if err != nil {
		return "", err
	}
	switch {
	case !rec.Enabled:
		return "", errors.New("the updater is switched off")
	case !rec.Supported:
		return "", errors.New("this install cannot be updated")
	case rec.Staged == nil || !rec.Staged.Verified:
		return "", errors.New("nothing verified is staged")
	case rec.State == updApplying:
		return "", errors.New("an update is already being applied")
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	op := hex.EncodeToString(nonce[:])
	now := u.clock()
	prev, err := u.loadState()
	if err != nil {
		return "", err
	}
	st := relaunchState{HubRelaunched: rec.Staged.Version, LastHubRelaunch: now, StartedAt: now, From: rec.Installed, To: rec.Staged.Version, Operation: op, By: by}
	if err := u.saveState(st); err != nil {
		return "", err
	}
	if err := u.prog.StartApply(); err != nil {
		_ = u.saveState(prev) // nothing started: the version may be tried again
		return "", fmt.Errorf("the updater did not start: %w", err)
	}
	if u.quit != nil {
		quit := u.quit
		safego.Go("update relaunch quit", func() {
			d := u.quitDelay
			if d == 0 {
				d = quitAfter
			}
			time.Sleep(d)
			quit()
		})
	}
	return op, nil
}

// Audit appends one attempt to the history, keeping the last maxAuditRows.
func (u *realUpdater) Audit(_ context.Context, a RelaunchAudit) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	var rows []RelaunchAudit
	b, err := u.dir.read(relaunchAuditFile, maxStateBytes)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return err
	default:
		if err := json.Unmarshal(b, &rows); err != nil {
			rows = nil // a damaged history starts again; the attempt is still recorded
		}
	}
	rows = append(rows, a)
	if len(rows) > maxAuditRows {
		rows = rows[len(rows)-maxAuditRows:]
	}
	out, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	if err := u.dir.ensure(); err != nil {
		return err
	}
	return u.dir.write(relaunchAuditFile, out)
}

// execProgram runs the updater program that sits beside this one.
type execProgram struct {
	path string // the program's path; "" is the one beside this program (a test names its own)
}

// programPath is coral-update next to the running program: no PATH search, and a regular file only.
func programPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", err
	}
	name := "coral-update"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	p := filepath.Join(filepath.Dir(real), name)
	fi, err := os.Lstat(p)
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", errors.New("the updater program is not a plain file")
	}
	return p, nil
}

// programEnv is the environment the updater program gets: the variables that say where Coral's data is and how it runs, and nothing else (no agent's variable, no token).
func programEnv() []string { return runrecord.AllowedEnv(os.Environ()) }

func (e execProgram) program() (string, error) {
	if e.path != "" {
		return e.path, nil
	}
	return programPath()
}

func (e execProgram) Status(ctx context.Context) ([]byte, error) {
	p, err := e.program()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, p, "status", "--json")
	cmd.Env = programEnv()
	cmd.WaitDelay = 2 * time.Second // a program that is killed at the timeout but left a child holding its output does not hold this call
	var out limitedBuffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	if out.over {
		return nil, errors.New("the status is too long")
	}
	return out.Bytes(), nil
}

func (e execProgram) StartApply() error {
	p, err := e.program()
	if err != nil {
		return err
	}
	cmd := exec.Command(p, "apply")
	cmd.Env = programEnv()
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	safego.Go("update apply wait", func() { _ = cmd.Wait() })
	return nil
}

// limitedBuffer keeps at most statusMaxBytes and says when there was more.
type limitedBuffer struct {
	buf  bytes.Buffer // a field, not embedded: io.Copy would use an embedded Buffer's ReadFrom and skip the limit
	over bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.buf.Len()+len(p) > statusMaxBytes {
		b.over = true
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *limitedBuffer) Bytes() []byte { return b.buf.Bytes() }

var _ io.Writer = (*limitedBuffer)(nil)
