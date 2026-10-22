//go:build !windows

// Not built on Windows: these test the Linux and macOS apply (POSIX file modes, renames over open files, symlinks, a staged record read back by path); the Windows apply is a separate piece (journal, MSI hand-off), #5395.

package apply

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/updater/stage"
	"github.com/subgentic/coral/internal/updater/status"
	"github.com/subgentic/coral/internal/updater/verify"
)

func sum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

type fakeSigner struct{ verdict error }

type fakeBundle struct {
	Artifact string `json:"artifact"`
	Commit   string `json:"commit"`
}

func bundleOf(artifact []byte, commit string) []byte {
	b, _ := json.Marshal(fakeBundle{Artifact: sum(artifact), Commit: commit})
	return b
}

func (f *fakeSigner) VerifyBundle(bundleJSON, artifact []byte, id verify.Identity) (verify.Cert, error) {
	if f.verdict != nil {
		return verify.Cert{}, f.verdict
	}
	var b fakeBundle
	if json.Unmarshal(bundleJSON, &b) != nil || b.Artifact != sum(artifact) {
		return verify.Cert{}, errors.New("the signature is not over this artifact")
	}
	return verify.Cert{SAN: id.SAN, SourceRepositoryDigest: b.Commit}, nil
}

var (
	testNow    = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	testCommit = strings.Repeat("cd", 20)
)

func asset(v string) string { return "Coral.v" + v + ".dmg" }

// fakePlatform records what the apply asks of the platform and lets a test make any step fail.
type fakePlatform struct {
	t         *testing.T
	root      string // the folder the "install" lives in
	pinned    bool
	unsupport string
	writable  bool
	coralGone bool

	version    map[string]string // path -> bundle version
	calls      []string
	buildPkg   string // what Build was given
	buildSaw   []byte // the package bytes Build read
	buildHook  func(pkg string)
	verifyErr  error
	swapErr    error
	startErrs  []error // popped per Start
	healthErr  error
	shown      string
	swaps      [][2]string
	swapHook   func()
	rollErr    error
	waitHook   func() bool
	running    bool // Running's answer
	verifyHook func() error
	// restartWhy is CanRestart's answer: why Coral could not be started again ("" = it can).
	restartWhy string
	// restartHook, when set, answers CanRestart instead (a test makes the answer change between the two asks).
	restartHook func() string
}

func (f *fakePlatform) rec(s string) { f.calls = append(f.calls, s) }

func (f *fakePlatform) CanRestart(string) string {
	if f.restartHook != nil {
		return f.restartHook()
	}
	return f.restartWhy
}

func (f *fakePlatform) Pinned() bool { return f.pinned }
func (f *fakePlatform) Install() (string, error) {
	return filepath.Join(f.root, "Coral.app"), nil
}
func (f *fakePlatform) Unsupported(string) string { return f.unsupport }
func (f *fakePlatform) Writable(string) bool      { return f.writable }
func (f *fakePlatform) WaitCoralGone(context.Context, string) bool {
	f.rec("wait")
	if f.waitHook != nil {
		return f.waitHook()
	}
	return f.coralGone
}
func (f *fakePlatform) Version(path string) (string, error) {
	if v, ok := f.version[path]; ok {
		return v, nil
	}
	return "", errors.New("no bundle")
}
func (f *fakePlatform) Build(ctx context.Context, pkg, install, work string) (string, func(), error) {
	f.rec("build")
	f.buildPkg = pkg
	if f.buildHook != nil {
		f.buildHook(pkg)
	}
	b, err := os.ReadFile(pkg)
	require.NoError(f.t, err)
	f.buildSaw = b
	np := filepath.Join(f.root, ".Coral.app.new")
	require.NoError(f.t, os.MkdirAll(np, 0o755))
	require.NoError(f.t, os.WriteFile(filepath.Join(np, "marker"), []byte("new"), 0o644))
	f.version[np] = "1.4.130"
	return np, func() { f.rec("cleanup") }, nil
}
func (f *fakePlatform) Verify(ctx context.Context, newPath, version string) error {
	f.rec("verify")
	if f.verifyHook != nil {
		return f.verifyHook()
	}
	return f.verifyErr
}
func (f *fakePlatform) Swap(a, b string) error {
	f.rec("swap")
	if f.swapHook != nil {
		f.swapHook()
	}
	if len(f.swaps) >= 1 && f.rollErr != nil {
		return f.rollErr
	}
	if f.swapErr != nil && len(f.swaps) == 0 {
		return f.swapErr
	}
	f.swaps = append(f.swaps, [2]string{a, b})
	ta := filepath.Join(f.root, ".swap")
	require.NoError(f.t, os.Rename(a, ta))
	require.NoError(f.t, os.Rename(b, a))
	require.NoError(f.t, os.Rename(ta, b))
	f.version[a], f.version[b] = f.version[b], f.version[a]
	return nil
}
func (f *fakePlatform) Running(string) bool { return f.running }
func (f *fakePlatform) Start(ctx context.Context, install string) error {
	f.rec("start")
	if len(f.startErrs) > 0 {
		e := f.startErrs[0]
		f.startErrs = f.startErrs[1:]
		return e
	}
	return nil
}
func (f *fakePlatform) Healthy(ctx context.Context, install string) error {
	f.rec("healthy")
	return f.healthErr
}
func (f *fakePlatform) Show(ctx context.Context, pkg string) error {
	f.rec("show")
	f.shown = pkg
	return nil
}

