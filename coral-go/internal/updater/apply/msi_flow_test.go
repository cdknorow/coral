package apply

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/updater/stage"
	"github.com/subgentic/coral/internal/updater/verify"
)

// The Windows apply's order and its failing closed (#162), on any OS: the MSI is faked, the verification is the real one over a staged release.

func msiSum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

type msiTestSigner struct{}

type msiTestBundle struct {
	Artifact string `json:"artifact"`
	Commit   string `json:"commit"`
}

func msiBundleOf(artifact []byte, commit string) []byte {
	b, _ := json.Marshal(msiTestBundle{Artifact: msiSum(artifact), Commit: commit})
	return b
}

func (msiTestSigner) VerifyBundle(bundleJSON, artifact []byte, id verify.Identity) (verify.Cert, error) {
	var b msiTestBundle
	if json.Unmarshal(bundleJSON, &b) != nil || b.Artifact != msiSum(artifact) {
		return verify.Cert{}, errors.New("the signature is not over this artifact")
	}
	return verify.Cert{SAN: id.SAN, SourceRepositoryDigest: b.Commit}, nil
}

var (
	msiTestNow    = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	msiTestCommit = strings.Repeat("ab", 20)
)

func msiAsset(v string) string { return "Coral-" + v + "-x64.msi" }

const (
	newProduct = "{11111111-2222-3333-4444-555555555555}"
	oldProduct = "{66666666-7777-8888-9999-AAAAAAAAAAAA}"
)

// fakeHeld is a held file: the fake records that it was held and closed.
type fakeHeld struct {
	path   string
	closed *bool
}

func (h fakeHeld) Path() string { return h.path }
func (h fakeHeld) Close() error { *h.closed = true; return nil }

// fakeMSI records what the apply asks and answers as the test sets it.
type fakeMSI struct {
	calls      []string
	install    string
	unsupport  string
	coralGone  bool
	authErr    error
	signed     bool
	product    string
	registered string
	codes      []int // msiexec's answers, popped per call
	msiArgs    [][]string
	heldClosed []bool
	sawAtExec  []byte // the bytes of the held file when msiexec ran
	startErr   []error
	healthErr  []error
	notes      []string
	shown      string
	holdHook   func(path string)
	// registeredAfterInstall, when set, is what Windows reports as registered after the update's install (another product).
	registeredAfterInstall string
}

