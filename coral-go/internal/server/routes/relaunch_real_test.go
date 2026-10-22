//go:build !windows

package routes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/updater/status"
)

// Step 3 A: the real updater behind the relaunch routes. It reads "coral-update status --json", starts "coral-update apply" and ends the server; the routes still answer 404 until the
// feature constant is switched on.

type fakeProgram struct {
	out        status.Out
	statusErr  error
	startErr   error
	statusRuns atomic.Int32
	starts     atomic.Int32
	kills      atomic.Int32
	checkRuns  atomic.Int32
	checkOut   []byte
	checkErr   error

	// what the started "apply" does: its pid, and the marker it writes (nil: none) once told to (ready is closed by the test; nil: at once).
	pid      int
	dir      *updatesDir
	marker   *status.Ready
	ready    chan struct{}
	endEarly bool // the apply ends without a marker
	noMarker bool // the apply runs and never writes one
	// endAfterMarker: the apply writes the marker and ends at once, leaving it behind (a stale marker).
	endAfterMarker bool
	// statusDelay makes the updater slow to answer, so concurrent callers all stand at the same check
	done        chan struct{}
	statusDelay time.Duration
}

func (f *fakeProgram) Status(context.Context) ([]byte, error) {
	f.statusRuns.Add(1)
	time.Sleep(f.statusDelay)
	if f.statusErr != nil {
		return nil, f.statusErr
	}
	return json.Marshal(f.out)
}

func (f *fakeProgram) RunCheck(context.Context) ([]byte, error) {
	f.checkRuns.Add(1)
	return f.checkOut, f.checkErr
}

func (f *fakeProgram) StartApply() (applyRun, error) {
	f.starts.Add(1)
	if f.startErr != nil {
		return applyRun{}, f.startErr
	}
	pid := f.pid
	if pid == 0 {
		pid = 4242
	}
	f.done = make(chan struct{})
	run := applyRun{PID: pid, Done: f.done, Kill: func() { f.kills.Add(1) }}
	go func() {
		if f.endEarly {
			close(f.done)
			return
		}
		if f.ready != nil {
			<-f.ready
		}
		if f.dir != nil && f.marker != nil && !f.noMarker {
			m := *f.marker
			if m.PID == 0 {
				m.PID = pid
			}
			b, _ := json.Marshal(m)
			_ = f.dir.ensure()
			_ = f.dir.write(status.ReadyFile, b)
		}
		if f.endAfterMarker {
			close(f.done)
		}
	}()
	return run, nil
}

func readyStatus() status.Out {
	return status.Out{
		Feature: "on", ApplyEnabled: true, Installed: "1.4.150", StateFile: "present", LastCheck: "2026-10-22T05:00:00Z",
		Accepted: &status.Accepted{Version: "1.4.150", Sequence: 7},
		Staged:   &status.Stage{Version: "1.4.155", StagedAt: "2026-10-22T05:01:00Z"},
		Install:  &status.Install{Kind: "app", Writable: true},
	}
}

func newRealUpdater(t *testing.T, f *fakeProgram, quit func()) *realUpdater {
	t.Helper()
	var mu sync.Mutex
	now := time.Date(2026, 10, 22, 6, 0, 0, 0, time.UTC)
	u := &realUpdater{dir: updatesDir{filepath.Join(t.TempDir(), "updates")}, prog: f, quit: quit, quitDelay: time.Millisecond, readyPoll: 5 * time.Millisecond, readyWithin: 5 * time.Second,
		now: func() time.Time { mu.Lock(); defer mu.Unlock(); now = now.Add(time.Minute); return now }}
	f.dir = &u.dir
	if f.marker == nil {
		f.marker = &status.Ready{Version: "1.4.155"}
	}
	return u
}