type rig struct {
	t      *testing.T
	data   string
	plat   *fakePlatform
	signer *fakeSigner
	pkg    []byte
	cfg    Config
}

// newRig makes a staged, verified 1.4.130 for an installed 1.4.125, as the check would have left it.
func newRig(t *testing.T) *rig {
	t.Helper()
	data := t.TempDir()
	root := t.TempDir()
	plat := &fakePlatform{t: t, root: root, pinned: true, writable: true, coralGone: true, version: map[string]string{}}
	install := filepath.Join(root, "Coral.app")
	require.NoError(t, os.MkdirAll(install, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(install, "marker"), []byte("old"), 0o644))
	plat.version[install] = "1.4.125"
	r := &rig{t: t, data: data, plat: plat, signer: &fakeSigner{}, pkg: []byte("the dmg bytes of 1.4.130")}
	r.stage("1.4.130", r.pkg)
	r.cfg = Config{DataDir: data, Installed: "1.4.125", Pins: verify.DefaultPins(), Verifier: r.signer, Asset: asset, Platform: plat,
		Now: func() time.Time { return testNow }}
	return r
}

// stage writes a verified release the way stage.Check leaves it.
func (r *rig) stage(version string, pkg []byte) {
	r.t.Helper()
	sums := []byte(sum(pkg) + "  " + asset(version) + "\n")
	m := verify.Manifest{Schema: 1, Repo: verify.Repo, Tag: "v" + version, Version: version, SourceSHA: testCommit, SHA256SumsSHA: sum(sums), Sequence: 7,
		PromotedAt: "2026-10-20T10:00:00Z", ExpiresAt: "2026-11-20T10:00:00Z", Revoked: []string{}}
	manifest, _ := json.Marshal(m)
	cand := verify.Candidate{Manifest: manifest, ManifestBundle: bundleOf(manifest, ""), Sums: sums, SumsBundle: bundleOf(sums, testCommit)}
	rec, err := verify.Release(r.signer, verify.DefaultPins(), cand, asset, verify.State{}, "1.4.125", testNow)
	require.NoError(r.t, err)
	dir := filepath.Join(r.data, stage.UpdatesDir, stage.StagingDir, version)
	require.NoError(r.t, os.MkdirAll(dir, 0o700))
	for name, b := range map[string][]byte{stage.ManifestFile: manifest, stage.ManifestBundleFile: cand.ManifestBundle, stage.SumsFile: sums, stage.SumsBundleFile: cand.SumsBundle, asset(version): pkg} {
		require.NoError(r.t, os.WriteFile(filepath.Join(dir, name), b, 0o600))
	}
	rb, _ := json.Marshal(rec)
	require.NoError(r.t, os.WriteFile(filepath.Join(dir, stage.RecordFile), rb, 0o600))
	require.NoError(r.t, os.Chmod(filepath.Join(r.data, stage.UpdatesDir), 0o700))
}

func (r *rig) staged() *stage.Staged {
	s, err := stage.LoadStaged(r.data)
	require.NoError(r.t, err)
	return s
}

func (r *rig) state() verify.State {
	st, err := verify.LoadState(filepath.Join(r.data, stage.UpdatesDir))
	require.NoError(r.t, err)
	return st
}