func (f *fakeMSI) rec(s string)              { f.calls = append(f.calls, s) }
func (f *fakeMSI) Install() (string, error)  { return f.install, nil }
func (f *fakeMSI) Unsupported(string) string { return f.unsupport }
func (f *fakeMSI) WaitCoralGone(context.Context, string) bool {
	f.rec("wait")
	return f.coralGone
}
func (f *fakeMSI) Hold(path, sha string) (Held, error) {
	f.rec("hold")
	if f.holdHook != nil {
		f.holdHook(path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if msiSum(b) != sha {
		return nil, errors.New("the file is not the one that was verified")
	}
	f.heldClosed = append(f.heldClosed, false)
	return fakeHeld{path: path, closed: &f.heldClosed[len(f.heldClosed)-1]}, nil
}
func (f *fakeMSI) Authenticode(Held) (bool, error) {
	f.rec("authenticode")
	return f.signed, f.authErr
}
func (f *fakeMSI) ProductCode(Held) (string, error) { f.rec("productcode"); return f.product, nil }
func (f *fakeMSI) RegisteredProduct() (string, error) {
	if f.registered == "" {
		return "", errors.New("none")
	}
	return f.registered, nil
}
func (f *fakeMSI) Msiexec(ctx context.Context, args ...string) (int, error) {
	f.rec("msiexec " + args[0])
	f.msiArgs = append(f.msiArgs, args)
	if args[0] == "/i" {
		b, _ := os.ReadFile(args[1])
		f.sawAtExec = b
	}
	code := 0
	if len(f.codes) > 0 {
		code, f.codes = f.codes[0], f.codes[1:]
	}
	if code == 0 || code == 3010 {
		switch args[0] {
		case "/i":
			if f.product != "" && len(f.msiArgs) == 1 {
				f.registered = f.product
				if f.registeredAfterInstall != "" {
					f.registered = f.registeredAfterInstall
				}
			} else {
				f.registered = oldProduct
			}
		case "/x":
			f.registered = ""
		}
	}
	return code, nil
}
func (f *fakeMSI) Start(context.Context, string) error {
	f.rec("start")
	if len(f.startErr) > 0 {
		e := f.startErr[0]
		f.startErr = f.startErr[1:]
		return e
	}
	return nil
}
func (f *fakeMSI) Healthy(context.Context, string) error {
	f.rec("healthy")
	if len(f.healthErr) > 0 {
		e := f.healthErr[0]
		f.healthErr = f.healthErr[1:]
		return e
	}
	return nil
}
func (f *fakeMSI) Notify(title, text string) { f.rec("notify"); f.notes = append(f.notes, text) }
func (f *fakeMSI) ShowFolder(path string)    { f.rec("show"); f.shown = path }

type msiRig struct {
	t    *testing.T
	data string
	msi  *fakeMSI
	pkg  []byte
	cfg  MSIConfig
	// rollback package
	rbPath string
	rbRec  *verify.Record
}

// newMSIRig stages a verified 1.4.130 for an installed 1.4.125, and has a verified 1.4.125 package for the rollback.
func newMSIRig(t *testing.T) *msiRig {
	t.Helper()
	data := t.TempDir()
	r := &msiRig{t: t, data: data, pkg: []byte("the msi bytes of 1.4.130"),
		msi: &fakeMSI{install: filepath.Join(t.TempDir(), "Coral"), coralGone: true, product: newProduct, registered: oldProduct, signed: true}}
	r.stageRelease("1.4.130", r.pkg)
	rb := []byte("the msi bytes of 1.4.125")
	r.rbPath = filepath.Join(t.TempDir(), msiAsset("1.4.125"))
	require.NoError(t, os.WriteFile(r.rbPath, rb, 0o600))
	r.rbRec = &verify.Record{Version: "1.4.125", Asset: msiAsset("1.4.125"), PackageSHA256: msiSum(rb)}
	r.cfg = MSIConfig{DataDir: data, Installed: "1.4.125", Pins: verify.DefaultPins(), Verifier: msiTestSigner{}, Asset: msiAsset, MSI: r.msi,
		Now: func() time.Time { return msiTestNow }, Rollback: func() (string, *verify.Record, error) { return r.rbPath, r.rbRec, nil }}
	return r
}

func (r *msiRig) stageRelease(version string, pkg []byte) {
	r.t.Helper()
	sums := []byte(msiSum(pkg) + "  " + msiAsset(version) + "\n")
	m := verify.Manifest{Schema: 1, Repo: verify.Repo, Tag: "v" + version, Version: version, SourceSHA: msiTestCommit, SHA256SumsSHA: msiSum(sums), Sequence: 7,
		PromotedAt: "2026-10-22T10:00:00Z", ExpiresAt: "2026-11-22T10:00:00Z", Revoked: []string{}}
	manifest, _ := json.Marshal(m)
	cand := verify.Candidate{Manifest: manifest, ManifestBundle: msiBundleOf(manifest, ""), Sums: sums, SumsBundle: msiBundleOf(sums, msiTestCommit)}
	rec, err := verify.Release(msiTestSigner{}, verify.DefaultPins(), cand, msiAsset, verify.State{}, "1.4.125", msiTestNow)
	require.NoError(r.t, err)
	dir := filepath.Join(r.data, stage.UpdatesDir, stage.StagingDir, version)
	require.NoError(r.t, os.MkdirAll(dir, 0o700))
	for name, b := range map[string][]byte{stage.ManifestFile: manifest, stage.ManifestBundleFile: cand.ManifestBundle, stage.SumsFile: sums, stage.SumsBundleFile: cand.SumsBundle, msiAsset(version): pkg} {
		require.NoError(r.t, os.WriteFile(filepath.Join(dir, name), b, 0o600))
	}
	rb, _ := json.Marshal(rec)
	require.NoError(r.t, os.WriteFile(filepath.Join(dir, stage.RecordFile), rb, 0o600))
}

func (r *msiRig) run() Result { return RunMSI(context.Background(), r.cfg) }

func (r *msiRig) state() verify.State {
	st, err := verify.LoadState(filepath.Join(r.data, stage.UpdatesDir))
	require.NoError(r.t, err)
	return st
}

func (r *msiRig) lastHistory() verify.HistoryEntry {
	h := r.state().History
	require.NotEmpty(r.t, h)
	return h[len(h)-1]
}

// The order of the design: wait for Coral, verify again, hold the copy, Authenticode, its ProductCode, msiexec (unelevated, basic UI, the fixed
// properties), then start the new Coral as this user and check it; the held handle is closed once msiexec has finished, and the high-water
// mark moves.
func TestTheWindowsUpdateRunsInTheOrderOfTheDesign(t *testing.T) {
	r := newMSIRig(t)
	res := r.run()
	assert.Equal(t, ResultRelaunched, res.Result, res.FailedStage)
	assert.Equal(t, []string{"wait", "hold", "authenticode", "productcode", "msiexec /i", "start", "healthy"}, r.msi.calls)
	args := r.msi.msiArgs[0]
	assert.Equal(t, []string{"/qb!-", "/norestart", "REBOOT=ReallySuppress", "MSIRESTARTMANAGERCONTROL=Disable", "/l*v"}, args[2:7], "the fixed property list")
	assert.Equal(t, filepath.Join(r.data, stage.UpdatesDir, "msi-install.log"), args[7], "the log stays in the private updates folder")
	assert.Equal(t, r.pkg, r.msi.sawAtExec, "msiexec gets the verified bytes")
	assert.Equal(t, []bool{true}, r.msi.heldClosed, "the hold ends once msiexec has finished")
	st := r.state()
	assert.Equal(t, "1.4.130", st.Version)
	assert.Equal(t, ResultRelaunched, r.lastHistory().Result)
	_, err := os.Stat(filepath.Join(r.data, stage.UpdatesDir, msiJournalFile))
	assert.True(t, os.IsNotExist(err), "no journal left")
}

// An unsigned MSI goes by the sums and the bundle alone, and the history says so; a reboot Windows Installer asked for is suppressed and noted.
func TestAnUnsignedPackageIsRecordedAndASuppressedRebootNoted(t *testing.T) {
	r := newMSIRig(t)
	r.msi.signed = false
	r.msi.codes = []int{3010}
	res := r.run()
	assert.Equal(t, ResultRelaunched, res.Result)
	assert.Equal(t, "unsigned_package,reboot_suppressed", r.lastHistory().Reason)
}

// Declined in Windows' consent prompt (only 1602, 1625 and 1223 mean that): nothing changed, the old Coral starts again, nothing is retried.
func TestADeclinedConsentChangesNothingAndStartsTheOldCoral(t *testing.T) {
	for _, code := range []int{1602, 1625, 1223} {
		r := newMSIRig(t)
		r.msi.codes = []int{code}
		res := r.run()
		assert.Equal(t, ResultDeclined, res.Result, "code %d", code)
		assert.Equal(t, []string{"wait", "hold", "authenticode", "productcode", "msiexec /i", "start"}, r.msi.calls, "code %d: the old Coral is started, never the installer again", code)
		assert.Equal(t, ResultDeclined, r.lastHistory().Result)
		assert.Empty(t, r.state().Version, "nothing accepted")
	}
}

// Another install in progress, or any other failure (Windows Installer has rolled it back): the old Coral starts again and the history says why.
func TestAFailedOrBusyInstallStartsTheOldCoral(t *testing.T) {
	for code, stageName := range map[int]string{1618: "installer_busy", 1603: "msiexec_1603"} {
		r := newMSIRig(t)
		r.msi.codes = []int{code}
		res := r.run()
		assert.Equal(t, ResultFailed, res.Result)
		assert.Equal(t, stageName, res.FailedStage)
		assert.Equal(t, "start", r.msi.calls[len(r.msi.calls)-1])
		assert.NotContains(t, r.msi.calls, "healthy")
	}
}

// D: the bytes msiexec gets are the ones that were verified. A staged package changed after it was staged is refused before any prompt; a copy
// changed between the copy and the hold is refused by the hold's re-hash, before any prompt too.
func TestATamperedPackageIsRefusedBeforeAnyPrompt(t *testing.T) {
	r := newMSIRig(t)
	staged := filepath.Join(r.data, stage.UpdatesDir, stage.StagingDir, "1.4.130", msiAsset("1.4.130"))
	require.NoError(t, os.WriteFile(staged, []byte("something else entirely"), 0o600))
	res := r.run()
	assert.Equal(t, ResultRefused, res.Result)
	assert.NotContains(t, strings.Join(r.msi.calls, " "), "msiexec")
	assert.Equal(t, "start", r.msi.calls[len(r.msi.calls)-1], "the old Coral is started again")

	r = newMSIRig(t)
	r.msi.holdHook = func(path string) { require.NoError(t, os.WriteFile(path, []byte("swapped after the copy"), 0o600)) }
	res = r.run()
	assert.Equal(t, ResultRefused, res.Result)
	assert.Equal(t, verify.StagePackage, res.FailedStage)
	assert.NotContains(t, strings.Join(r.msi.calls, " "), "msiexec")
}

// (iv) An invalid signature, or one not from the pinned chain and subject, is refused even though the sums matched.
func TestAForeignOrInvalidSignatureIsRefused(t *testing.T) {
	r := newMSIRig(t)
	r.msi.authErr = errors.New("signed by another publisher")
	res := r.run()
	assert.Equal(t, ResultRefused, res.Result)
	assert.Equal(t, "authenticode", res.FailedStage)
	assert.NotContains(t, strings.Join(r.msi.calls, " "), "msiexec")
	assert.Equal(t, []bool{true}, r.msi.heldClosed)
}

// Coral still running after the wait: nothing is touched, and nothing is started (it is running).
func TestNothingIsTouchedWhileCoralRuns(t *testing.T) {
	r := newMSIRig(t)
	r.msi.coralGone = false
	res := r.run()
	assert.Equal(t, ResultCoralRunning, res.Result)
	assert.Equal(t, []string{"wait"}, r.msi.calls)
}

// A portable install, or none registered: unsupported, nothing done.
func TestAPortableInstallIsUnsupported(t *testing.T) {
	r := newMSIRig(t)
	r.msi.unsupport = "portable_install"
	res := r.run()
	assert.Equal(t, ResultUnsupported, res.Result)
	assert.Equal(t, "portable_install", res.FailedStage)
	assert.Empty(t, r.msi.calls)
}

// The new version installed but didn't come up: the person is told first, the new product is removed by its verified code, the previous
// version's package (verified again here) is installed, and the old Coral is started.
func TestANewVersionThatDoesNotComeUpIsRolledBackWithTwoPrompts(t *testing.T) {
	r := newMSIRig(t)
	r.msi.healthErr = []error{errors.New("not up")}
	res := r.run()
	assert.Equal(t, ResultRolledBack, res.Result, res.FailedStage)
	assert.Equal(t, "health", res.FailedStage)
	assert.Equal(t, []string{"wait", "hold", "authenticode", "productcode", "msiexec /i", "start", "healthy",
		"hold", "authenticode", "notify", "msiexec /x", "msiexec /i", "start"}, r.msi.calls)
	assert.Equal(t, newProduct, r.msi.msiArgs[1][1], "the product removed is the verified package's")
	assert.Equal(t, r.rbPath, r.msi.msiArgs[2][1], "the previous version's verified package")
	assert.Contains(t, r.msi.notes[0], "Windows will ask you twice")
	assert.Equal(t, ResultRolledBack, r.lastHistory().Result)
	assert.Empty(t, r.state().Version, "the new version is not accepted")
}

// C: a rollback package changed after it was verified is refused before any prompt; so is one of another version, and a registered product
// that isn't the verified package's. The new version then stays and the person is told.
func TestTheRollbackPackageIsVerifiedAgainWhereItIsUsed(t *testing.T) {
	r := newMSIRig(t)
	r.msi.healthErr = []error{errors.New("not up")}
	r.msi.holdHook = func(path string) {
		if path == r.rbPath {
			require.NoError(t, os.WriteFile(path, []byte("a swapped rollback package"), 0o600))
		}
	}
	res := r.run()
	assert.Equal(t, ResultFailed, res.Result)
	assert.Equal(t, "rollback_package_refused", res.FailedStage)
	assert.NotContains(t, strings.Join(r.msi.calls, " "), "msiexec /x", "no prompt for a package that didn't pass")

	r = newMSIRig(t)
	r.msi.healthErr = []error{errors.New("not up")}
	r.rbRec = &verify.Record{Version: "1.4.120", PackageSHA256: r.rbRec.PackageSHA256}
	res = r.run()
	assert.Equal(t, "rollback_package_refused", res.FailedStage, "the previous version must be the one installed before")

	r = newMSIRig(t)
	r.msi.healthErr = []error{errors.New("not up")}
	r.msi.registeredAfterInstall = "{99999999-0000-0000-0000-000000000000}" // not the verified package's product
	res = r.run()
	assert.Equal(t, "rollback_product_mismatch", res.FailedStage)
	assert.NotContains(t, strings.Join(r.msi.calls, " "), "msiexec /x")
}

// A declined removal leaves the new version (a complete install) and opens the previous version's installer for the person; a declined
// reinstall after the removal says so too. Neither is retried.
func TestADeclinedRollbackPromptLeavesSomethingThePersonCanFinish(t *testing.T) {
	r := newMSIRig(t)
	r.msi.healthErr = []error{errors.New("not up")}
	r.msi.codes = []int{0, 1602}
	res := r.run()
	assert.Equal(t, ResultFailed, res.Result)
	assert.Equal(t, "rollback_remove_1602", res.FailedStage)
	assert.True(t, res.RestartFailed)
	assert.Equal(t, filepath.Dir(r.rbPath), r.msi.shown)
	assert.Contains(t, r.msi.notes[len(r.msi.notes)-1], "is still installed")

	r = newMSIRig(t)
	r.msi.healthErr = []error{errors.New("not up")}
	r.msi.codes = []int{0, 0, 1602}
	res = r.run()
	assert.Equal(t, "rollback_install_1602", res.FailedStage)
	assert.Contains(t, r.msi.notes[len(r.msi.notes)-1], "was removed")
}

// No rollback package: the new version stays and the person is told how to go back.
func TestNoRollbackPackageLeavesTheNewVersionAndSaysSo(t *testing.T) {
	r := newMSIRig(t)
	r.msi.healthErr = []error{errors.New("not up")}
	r.cfg.Rollback = nil
	res := r.run()
	assert.Equal(t, "no_rollback_package", res.FailedStage)
	assert.NotContains(t, strings.Join(r.msi.calls, " "), "msiexec /x")
	assert.Len(t, r.msi.notes, 1)
}

// An apply that died after msiexec succeeded is finished by the next: the new product registered and Coral up → accepted; up after a start →
// accepted; not up → the rollback. A journal whose product isn't the registered one is just removed.
func TestAnInterruptedWindowsApplyIsFinishedByTheNext(t *testing.T) {
	r := newMSIRig(t)
	r.msi.registered = newProduct
	st, _ := stage.LoadStaged(r.data)
	j := msiJournal{Phase: "installed", From: "1.4.125", To: "1.4.130", Product: newProduct, Record: st.Record}
	require.NoError(t, writeMSIJournal(filepath.Join(r.data, stage.UpdatesDir), j))
	res := r.run()
	assert.Equal(t, ResultRelaunched, res.Result)
	assert.Equal(t, []string{"healthy"}, r.msi.calls, "nothing installed again")
	assert.Equal(t, "1.4.130", r.state().Version)

	r = newMSIRig(t)
	r.msi.registered = newProduct
	st, _ = stage.LoadStaged(r.data)
	j.Record = st.Record
	require.NoError(t, writeMSIJournal(filepath.Join(r.data, stage.UpdatesDir), j))
	r.msi.healthErr = []error{errors.New("down"), errors.New("still down")}
	res = r.run()
	assert.Equal(t, ResultRolledBack, res.Result, res.FailedStage)
	assert.Equal(t, "interrupted", res.FailedStage)

	r = newMSIRig(t)
	j.Product = "{00000000-0000-0000-0000-000000000000}"
	require.NoError(t, writeMSIJournal(filepath.Join(r.data, stage.UpdatesDir), j))
	res = r.run()
	assert.Equal(t, ResultRelaunched, res.Result, "a normal apply after the stale journal went")
	assert.Equal(t, "msiexec /i", r.msi.calls[4])
}

var _ io.Closer = fakeHeld{}