func TestTheRecordSaysWhatTheUpdaterSaysInTheRoutesWords(t *testing.T) {
	now := time.Date(2026, 10, 22, 6, 0, 0, 0, time.UTC)
	for name, tc := range map[string]struct {
		mod       func(*status.Out)
		state     relaunchState
		wantState string
		notReady  string
		supported bool
		enabled   bool
	}{
		"a verified staged update": {func(o *status.Out) {}, relaunchState{}, updStaged, "", true, true},
		"a Homebrew install": {func(o *status.Out) {
			o.Staged = nil
			o.Install = &status.Install{Kind: "homebrew", Unsupported: "homebrew", Writable: true}
		}, relaunchState{}, updNone, "unsupported_install", false, true},
		"a folder the user cannot write": {func(o *status.Out) { o.Staged = nil; o.Install = &status.Install{Kind: "unwritable", Writable: false} }, relaunchState{}, updNone, "unsupported_install", false, true},
		"never checked":                  {func(o *status.Out) { o.Staged = nil; o.StateFile = "missing"; o.LastCheck = "" }, relaunchState{}, updNone, "not_checked", true, true},
		"waiting after a failed try": {func(o *status.Out) {
			o.Staged = nil
			o.Backoff = &status.Back{Waiting: true, Until: "2026-10-22T07:00:00Z"}
		}, relaunchState{}, updNone, "backoff", true, true},
		"the last check failed":          {func(o *status.Out) { o.Staged = nil; o.LastFailure = &status.Fail{Stage: "verify"} }, relaunchState{}, updNone, "check_failed", true, true},
		"up to date":                     {func(o *status.Out) { o.Staged = nil }, relaunchState{}, updNone, "up_to_date", true, true},
		"the apply switch is off":        {func(o *status.Out) { o.Staged = nil; o.ApplyEnabled = false }, relaunchState{}, updNone, "switch_off", true, false},
		"the feature is off":             {func(o *status.Out) { o.Feature = "off"; o.OffReason = "build_tier" }, relaunchState{}, updStaged, "", true, false},
		"a relaunch in flight":           {func(o *status.Out) {}, relaunchState{StartedAt: now.Add(-time.Minute), From: "1.4.150"}, updApplying, "applying", true, true},
		"an old relaunch with no result": {func(o *status.Out) {}, relaunchState{StartedAt: now.Add(-applyingFor - time.Minute), From: "1.4.150"}, updStaged, "", true, true},
		"a relaunch the updater has a result for": {func(o *status.Out) {
			o.LastApply = &status.Apply{At: "2026-10-22T05:59:30Z", Result: "failed_rolled_back"}
		}, relaunchState{StartedAt: now.Add(-time.Minute - 45*time.Second), From: "1.4.150"}, updStaged, "", true, true},
		"a relaunch into another version already ran": {func(o *status.Out) { o.Installed = "1.4.155" }, relaunchState{StartedAt: now.Add(-time.Minute), From: "1.4.150"}, updStaged, "", true, true},
	} {
		out := readyStatus()
		tc.mod(&out)
		rec := recordFrom(out, tc.state, now)
		assert.Equal(t, tc.wantState, rec.State, name)
		assert.Equal(t, tc.notReady, rec.NotReady, name)
		assert.Equal(t, tc.supported, rec.Supported, name)
		assert.Equal(t, tc.enabled, rec.Enabled, name)
	}

	rec := recordFrom(readyStatus(), relaunchState{HubRelaunched: "1.4.155", LastHubRelaunch: now}, now)
	require.NotNil(t, rec.Staged)
	assert.True(t, rec.Staged.Verified)
	assert.Equal(t, "1.4.155", rec.Staged.Version)
	assert.Equal(t, "app", rec.InstallKind)
	assert.EqualValues(t, 7, rec.Trust.ManifestSequence)
	assert.Equal(t, "1.4.155", rec.HubRelaunched)

	// a hand-made version in the staged record is not taken
	out := readyStatus()
	out.Staged.Version = "../../etc"
	assert.Nil(t, recordFrom(out, relaunchState{}, now).Staged)
}