func (r *rig) installMarker() string {
	b, err := os.ReadFile(filepath.Join(r.plat.root, "Coral.app", "marker"))
	require.NoError(r.t, err)
	return string(b)
}

func (r *rig) run() Result { return Run(context.Background(), r.cfg) }

func TestAnUpdateIsAppliedInTheOrderOfTheDesign(t *testing.T) {
	r := newRig(t)
	res := r.run()
	require.Equal(t, ResultRelaunched, res.Result, "%+v", res)
	assert.Equal(t, "1.4.130", res.To)
	assert.Equal(t, []string{"build", "verify", "wait", "verify", "swap", "start", "healthy", "cleanup"}, r.plat.calls, "everything that can be done while Coral runs comes before the one wait for it to end")
	assert.Equal(t, "new", r.installMarker(), "the new install is in place")
	_, err := os.Stat(filepath.Join(r.plat.root, ".Coral.app.new"))
	assert.ErrorIs(t, err, os.ErrNotExist, "the old install goes only after the new one is up")

	st := r.state()
	assert.Equal(t, "1.4.130", st.Version, "the high-water mark moves after the health check")
	assert.EqualValues(t, 7, st.Sequence)
	require.NotEmpty(t, st.History)
	assert.Equal(t, "relaunched", st.History[len(st.History)-1].Result)
	_, err = stage.LoadStaged(r.data)
	assert.ErrorIs(t, err, stage.ErrNoStaged, "the staged copy is removed")
	_, err = os.Stat(filepath.Join(r.data, stage.UpdatesDir, journalFile))
	assert.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(filepath.Join(r.data, stage.UpdatesDir, "apply"))
	assert.ErrorIs(t, err, os.ErrNotExist, "the private copy is removed")
	_, err = os.Stat(filepath.Join(r.data, stage.UpdatesDir, stage.ApplyLockFile))
	assert.ErrorIs(t, err, os.ErrNotExist, "the lock is released")
}

func TestNothingIsInstalledWithoutAPinnedSignerOrWhileCoralRuns(t *testing.T) {
	r := newRig(t)
	r.plat.pinned = false
	assert.Equal(t, ResultPinsNotSet, r.run().Result)
	assert.Empty(t, r.plat.calls)

	r = newRig(t)
	r.plat.coralGone = false
	assert.Equal(t, ResultCoralRunning, r.run().Result)
	assert.Equal(t, []string{"build", "verify", "wait", "cleanup"}, r.plat.calls, "it builds and checks the new install while Coral runs, waits for Coral, swaps nothing and starts nothing")
	assert.Equal(t, "old", r.installMarker())
	assert.NotNil(t, r.staged(), "the staged update is kept")
}

func TestNothingStagedBusyAndUnsupported(t *testing.T) {
	r := newRig(t)
	require.NoError(t, os.RemoveAll(filepath.Join(r.data, stage.UpdatesDir, stage.StagingDir)))
	assert.Equal(t, ResultNothingStaged, r.run().Result)

	r = newRig(t)
	live, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "taken": testNow.Unix()})
	dir := filepath.Join(r.data, stage.UpdatesDir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, stage.CheckLockFile), live, 0o600))
	assert.Equal(t, ResultBusy, r.run().Result, "a check is running")
	require.NoError(t, os.Remove(filepath.Join(dir, stage.CheckLockFile)))
	require.NoError(t, os.WriteFile(filepath.Join(dir, stage.ApplyLockFile), live, 0o600))
	assert.Equal(t, ResultBusy, r.run().Result, "another apply is running")
	assert.Empty(t, r.plat.calls)

	r = newRig(t)
	r.plat.unsupport = "homebrew"
	res := r.run()
	assert.Equal(t, ResultUnsupported, res.Result)
	assert.Equal(t, "homebrew", res.FailedStage)
	assert.Empty(t, r.plat.calls, "nothing was touched, not even waited for")
}

func TestAnInstallTheUserCannotWriteGetsTheVerifiedPackageOpenedNotASwap(t *testing.T) {
	r := newRig(t)
	r.plat.writable = false
	res := r.run()
	require.Equal(t, ResultNeedsManual, res.Result)
	assert.Equal(t, []string{"show"}, r.plat.calls, "the package is shown while Coral runs: nothing is swapped, so nothing waits")
	assert.NotEqual(t, r.staged().PackagePath, r.plat.shown, "the copy that was verified is shown, not the staged file")
	assert.Equal(t, "old", r.installMarker())
}

