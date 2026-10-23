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

	"github.com/subgentic/coral/internal/safefs"
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
	// readyWithin is how long the apply gets to be ready to swap (verify again, copy, build and check the new install, all while Coral runs); no marker in that time and Coral is
	// not ended: it keeps running, the apply is ended and the person is told why.
	readyWithin = 15 * time.Minute
	readyPoll   = 500 * time.Millisecond
	// quitAfter gives the reply to the relaunch request time to leave before this server ends, once the apply is ready.
	quitAfter = 2 * time.Second
)

// updaterProgram is the two things the updater program is asked: what it knows, and to apply (a test replaces it; the real one is execProgram).
type updaterProgram interface {
	Status(ctx context.Context) ([]byte, error)
	RunCheck(ctx context.Context) ([]byte, error)
	StartApply() (applyRun, error)
}

// applyRun is a started apply: its pid (the marker it writes names it), a channel that closes when it has ended, and a way to end it.
type applyRun struct {
	PID  int
	Done <-chan struct{}
	Kill func()
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
	// Failed is a relaunch that ended without an update and without the updater saying why: it never got ready, or it ended first (#5982). Reason is a fixed word.
	Failed *relaunchFailure `json:"failed,omitempty"`
}

// relaunchFailure is how a started relaunch ended without a swap and without a result of the updater's own.
type relaunchFailure struct {
	At     time.Time `json:"at"`
	Reason string    `json:"reason"` // not_ready (no ready marker in time) or updater_ended (the updater ended before it was ready)
}