func TestTheLastApplyAndRollbackAreInTheRoutesVocabulary(t *testing.T) {
	out := readyStatus()
	out.Staged = nil
	out.LastApply = &status.Apply{At: "2026-10-22T05:30:00Z", From: "1.4.149", To: "1.4.150", By: "local", Result: "relaunched"}
	rec := recordFrom(out, relaunchState{}, time.Now())
	require.NotNil(t, rec.LastResult)
	assert.Equal(t, resultRelaunched, rec.LastResult.Result)

	out.LastApply = &status.Apply{At: "2026-10-22T05:30:00Z", Result: "restart_failed"}
	out.LastRolled = &status.Apply{At: "2026-10-22T05:30:00Z", From: "1.4.150", To: "1.4.151", Result: "restart_failed", Reason: "did_not_start"}
	rec = recordFrom(out, relaunchState{}, time.Now())
	assert.Equal(t, resultRolledBack, rec.LastResult.Result)
	assert.Equal(t, updFailed, rec.State)
	require.NotNil(t, rec.LastRollback)
	assert.True(t, rec.LastRollback.RestartFailed)
	assert.Equal(t, "did_not_start", rec.LastRollback.Reason)

	out.LastRolled.Reason = "some free text"
	assert.Equal(t, rollbackOtherTag, recordFrom(out, relaunchState{}, time.Now()).LastRollback.Reason, "a reason that is not a fixed word is 'other'")
	out.LastApply.Result = "coral_running"
	assert.Equal(t, resultRefused, recordFrom(out, relaunchState{}, time.Now()).LastResult.Result)
}

func untilTrue(t *testing.T, what string, cond func() bool) {
	t.Helper()
	require.Eventually(t, cond, 5*time.Second, 5*time.Millisecond, what)
}

func TestApplyRecordsAndStartsTheUpdaterAndTheServerEndsOnlyWhenTheApplyIsReady(t *testing.T) {
	f := &fakeProgram{out: readyStatus(), ready: make(chan struct{})}
	var quit atomic.Int32
	u := newRealUpdater(t, f, func() { quit.Add(1) })

	op, err := u.Apply(t.Context(), "hub:hub-user-0a1b2c3d")
	require.NoError(t, err)
	assert.Regexp(t, `^[0-9a-f]{16}$`, op)
	assert.EqualValues(t, 1, f.starts.Load())
	st, err := u.loadState()
	require.NoError(t, err)
	assert.Equal(t, "1.4.155", st.HubRelaunched, "once per version: the version is recorded")
	assert.Equal(t, "1.4.150", st.From)
	assert.Equal(t, op, st.Operation)
	assert.Equal(t, "hub:hub-user-0a1b2c3d", st.By)

	time.Sleep(100 * time.Millisecond) // sleep-ok: the absence of a quit while the apply is not ready needs a moment to mean something
	assert.Zero(t, quit.Load(), "the apply is still verifying and building: the server keeps running")

	// the record now says a relaunch is running, and a second Apply starts nothing
	rec, err := u.Record(t.Context())
	require.NoError(t, err)
	assert.Equal(t, updApplying, rec.State)
	_, err = u.Apply(t.Context(), "local")
	assert.Error(t, err)
	assert.EqualValues(t, 1, f.starts.Load())

	close(f.ready) // the apply writes its ready marker
	untilTrue(t, "the server ends once the apply is ready", func() bool { return quit.Load() == 1 })
}

func TestTheServerIgnoresAReadyMarkerThatIsNotThisAppliesForThisVersion(t *testing.T) {
	for name, m := range map[string]*status.Ready{
		"another version":     {Version: "1.4.156"},
		"another pid":         {Version: "1.4.155", PID: 1},
		"the version missing": {},
	} {
		f := &fakeProgram{out: readyStatus(), marker: m}
		if name == "the version missing" {
			f.marker = &status.Ready{PID: 4242}
		}
		var quit atomic.Int32
		u := newRealUpdater(t, f, func() { quit.Add(1) })
		u.readyWithin = 150 * time.Millisecond
		_, err := u.Apply(t.Context(), "local")
		require.NoError(t, err, name)
		untilTrue(t, name+": it gave up instead", func() bool { st, _ := u.loadState(); return st.Failed != nil })
		assert.Zero(t, quit.Load(), name+": the server keeps running")
	}
}