func TestWhatIsInstalledIsTheBytesThatWereVerifiedWhateverHappensToTheStagedFile(t *testing.T) {
	r := newRig(t)
	staged := r.staged().PackagePath
	r.plat.buildHook = func(pkg string) {
		assert.NotEqual(t, staged, pkg, "the install is built from the private copy")
		require.NoError(t, os.WriteFile(staged, []byte("swapped after it was verified"), 0o600))
	}
	require.Equal(t, ResultRelaunched, r.run().Result)
	assert.Equal(t, r.pkg, r.plat.buildSaw, "the build read the verified bytes")
}

func TestAnythingThatFailsVerificationAgainChangesNothing(t *testing.T) {
	for name, tamper := range map[string]func(r *rig){
		"the package is replaced after the check": func(r *rig) {
			require.NoError(t, os.WriteFile(r.staged().PackagePath, []byte("evil"), 0o600))
		},
		"the record is edited to match": func(r *rig) {
			require.NoError(t, os.WriteFile(r.staged().PackagePath, []byte("evil"), 0o600))
			s := r.staged()
			s.Record.PackageSHA256 = sum([]byte("evil"))
			b, _ := json.Marshal(s.Record)
			require.NoError(t, os.WriteFile(filepath.Join(s.Dir, stage.RecordFile), b, 0o600))
		},
		"the sums file is replaced": func(r *rig) {
			s := r.staged()
			require.NoError(t, os.WriteFile(filepath.Join(s.Dir, stage.SumsFile), append(s.Candidate.Sums, '\n'), 0o600))
		},
		"the signer now refuses":             func(r *rig) { r.signer.verdict = errors.New("not the pinned identity") },
		"the installed version is now newer": func(r *rig) { r.cfg.Installed = "1.4.131" },
		"the manifest has expired":           func(r *rig) { r.cfg.Now = func() time.Time { return testNow.Add(90 * 24 * time.Hour) } },
		"the version was revoked": func(r *rig) {
			require.NoError(t, verify.SaveState(filepath.Join(r.data, stage.UpdatesDir), verify.State{Revoked: []string{"1.4.130"}}))
		},
	} {
		r := newRig(t)
		tamper(r)
		res := r.run()
		assert.Equal(t, ResultRefused, res.Result, name)
		assert.NotContains(t, r.plat.calls, "build", name)
		assert.NotContains(t, r.plat.calls, "swap", name)
		assert.Equal(t, "old", r.installMarker(), name)
		st := r.state()
		assert.NotEqual(t, "1.4.130", st.Version, name+": the high-water mark did not move")
		if !strings.Contains(name, "newer") {
			require.NotNil(t, st.LastFailure, name)
		}
	}
}

func TestANewInstallThatFailsItsOwnChecksIsDiscardedBeforeTheSwap(t *testing.T) {
	r := newRig(t)
	r.plat.verifyErr = errors.New("codesign: a file the signature does not seal")
	res := r.run()
	require.Equal(t, ResultRefused, res.Result)
	assert.Equal(t, "platform_signature", res.FailedStage)
	assert.NotContains(t, r.plat.calls, "swap")
	assert.Equal(t, "old", r.installMarker())
	_, err := os.Stat(filepath.Join(r.plat.root, ".Coral.app.new"))
	assert.ErrorIs(t, err, os.ErrNotExist, "the unverified copy is removed")
	assert.NotEqual(t, "1.4.130", r.state().Version)
}

func TestASwapThatFailsLeavesTheOldInstall(t *testing.T) {
	r := newRig(t)
	r.plat.swapErr = errors.New("EXDEV")
	res := r.run()
	assert.Equal(t, ResultFailed, res.Result)
	assert.Equal(t, "swap", res.FailedStage)
	assert.Equal(t, "old", r.installMarker())
	_, err := os.Stat(filepath.Join(r.plat.root, ".Coral.app.new"))
	assert.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(filepath.Join(r.data, stage.UpdatesDir, journalFile))
	assert.ErrorIs(t, err, os.ErrNotExist, "no journal of a swap that did not happen")
}