type realUpdater struct {
	dir  updatesDir
	prog updaterProgram
	quit func()
	now  func() time.Time
	// quitDelay is how long after the apply is ready quit is called (quitAfter unless a test says otherwise); readyWithin and readyPoll likewise.
	quitDelay, readyWithin, readyPoll time.Duration

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
		rec.Staged = &StagedUpdate{Version: s.Version, StagedAt: parseTime(s.StagedAt), Verified: true, Source: s.Source}
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
	// A relaunch that ended with no swap and no result of the updater's own (never ready, or the updater ended first) is a failed attempt the person is told about (#5982).
	if f := st.Failed; f != nil && (out.LastApply == nil || parseTime(out.LastApply.At).Before(f.At)) && out.Installed == st.From {
		rec.LastResult = &UpdateResult{At: f.At, From: st.From, To: st.To, By: st.By, Result: resultRefused}
		if rec.State != updApplying {
			rec.State = updFailed
			if rec.Staged != nil {
				rec.State = updStaged // still staged: it can be tried again
			}
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

// Apply starts the apply for a relaunch by by ("local" or a hub pseudonym): it records that this version was started (once per version) and starts the updater program. This server
// does NOT end itself yet: the apply verifies the package again, copies it and builds and checks the new install while Coral still runs, and says "ready to swap" (status.ReadyFile) only
// after all of that (#5982). watchReady ends this server on that marker, so a package that fails any step costs the person nothing. Nothing in the request chooses what is installed.
func (u *realUpdater) Apply(ctx context.Context, by string) (string, error) {
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	op := hex.EncodeToString(nonce[:])

	// One Apply at a time: the "is one already running" check and the record that says one is, are one step (a second request in between would start a second updater and a second quit).
	u.mu.Lock()
	u.cachedOK = false // read it fresh: this decides what is installed
	rec, err := u.read(ctx)
	if err != nil {
		u.mu.Unlock()
		return "", err
	}
	switch {
	case !rec.Enabled:
		err = errors.New("the updater is switched off")
	case !rec.Supported:
		err = errors.New("this install cannot be updated")
	case rec.Staged == nil || !rec.Staged.Verified:
		err = errors.New("nothing verified is staged")
	case rec.State == updApplying:
		err = errors.New("an update is already being applied")
	}
	if err != nil {
		u.mu.Unlock()
		return "", err
	}
	prev, err := u.loadState()
	if err != nil {
		u.mu.Unlock()
		return "", err
	}
	now := u.clock()
	st := relaunchState{HubRelaunched: rec.Staged.Version, LastHubRelaunch: now, StartedAt: now, From: rec.Installed, To: rec.Staged.Version, Operation: op, By: by}
	if err := u.saveState(st); err != nil {
		u.mu.Unlock()
		return "", err
	}
	u.cachedOK = false
	u.mu.Unlock()

	_ = u.dir.remove(status.ReadyFile) // a marker left by an earlier apply says nothing about this one
	run, err := u.prog.StartApply()
	if err != nil {
		u.mu.Lock()
		_ = u.saveState(prev) // nothing started: the version may be tried again
		u.cachedOK = false
		u.mu.Unlock()
		return "", fmt.Errorf("the updater did not start: %w", err)
	}
	safego.Go("update relaunch ready", func() { u.watchReady(run, st) })
	return op, nil
}

// watchReady ends this server when the apply says it is ready to swap, and only then. The marker must name the version and the pid of the apply this updater started. When the
// apply ends first, or says nothing within readyWithin, the server keeps running (the apply is ended) and the record says why, so the person is told and can try again.
func (u *realUpdater) watchReady(run applyRun, st relaunchState) {
	deadline := time.Now().Add(readyWithin)
	if u.readyWithin > 0 {
		deadline = time.Now().Add(u.readyWithin)
	}
	poll := readyPoll
	if u.readyPoll > 0 {
		poll = u.readyPoll
	}
	tick := time.NewTicker(poll)
	defer tick.Stop()
	d := u.quitDelay
	if d == 0 {
		d = quitAfter
	}
	for {
		// An apply that has ended is a failure whatever it left behind: a marker is only true while the apply runs, and a stale one must never end the server.
		select {
		case <-run.Done:
			u.giveUp(st, "updater_ended")
			return
		default:
		}
		if u.readyMarker(run, st) {
			// Give the reply to the relaunch request time to leave, but an apply that ends meanwhile cancels the quit, and the apply is looked at again just before it.
			select {
			case <-run.Done:
				u.giveUp(st, "updater_ended")
				return
			case <-time.After(d):
			}
			select {
			case <-run.Done:
				u.giveUp(st, "updater_ended")
				return
			default:
			}
			if !u.readyMarker(run, st) {
				continue // the marker went (the apply is ending): the next turn sees it ended
			}
			if u.quit != nil {
				u.quit()
			}
			return
		}
		select {
		case <-run.Done:
			u.giveUp(st, "updater_ended")
			return
		case <-tick.C:
			if time.Now().After(deadline) {
				if run.Kill != nil {
					run.Kill()
				}
				u.giveUp(st, "not_ready")
				return
			}
		}
	}
}

// readyMarker says whether the marker is there and is this apply's, for this version.
func (u *realUpdater) readyMarker(run applyRun, st relaunchState) bool {
	b, err := u.dir.read(status.ReadyFile, maxStateBytes)
	if err != nil {
		return false
	}
	var r status.Ready
	if json.Unmarshal(b, &r) != nil {
		return false
	}
	return r.Version == st.To && r.PID == run.PID && run.PID > 0
}

// giveUp records that the relaunch ended without a swap and without a result of the updater's own: nothing is in flight, the version is spent for the spacing rule but the record says
// what happened.
func (u *realUpdater) giveUp(st relaunchState, reason string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	cur, err := u.loadState()
	if err != nil || cur.Operation != st.Operation {
		return // another relaunch has been started since
	}
	cur.StartedAt = time.Time{}
	cur.HubRelaunched = "" // nothing was installed: the version may be tried again (after the spacing, which LastHubRelaunch keeps)
	cur.Failed = &relaunchFailure{At: u.clock(), Reason: reason}
	_ = u.saveState(cur)
	u.cachedOK = false
}

// Check runs one check and returns the updater's own word for how it ended. It never installs.
func (u *realUpdater) Check(ctx context.Context) (string, error) {
	out, err := u.prog.RunCheck(ctx)
	u.mu.Lock()
	u.cachedOK = false // a check changes what is staged
	u.mu.Unlock()
	if err != nil {
		return "", fmt.Errorf("the updater did not answer: %w", err)
	}
	var r struct {
		Result string `json:"result"`
	}
	if json.Unmarshal(out, &r) != nil {
		return "", errors.New("the updater's answer is not a result")
	}
	return resultToken(r.Result), nil
}

// resultToken keeps an updater result word (lower-case letters, digits and underscores, at most 40) and shows anything else as "other": it goes to the log and to the job's decision only.
func resultToken(s string) string {
	if s == "" || len(s) > 40 {
		return "other"
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return "other"
		}
	}
	return s
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
	real, err := safefs.Resolve(exe)
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
	ctx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	return e.run(ctx, "status", "--json")
}

// RunCheck runs "coral-update check" (it fetches, verifies and stages; the caller's context bounds it) and returns its one JSON line.
func (e execProgram) RunCheck(ctx context.Context) ([]byte, error) { return e.run(ctx, "check") }

func (e execProgram) run(ctx context.Context, args ...string) ([]byte, error) {
	p, err := e.program()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, p, args...)
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

func (e execProgram) StartApply() (applyRun, error) {
	p, err := e.program()
	if err != nil {
		return applyRun{}, err
	}
	cmd := exec.Command(p, "apply")
	cmd.Env = programEnv()
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return applyRun{}, err
	}
	done := make(chan struct{})
	safego.Go("update apply wait", func() { _ = cmd.Wait(); close(done) })
	return applyRun{PID: cmd.Process.Pid, Done: done, Kill: func() { _ = cmd.Process.Kill() }}, nil
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