func TestAnApplyThatIsNeverReadyIsEndedTheServerKeepsRunningAndThePersonCanTryAgain(t *testing.T) {
	f := &fakeProgram{out: readyStatus()}
	f.noMarker = true // it never writes the marker
	var quit atomic.Int32
	u := newRealUpdater(t, f, func() { quit.Add(1) })
	u.readyWithin = 100 * time.Millisecond
	_, err := u.Apply(t.Context(), "local")
	require.NoError(t, err)
	untilTrue(t, "it gives up", func() bool { st, _ := u.loadState(); return st.Failed != nil })
	assert.Zero(t, quit.Load(), "no ready, no quit")
	assert.EqualValues(t, 1, f.kills.Load(), "the apply is ended")
	st, _ := u.loadState()
	assert.Equal(t, "not_ready", st.Failed.Reason)
	assert.True(t, st.StartedAt.IsZero(), "nothing is in flight")

	u.mu.Lock()
	u.cachedOK = false
	u.mu.Unlock()
	rec, err := u.Record(t.Context())
	require.NoError(t, err)
	assert.Equal(t, updStaged, rec.State, "the update is still staged and can be tried again")
	require.NotNil(t, rec.LastResult, "and the person is told it did not happen")
	assert.Equal(t, resultRefused, rec.LastResult.Result)
}

func TestAnApplyThatEndsBeforeItIsReadyLeavesTheServerRunning(t *testing.T) {
	f := &fakeProgram{out: readyStatus(), endEarly: true}
	var quit atomic.Int32
	u := newRealUpdater(t, f, func() { quit.Add(1) })
	_, err := u.Apply(t.Context(), "local")
	require.NoError(t, err)
	untilTrue(t, "it gives up", func() bool { st, _ := u.loadState(); return st.Failed != nil })
	st, _ := u.loadState()
	assert.Equal(t, "updater_ended", st.Failed.Reason)
	assert.Zero(t, quit.Load())
	assert.EqualValues(t, 0, f.kills.Load(), "it had already ended")
}

// Security's point 4 (#5981): two Apply calls at once start one updater and schedule one quit.
func TestTwoApplyCallsAtOnceStartOneUpdaterAndOneQuit(t *testing.T) {
	f := &fakeProgram{out: readyStatus(), statusDelay: 20 * time.Millisecond} // every caller reads the status before any of them has recorded a start
	var quit atomic.Int32
	u := newRealUpdater(t, f, func() { quit.Add(1) })
	var wg sync.WaitGroup
	var ok atomic.Int32
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := u.Apply(t.Context(), "local"); err == nil {
				ok.Add(1)
			}
		}()
	}
	wg.Wait()
	assert.EqualValues(t, 1, ok.Load(), "one call starts it, the others are refused")
	assert.EqualValues(t, 1, f.starts.Load())
	untilTrue(t, "the one quit", func() bool { return quit.Load() == 1 })
	time.Sleep(100 * time.Millisecond) // sleep-ok: a second quit would come within a moment
	assert.EqualValues(t, 1, quit.Load())
}