func TestANewVersionThatDoesNotComeUpIsRolledBack(t *testing.T) {
	for name, set := range map[string]func(p *fakePlatform){
		"it does not stay up": func(p *fakePlatform) { p.healthErr = errors.New("exited") },
		"it does not start":   func(p *fakePlatform) { p.startErrs = []error{errors.New("open failed")} },
	} {
		r := newRig(t)
		set(r.plat)
		res := r.run()
		require.Equal(t, ResultRolledBack, res.Result, name)
		assert.Equal(t, "old", r.installMarker(), name+": the old install is back")
		assert.Len(t, r.plat.swaps, 2, name)
		starts := 0
		for _, c := range r.plat.calls {
			if c == "start" {
				starts++
			}
		}
		assert.Equal(t, 2, starts, name+": the new one was started, then the old one again")
		st := r.state()
		assert.NotEqual(t, "1.4.130", st.Version, name+": the high-water mark did not move")
		require.NotEmpty(t, st.History, name)
		assert.Equal(t, "failed_rolled_back", st.History[len(st.History)-1].Result, name)
		assert.Equal(t, res.FailedStage, st.History[len(st.History)-1].Reason, name+": the history keeps the stage it stopped at, for coral-update status (#5420)")
		assert.NotEmpty(t, res.FailedStage, name)
		require.NotNil(t, st.LastFailure, name)
		_, err := os.Stat(filepath.Join(r.plat.root, ".Coral.app.new"))
		assert.ErrorIs(t, err, os.ErrNotExist, name+": the failed new install is removed")
		assert.NotNil(t, r.staged(), name+": the staged update stays (it is not staged again in a loop: the check's back-off)")
	}
	// A rollback that itself fails is said so, loudly, and nothing else is claimed.
	r := newRig(t)
	r.plat.healthErr = errors.New("exited")
	r.plat.rollErr = errors.New("swap back failed")
	res := r.run()
	assert.Equal(t, ResultFailed, res.Result)
	assert.Equal(t, "rollback_swap", res.FailedStage)
}

func TestAnInterruptedApplyIsPutRightAtTheNextOne(t *testing.T) {
	// Dies right after the swap, before anything was proven: the next apply puts the old install back.
	r := newRig(t)
	r.cfg.AfterSwap = func() { panic("power cut") }
	func() {
		defer func() { _ = recover() }()
		r.run()
	}()
	// The lock of the dead run is still there (its owner "died"): release it as a dead owner's.
	dir := filepath.Join(r.data, stage.UpdatesDir)
	dead, _ := json.Marshal(map[string]any{"pid": 2147483646, "taken": testNow.Unix()})
	require.NoError(t, os.WriteFile(filepath.Join(dir, stage.ApplyLockFile), dead, 0o600))
	require.Equal(t, "new", r.installMarker(), "the swap happened")
	r.cfg.AfterSwap = nil
	r.plat.calls = nil
	res := r.run()
	assert.Equal(t, ResultRolledBack, res.Result)
	assert.Equal(t, "interrupted", res.FailedStage)
	assert.Equal(t, "old", r.installMarker())
	assert.NotEqual(t, "1.4.130", r.state().Version)

	// Dies between the journal and the swap: nothing was swapped; the leftover copy is removed and the apply goes on.
	r = newRig(t)
	r.plat.swapHook = func() { panic("killed") }
	func() {
		defer func() { _ = recover() }()
		r.run()
	}()
	dir = filepath.Join(r.data, stage.UpdatesDir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, stage.ApplyLockFile), dead, 0o600))
	r.plat.swapHook = nil
	require.Equal(t, "old", r.installMarker())
	res = r.run()
	assert.Equal(t, ResultRelaunched, res.Result, "%+v", res)
	assert.Equal(t, "new", r.installMarker())
}

// #5338: Coral started again while the package was being copied and checked: nothing is swapped, and the copy is removed.
func TestACoralThatDoesNotEndIsNotSwappedUnderAndTheCopyIsRemoved(t *testing.T) {
	r := newRig(t)
	r.plat.waitHook = func() bool { return false } // Coral is still running when the apply is ready
	res := r.run()
	assert.Equal(t, ResultCoralRunning, res.Result)
	assert.NotContains(t, r.plat.calls, "swap")
	assert.NotContains(t, r.plat.calls, "start", "Coral never ended: it is not started again")
	assert.False(t, res.RestartFailed)
	assert.Equal(t, "old", r.installMarker())
	_, err := os.Stat(filepath.Join(r.plat.root, ".Coral.app.new"))
	assert.ErrorIs(t, err, os.ErrNotExist, "the copy is removed")
	assert.NotNil(t, r.staged(), "the staged update is kept")
}

// #5338: a journal is not trusted for paths, and after the swap an unreadable install is not a reason to delete the rollback.
func TestAJournalDoesNotChooseWhatIsRemovedAndASwappedInstallIsNeverTakenForALeftoverCopy(t *testing.T) {
	r := newRig(t)
	dir := filepath.Join(r.data, stage.UpdatesDir)
	victim := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(victim, "keep"), []byte("x"), 0o600))
	install := filepath.Join(r.plat.root, "Coral.app")
	require.NoError(t, writeJournal(dir, journal{Phase: "swapping", From: "1.4.125", To: "1.4.130", NewPath: victim, Install: install}))
	_ = r.run()
	_, err := os.Stat(filepath.Join(victim, "keep"))
	assert.NoError(t, err, "a journal naming another folder removes nothing")

	// Swapped, the new install cannot be read: the other path is the old install, the rollback, and stays.
	r = newRig(t)
	dir = filepath.Join(r.data, stage.UpdatesDir)
	other := filepath.Join(r.plat.root, ".Coral.app.new")
	require.NoError(t, os.MkdirAll(other, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(other, "marker"), []byte("old"), 0o644))
	delete(r.plat.version, filepath.Join(r.plat.root, "Coral.app")) // unreadable
	install = filepath.Join(r.plat.root, "Coral.app")
	require.NoError(t, writeJournal(dir, journal{Phase: "swapped", From: "1.4.125", To: "1.4.130", NewPath: other, Install: install}))
	_, _ = recoverInterrupted(context.Background(), r.cfg, dir, install)
	_, err = os.Stat(filepath.Join(other, "marker"))
	assert.NoError(t, err, "the old install is not deleted because the new one cannot be read")
}