func TestApplyStartsNothingWhenTheUpdaterCannotApply(t *testing.T) {
	for name, mod := range map[string]func(*status.Out){
		"the apply switch is off": func(o *status.Out) { o.ApplyEnabled = false },
		"an install it cannot update": func(o *status.Out) {
			o.Install = &status.Install{Kind: "homebrew", Unsupported: "homebrew", Writable: true}
		},
		"nothing staged": func(o *status.Out) { o.Staged = nil },
	} {
		f := &fakeProgram{out: readyStatus()}
		mod(&f.out)
		var quit atomic.Int32
		u := newRealUpdater(t, f, func() { quit.Add(1) })
		_, err := u.Apply(t.Context(), "local")
		assert.Error(t, err, name)
		assert.Zero(t, f.starts.Load(), name)
		_, serr := os.Stat(filepath.Join(u.dir.Dir, relaunchStateFile))
		assert.True(t, os.IsNotExist(serr), name+": nothing recorded")
	}

	f := &fakeProgram{out: readyStatus(), statusErr: errors.New("no updater")}
	_, err := newRealUpdater(t, f, nil).Apply(t.Context(), "local")
	assert.Error(t, err, "an updater that does not answer starts nothing")
	assert.Zero(t, f.starts.Load())
}

func TestAnUpdaterThatDoesNotStartLeavesTheVersionToBeTriedAgain(t *testing.T) {
	f := &fakeProgram{out: readyStatus(), startErr: errors.New("exec format error")}
	var quit atomic.Int32
	u := newRealUpdater(t, f, func() { quit.Add(1) })
	_, err := u.Apply(t.Context(), "local")
	require.Error(t, err)
	time.Sleep(50 * time.Millisecond) // sleep-ok: the quit is scheduled only on success, so the absence of it needs a moment to mean something
	assert.Zero(t, quit.Load(), "the server is not ended for an update that did not start")
	st, err := u.loadState()
	require.NoError(t, err)
	assert.Empty(t, st.HubRelaunched, "the version was not spent")
	assert.True(t, st.StartedAt.IsZero(), "and nothing is in flight")
}

func TestRecordIsAnsweredFromTheLastAnswerForAMoment(t *testing.T) {
	f := &fakeProgram{out: readyStatus()}
	clock := time.Date(2026, 10, 22, 6, 0, 0, 0, time.UTC)
	u := newRealUpdater(t, f, nil)
	u.now = func() time.Time { return clock }
	for i := 0; i < 5; i++ {
		clock = clock.Add(100 * time.Millisecond)
		_, err := u.Record(t.Context())
		require.NoError(t, err)
	}
	assert.EqualValues(t, 1, f.statusRuns.Load(), "five calls inside the moment start the program once")
	clock = clock.Add(recordCacheFor + time.Second)
	_, err := u.Record(t.Context())
	require.NoError(t, err)
	assert.EqualValues(t, 2, f.statusRuns.Load())
}

func TestAuditKeepsTheLastFiftyAttemptsAndSurvivesADamagedFile(t *testing.T) {
	u := newRealUpdater(t, &fakeProgram{out: readyStatus()}, nil)
	for i := 0; i < maxAuditRows+10; i++ {
		require.NoError(t, u.Audit(t.Context(), RelaunchAudit{At: time.Unix(int64(i), 0).UTC(), Code: fmt.Sprintf("c%d", i)}))
	}
	b, err := u.dir.read(relaunchAuditFile, maxStateBytes)
	require.NoError(t, err)
	var rows []RelaunchAudit
	require.NoError(t, json.Unmarshal(b, &rows))
	require.Len(t, rows, maxAuditRows)
	assert.Equal(t, "c10", rows[0].Code)
	assert.Equal(t, fmt.Sprintf("c%d", maxAuditRows+9), rows[len(rows)-1].Code)

	require.NoError(t, u.dir.write(relaunchAuditFile, []byte("{not json")))
	require.NoError(t, u.Audit(t.Context(), RelaunchAudit{Code: "after"}))
	b, _ = u.dir.read(relaunchAuditFile, maxStateBytes)
	require.NoError(t, json.Unmarshal(b, &rows))
	require.Len(t, rows, 1)
	assert.Equal(t, "after", rows[0].Code)
}