// An apply that died after the swap, but whose new install came up (a service manager restarts Coral and can end the updater with it): the next
// apply finds the new install running and finishes the job, it does not roll a working update back.
func TestAnInterruptedApplyWhoseNewInstallIsRunningIsFinishedNotRolledBack(t *testing.T) {
	r := newRig(t)
	r.cfg.AfterSwap = func() { panic("the manager ended the updater with Coral") }
	func() {
		defer func() { _ = recover() }()
		r.run()
	}()
	dir := filepath.Join(r.data, stage.UpdatesDir)
	dead, _ := json.Marshal(map[string]any{"pid": 2147483646, "taken": testNow.Unix()})
	require.NoError(t, os.WriteFile(filepath.Join(dir, stage.ApplyLockFile), dead, 0o600))
	require.Equal(t, "new", r.installMarker())
	r.cfg.AfterSwap = nil
	r.plat.running = true
	r.plat.calls = nil
	res := r.run()
	assert.Equal(t, ResultRelaunched, res.Result, "%+v", res)
	assert.Equal(t, "new", r.installMarker(), "the working new install stays")
	assert.Len(t, r.plat.swaps, 1, "no rollback swap")
	_, err := os.Stat(filepath.Join(r.plat.root, ".Coral.app.new"))
	assert.ErrorIs(t, err, os.ErrNotExist, "the old install is removed")
	st := r.state()
	assert.Equal(t, "1.4.130", st.Version, "the high-water mark moves")
	assert.EqualValues(t, 7, st.Sequence)
	_, err = os.Stat(filepath.Join(dir, journalFile))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

// The new install is checked again right before the swap (it sat beside the old one in the meantime): a copy that fails the second check is
// discarded and nothing is swapped.
func TestANewInstallChangedBetweenItsChecksIsNotSwapped(t *testing.T) {
	r := newRig(t)
	verifies := 0
	r.plat.verifyHook = func() error {
		verifies++
		if verifies == 2 {
			return errors.New("a file of the copy changed")
		}
		return nil
	}
	res := r.run()
	assert.Equal(t, ResultRefused, res.Result)
	assert.Equal(t, "platform_signature", res.FailedStage)
	assert.NotContains(t, r.plat.calls, "swap")
	assert.Equal(t, "old", r.installMarker())
	_, err := os.Stat(filepath.Join(r.plat.root, ".Coral.app.new"))
	assert.ErrorIs(t, err, os.ErrNotExist)
	assert.NotEqual(t, "1.4.130", r.state().Version)
}

// #5982: the apply does steps 2 to 4 while Coral still runs and says "ready to swap" only after them; the server ends itself on that and not before.
func TestAPackageThatFailsVerificationAgainLeavesCoralRunningAndNeverWaitsForIt(t *testing.T) {
	r := newRig(t)
	r.plat.verifyErr = errors.New("signature does not match")
	res := r.run()
	require.Equal(t, ResultRefused, res.Result)
	assert.Equal(t, "platform_signature", res.FailedStage)
	assert.NotContains(t, r.plat.calls, "wait", "nothing asked Coral to end")
	assert.NotContains(t, r.plat.calls, "start", "and nothing started it: it never stopped")
	assert.Equal(t, "old", r.installMarker())
	_, err := os.Stat(filepath.Join(r.data, stage.UpdatesDir, status.ReadyFile))
	assert.ErrorIs(t, err, os.ErrNotExist, "no ready marker for a package that was refused")

	// the same for a staged file whose bytes are not the verified ones (the copy refuses it)
	r = newRig(t)
	require.NoError(t, os.WriteFile(r.staged().PackagePath, []byte("tampered"), 0o600))
	res = r.run()
	assert.Contains(t, []string{ResultRefused, ResultFailed}, res.Result)
	assert.Empty(t, r.plat.calls, "not even built")
	_, err = os.Stat(filepath.Join(r.data, stage.UpdatesDir, status.ReadyFile))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestTheReadyMarkerIsWrittenBeforeTheWaitForCoralAndRemovedWhenTheApplyEnds(t *testing.T) {
	r := newRig(t)
	marker := filepath.Join(r.data, stage.UpdatesDir, status.ReadyFile)
	var seen status.Ready
	r.plat.waitHook = func() bool {
		b, err := os.ReadFile(marker)
		require.NoError(t, err, "the marker is there when the apply starts waiting for Coral to end")
		require.NoError(t, json.Unmarshal(b, &seen))
		return true
	}
	require.Equal(t, ResultRelaunched, r.run().Result)
	assert.Equal(t, "1.4.130", seen.Version)
	assert.Equal(t, os.Getpid(), seen.PID, "it says whose apply is ready")
	_, err := os.Stat(marker)
	assert.ErrorIs(t, err, os.ErrNotExist, "it is true only while the apply runs")
}

func TestAnApplyThatStopsAfterCoralEndedWithoutSwappingStartsThePreviousCoralAgain(t *testing.T) {
	for name, setup := range map[string]func(r *rig) (wantResult string){
		"the new install no longer verifies": func(r *rig) string {
			calls := 0
			r.plat.verifyHook = func() error { calls++; return map[bool]error{true: errors.New("changed under us")}[calls == 2] }
			return ResultRefused
		},
		"Coral could not be started again by the time of the swap": func(r *rig) string {
			calls := 0
			// CanRestart is asked before anything is built, and again before the swap: it goes away in between
			r.plat.restartHook = func() string { calls++; return map[bool]string{true: "no_run_record"}[calls >= 2] }
			return ResultUnsupported
		},
		"the swap fails": func(r *rig) string { r.plat.swapErr = errors.New("cross-device"); return ResultFailed },
	} {
		r := newRig(t)
		want := setup(r)
		res := r.run()
		assert.Equal(t, want, res.Result, name)
		assert.Contains(t, r.plat.calls, "start", name+": the previous Coral is started again, Coral had ended")
		assert.False(t, res.RestartFailed, name)
		assert.Equal(t, "old", r.installMarker(), name)
	}
}

func TestWhenThePreviousCoralCannotBeStartedAgainTheResultSaysSoAndTheHistoryRecordsIt(t *testing.T) {
	r := newRig(t)
	r.plat.swapErr = errors.New("cross-device")
	r.plat.startErrs = []error{errors.New("the app would not open")}
	res := r.run()
	assert.Equal(t, ResultFailed, res.Result)
	assert.True(t, res.RestartFailed, "nothing is running now: said, never silence (#5419)")
	st := r.state()
	require.NotEmpty(t, st.History)
	assert.Equal(t, "restart_failed", st.History[len(st.History)-1].Result)
}