// The program the real updater runs: a fixed argv, the allowlisted environment only, output bounded; and the apply is started with no argument but "apply".
func TestTheUpdaterProgramIsRunWithAFixedArgvAndOnlyTheAllowlistedEnvironment(t *testing.T) {
	dir := t.TempDir()
	argsFile, envFile := filepath.Join(dir, "args"), filepath.Join(dir, "env")
	prog := filepath.Join(dir, "coral-update")
	script := "#!/bin/sh\necho \"$@\" >> " + argsFile + "\nenv > " + envFile + "\nif [ \"$1\" = status ]; then echo '{\"feature\":\"on\"}'; fi\n"
	require.NoError(t, os.WriteFile(prog, []byte(script), 0o755))
	t.Setenv("CORAL_DATA_DIR", "/tmp/somewhere")
	t.Setenv("ANTHROPIC_API_KEY", "sk-should-not-reach-it")
	t.Setenv("GITHUB_TOKEN", "ghp_should_not_reach_it")
	e := execProgram{path: prog}

	out, err := e.Status(t.Context())
	require.NoError(t, err)
	assert.JSONEq(t, `{"feature":"on"}`, string(out))
	_, err = e.StartApply()
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		b, _ := os.ReadFile(argsFile)
		return strings.Count(string(b), "\n") == 2
	}, 5*time.Second, 20*time.Millisecond)
	b, _ := os.ReadFile(argsFile)
	assert.Equal(t, "status --json\napply\n", string(b), "the argv is fixed")
	env, _ := os.ReadFile(envFile)
	assert.Contains(t, string(env), "CORAL_DATA_DIR=/tmp/somewhere")
	assert.NotContains(t, string(env), "sk-should-not-reach-it", "an agent's key is not the updater's")
	assert.NotContains(t, string(env), "ghp_should_not_reach_it")
}

func TestTheUpdaterProgramsAnswerIsBoundedAndItsFailureIsAnError(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "big")
	require.NoError(t, os.WriteFile(big, []byte("#!/bin/sh\nyes '0123456789012345678901234567890123456789' | head -c 200000\n"), 0o755))
	_, err := execProgram{path: big}.Status(t.Context())
	assert.Error(t, err, "more than the bound is refused, not truncated into something that parses")

	bad := filepath.Join(dir, "bad")
	require.NoError(t, os.WriteFile(bad, []byte("#!/bin/sh\nexit 3\n"), 0o755))
	_, err = execProgram{path: bad}.Status(t.Context())
	assert.Error(t, err)

	_, err = execProgram{path: filepath.Join(dir, "missing")}.Status(t.Context())
	assert.Error(t, err)

	slow := filepath.Join(dir, "slow")
	require.NoError(t, os.WriteFile(slow, []byte("#!/bin/sh\nsleep 30\n"), 0o755))
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = execProgram{path: slow}.Status(ctx)
	assert.Error(t, err)
	assert.Less(t, time.Since(start), 10*time.Second, "a hung program does not hang the request")
}

// A program killed at the timeout that left a child holding its output must not hold the call: the child here keeps the pipe open for 30 s, so only WaitDelay ends the wait.
func TestAKilledUpdaterWhoseChildKeepsItsOutputOpenDoesNotHoldTheCall(t *testing.T) {
	dir := t.TempDir()
	holder := filepath.Join(dir, "holder")
	require.NoError(t, os.WriteFile(holder, []byte("#!/bin/sh\nsleep 30 &\nsleep 30\n"), 0o755))
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := execProgram{path: holder}.Status(ctx)
	assert.Error(t, err)
	assert.Less(t, time.Since(start), 10*time.Second, "a child holding the output does not hang the request")
}

func TestTheProgramBesideThisOneIsAPlainFileOrNothing(t *testing.T) {
	_, err := programPath() // the test binary has no coral-update beside it
	assert.Error(t, err)
}

// Security's review of #5982: an ended apply is always a failure, so a server is never ended with no apply alive.
func TestAnApplyThatEndedLeavingAReadyMarkerBehindDoesNotEndTheServer(t *testing.T) {
	f := &fakeProgram{out: readyStatus(), endAfterMarker: true}
	var quit atomic.Int32
	u := newRealUpdater(t, f, func() { quit.Add(1) })
	u.quitDelay = 30 * time.Millisecond
	_, err := u.Apply(t.Context(), "local")
	require.NoError(t, err)
	untilTrue(t, "it gives up", func() bool { st, _ := u.loadState(); return st.Failed != nil })
	time.Sleep(100 * time.Millisecond) // sleep-ok: a quit after the delay would come within it
	st, _ := u.loadState()
	assert.Equal(t, "updater_ended", st.Failed.Reason)
	assert.Zero(t, quit.Load(), "a marker that is still there from an apply that has ended is stale")
}

func TestAnApplyThatEndsWhileTheServerWaitsToEndCancelsTheQuit(t *testing.T) {
	f := &fakeProgram{out: readyStatus()}
	var quit atomic.Int32
	u := newRealUpdater(t, f, func() { quit.Add(1) })
	u.quitDelay = 300 * time.Millisecond // the reply needs time to leave
	_, err := u.Apply(t.Context(), "local")
	require.NoError(t, err)
	untilTrue(t, "the marker is there", func() bool { _, err := u.dir.read(status.ReadyFile, maxStateBytes); return err == nil })
	close(f.done) // the apply ends inside the delay
	untilTrue(t, "it gives up", func() bool { st, _ := u.loadState(); return st.Failed != nil })
	time.Sleep(400 * time.Millisecond) // sleep-ok: the quit, if it were still scheduled, comes after the 300 ms delay
	st, _ := u.loadState()
	assert.Equal(t, "updater_ended", st.Failed.Reason)
	assert.Zero(t, quit.Load(), "the server is not ended with no apply alive")
}

func TestTheRealUpdaterRunsACheckAndReadsOnlyItsResultWord(t *testing.T) {
	f := &fakeProgram{out: readyStatus(), checkOut: []byte(`{"result":"staged","installed":"1.4.150","version":"1.4.155","trust":"tuf"}` + "\n")}
	u := newRealUpdater(t, f, nil)
	res, err := u.Check(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "staged", res)
	assert.EqualValues(t, 1, f.checkRuns.Load())

	for out, want := range map[string]string{`{"result":"Not A Word"}`: "other", `{"result":""}`: "other", `{"result":"` + strings.Repeat("a", 41) + `"}`: "other", `{"result":"backoff"}`: "backoff"} {
		f.checkOut = []byte(out)
		res, err = u.Check(t.Context())
		require.NoError(t, err)
		assert.Equal(t, want, res, out)
	}
	f.checkOut = []byte("not json")
	_, err = u.Check(t.Context())
	assert.Error(t, err)
	f.checkErr = errors.New("exit status 1")
	_, err = u.Check(t.Context())
	assert.Error(t, err)

	// a check changes what is staged: the record is read again, not answered from the cache
	f.checkErr, f.checkOut = nil, []byte(`{"result":"staged"}`)
	before := f.statusRuns.Load()
	_, _ = u.Record(t.Context())
	_, _ = u.Check(t.Context())
	_, _ = u.Record(t.Context())
	assert.EqualValues(t, before+2, f.statusRuns.Load())
}

func TestTheUpdaterProgramIsRunForACheckWithTheArgvCheckOnly(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	prog := filepath.Join(dir, "coral-update")
	require.NoError(t, os.WriteFile(prog, []byte("#!/bin/sh\necho \"$@\" > "+argsFile+"\necho '{\"result\":\"up_to_date\"}'\n"), 0o755))
	out, err := execProgram{path: prog}.RunCheck(t.Context())
	require.NoError(t, err)
	assert.JSONEq(t, `{"result":"up_to_date"}`, string(out))
	b, _ := os.ReadFile(argsFile)
	assert.Equal(t, "check\n", string(b))
}
