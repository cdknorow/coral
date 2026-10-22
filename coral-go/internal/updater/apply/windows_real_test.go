//go:build windows

package apply

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"

	"github.com/subgentic/coral/internal/updater/stage"
	"github.com/subgentic/coral/internal/updater/verify"
)

// The real Windows upgrade, with the real previous and new release MSIs, on a CI runner (tests/release/upgrade_windows.ps1 sets these): the previous
// release is installed per machine, the new one is staged as the updater stages it and goes through the real RunMSI with the real Windows Installer
// (the runner is an administrator, so there is no consent prompt), and the real rollback. Only three things are replaced, none of them the install:
// Coral is never started (a CI runner has no tray to supervise it, so Start only records what is running), Healthy answers as the scenario says (the
// release MSIs cannot be made to fail to start), and the message box is only recorded.
//
// It installs and removes software on the machine it runs on. It runs only where upgrade_windows.ps1 says it is a throwaway machine
// (CORAL_APPLY_WINDOWS_CI=1), only as an administrator, and only when no Coral is installed.
const realFlag = "CORAL_APPLY_WINDOWS_CI"

type realPackages struct {
	oldMSI, newMSI, oldVersion, newVersion string
}

func realWindows(t *testing.T) realPackages {
	t.Helper()
	p := realPackages{
		oldMSI: os.Getenv("CORAL_APPLY_OLD_MSI"), newMSI: os.Getenv("CORAL_APPLY_NEW_MSI"),
		oldVersion: os.Getenv("CORAL_APPLY_OLD_VERSION"), newVersion: os.Getenv("CORAL_APPLY_NEW_VERSION"),
	}
	if p.oldMSI == "" || p.newMSI == "" || p.oldVersion == "" || p.newVersion == "" {
		t.Skip("set CORAL_APPLY_OLD_MSI, CORAL_APPLY_NEW_MSI, CORAL_APPLY_OLD_VERSION and CORAL_APPLY_NEW_VERSION (tests/release/upgrade_windows.ps1)")
	}
	if os.Getenv(realFlag) != "1" {
		t.Skipf("%s=1 is set by upgrade_windows.ps1 on a throwaway machine: this test installs and removes Coral", realFlag)
	}
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("an administrator's test: a per-machine install without a consent prompt")
	}
	if code, err := (&WindowsMSI{}).RegisteredProduct(); err == nil {
		t.Skipf("a Coral (%s) is installed on this machine: this test never touches an existing install", code)
	}
	// Per user too (a per-user product with Coral's UpgradeCode is not the one updated, but it is an install this test must not sit beside).
	if codes, err := relatedProducts(); err != nil || len(codes) > 0 {
		t.Skipf("a Coral product is registered on this machine (%v, %v): this test never touches an existing install", codes, err)
	}
	return p
}

// msiexecReal runs the system's msiexec quietly (the test installs and removes; the code under test is RunMSI).
func msiexecReal(t *testing.T, args ...string) int {
	t.Helper()
	code, err := (&WindowsMSI{}).Msiexec(context.Background(), args...)
	require.NoError(t, err)
	return code
}

func installReal(t *testing.T, msi string) {
	t.Helper()
	log := filepath.Join(t.TempDir(), "install.log")
	code := msiexecReal(t, "/i", msi, "/qn", "/norestart", "REBOOT=ReallySuppress", "/l*v", log)
	keepLog(t, log, "setup-install.log")
	if code != 0 && code != 3010 {
		b, _ := os.ReadFile(log)
		t.Fatalf("msiexec /i %s: exit %d\n%s", msi, code, tail(string(b), 3000))
	}
}

// keepLog copies a log into CORAL_APPLY_LOG_DIR\<test name>\ when that is set (upgrade_windows.ps1 -LogDir, which the runner workflow uploads as an artifact).
func keepLog(t *testing.T, from, name string) {
	dir := os.Getenv("CORAL_APPLY_LOG_DIR")
	if dir == "" {
		return
	}
	b, err := os.ReadFile(from)
	if err != nil {
		return
	}
	to := filepath.Join(dir, strings.ReplaceAll(t.Name(), "/", "_"))
	if os.MkdirAll(to, 0o755) == nil {
		_ = os.WriteFile(filepath.Join(to, name), b, 0o644)
	}
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

// uninstallRegistered removes whatever Coral product is registered, the test's own cleanup: nothing is left on the runner.
func uninstallRegistered() {
	if code, err := (&WindowsMSI{}).RegisteredProduct(); err == nil {
		_, _ = (&WindowsMSI{}).Msiexec(context.Background(), "/x", code, "/qn", "/norestart", "REBOOT=ReallySuppress")
	}
}

// realMSI is the real WindowsMSI with the three replacements of the comment at the top.
type realMSI struct {
	*WindowsMSI
	newVersion string

	mu            sync.Mutex
	msiexecs      [][]string
	runningAtOpen []string // the install's processes the moment the apply went to start Coral: none, if /qb!- started nothing
	startCalls    int
	healthCalls   int
	healthFails   func(registered string) bool
	notes         []string
}

func newRealMSI(data, newVersion string, healthFails func(registered string) bool) *realMSI {
	return &realMSI{WindowsMSI: &WindowsMSI{DataDir: data}, newVersion: newVersion, healthFails: healthFails}
}

func (r *realMSI) Msiexec(ctx context.Context, args ...string) (int, error) {
	r.mu.Lock()
	r.msiexecs = append(r.msiexecs, append([]string(nil), args...))
	r.mu.Unlock()
	return r.WindowsMSI.Msiexec(ctx, args...)
}

func (r *realMSI) Start(_ context.Context, install string) error {
	procs, _ := r.running(install)
	r.mu.Lock()
	r.startCalls++
	if r.startCalls == 1 {
		r.runningAtOpen = procs
	}
	r.mu.Unlock()
	return nil // Coral is not started on a CI runner
}

func (r *realMSI) Healthy(context.Context, string) error {
	r.mu.Lock()
	r.healthCalls++
	r.mu.Unlock()
	v, _ := r.WindowsMSI.RegisteredVersion()
	if r.healthFails != nil && r.healthFails(v) {
		return fmt.Errorf("the install of %s did not stay up (a scenario of the test)", v)
	}
	return nil
}

func (r *realMSI) Notify(title, text string) {
	r.mu.Lock()
	r.notes = append(r.notes, title+": "+text)
	r.mu.Unlock()
}

func (r *realMSI) installs() int {
	n := 0
	for _, a := range r.msiexecs {
		if len(a) > 0 && (a[0] == "/i" || a[0] == "/x") {
			n++
		}
	}
	return n
}

// stageReal stages the real new MSI the way the updater would have: a verified release record, the sums and the manifest with their bundles (the
// signer is the tests'), under data\updates\staging\<version>.
func stageReal(t *testing.T, data string, p realPackages) {
	t.Helper()
	pkg, err := os.ReadFile(p.newMSI)
	require.NoError(t, err)
	sums := []byte(msiSum(pkg) + "  " + msiAsset(p.newVersion) + "\n")
	m := verify.Manifest{Schema: 1, Repo: verify.Repo, Tag: "v" + p.newVersion, Version: p.newVersion, SourceSHA: msiTestCommit, SHA256SumsSHA: msiSum(sums), Sequence: 7,
		PromotedAt: "2026-10-22T10:00:00Z", ExpiresAt: "2026-11-22T10:00:00Z", Revoked: []string{}}
	manifest, _ := json.Marshal(m)
	cand := verify.Candidate{Manifest: manifest, ManifestBundle: msiBundleOf(manifest, ""), Sums: sums, SumsBundle: msiBundleOf(sums, msiTestCommit)}
	rec, err := verify.Release(msiTestSigner{}, verify.DefaultPins(), cand, msiAsset, verify.State{}, p.oldVersion, msiTestNow)
	require.NoError(t, err, "the new release is newer than the previous one")
	dir := filepath.Join(data, stage.UpdatesDir, stage.StagingDir, p.newVersion)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	for name, b := range map[string][]byte{stage.ManifestFile: manifest, stage.ManifestBundleFile: cand.ManifestBundle, stage.SumsFile: sums, stage.SumsBundleFile: cand.SumsBundle, msiAsset(p.newVersion): pkg} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), b, 0o600))
	}
	rb, _ := json.Marshal(rec)
	require.NoError(t, os.WriteFile(filepath.Join(dir, stage.RecordFile), rb, 0o600))
}

type realRig struct {
	t       *testing.T
	p       realPackages
	data    string
	msi     *realMSI
	cfg     MSIConfig
	install string
}

// newRealRig installs the previous release, stages the new one and returns the rig; the cleanup removes whatever is installed.
func newRealRig(t *testing.T, healthFails func(registered string) bool) *realRig {
	t.Helper()
	p := realWindows(t)
	t.Cleanup(uninstallRegistered)
	installReal(t, p.oldMSI)
	w := &WindowsMSI{}
	v, err := w.RegisteredVersion()
	require.NoError(t, err, "the previous release is registered per machine")
	require.Equal(t, p.oldVersion, v)
	install, err := w.Install()
	require.NoError(t, err)
	require.Empty(t, w.Unsupported(install), "installed under Program Files")

	data := t.TempDir()
	stageReal(t, data, p)
	r := &realRig{t: t, p: p, data: data, install: install, msi: newRealMSI(data, p.newVersion, healthFails)}
	// The apply's own logs (msiexec's verbose logs, apply.log) are kept before the data folder goes.
	t.Cleanup(func() {
		logs, _ := filepath.Glob(filepath.Join(data, stage.UpdatesDir, "*.log"))
		for _, l := range logs {
			keepLog(t, l, filepath.Base(l))
		}
	})
	oldBytes, err := os.ReadFile(p.oldMSI)
	require.NoError(t, err)
	r.cfg = MSIConfig{
		DataDir: data, Installed: p.oldVersion, Pins: verify.DefaultPins(), Verifier: msiTestSigner{}, Asset: msiAsset, MSI: r.msi,
		// The rollback package: the previous release's own MSI, as the check keeps it, verified again where it is used.
		Rollback: func() (string, *verify.Record, error) {
			return p.oldMSI, &verify.Record{Version: p.oldVersion, Asset: msiAsset(p.oldVersion), PackageSHA256: msiSum(oldBytes)}, nil
		},
		ClickedVersion: p.newVersion,
		Now:            func() time.Time { return msiTestNow },
	}
	return r
}

func (r *realRig) run() Result { return RunMSI(context.Background(), r.cfg) }

func (r *realRig) cmdVersion() string {
	out, err := exec.Command(filepath.Join(r.install, "coral.exe"), "--version").CombinedOutput()
	require.NoError(r.t, err, string(out))
	return strings.TrimSpace(string(out))
}

func (r *realRig) registered() string {
	v, err := (&WindowsMSI{}).RegisteredVersion()
	require.NoError(r.t, err)
	return v
}

// Previous release -> new release, for real: a major upgrade by Windows Installer, the old product replaced, the new coral.exe in place, and
// Windows Installer (/qb!-) starting no Coral on its own.
func TestRealWindowsUpgradeFromThePreviousRelease(t *testing.T) {
	r := newRealRig(t, nil)
	oldProduct, err := (&WindowsMSI{}).RegisteredProduct()
	require.NoError(t, err)
	require.Contains(t, r.cmdVersion(), r.p.oldVersion)

	res := r.run()
	require.Equal(t, ResultRelaunched, res.Result, "failed stage: %q", res.FailedStage)

	assert.Equal(t, r.p.newVersion, r.registered(), "the new release is the registered one")
	newProduct, err := (&WindowsMSI{}).RegisteredProduct()
	require.NoError(t, err)
	assert.NotEqual(t, oldProduct, newProduct, "a major upgrade replaced the product")
	assert.Contains(t, r.cmdVersion(), r.p.newVersion, "the new coral.exe is in the install folder")
	assert.Empty(t, r.msi.runningAtOpen, "msiexec /qb!- started nothing from the install folder: only the apply starts Coral, as the user")
	require.Len(t, r.msi.msiexecs, 1)
	args := r.msi.msiexecs[0]
	assert.Equal(t, "/i", args[0])
	assert.Contains(t, args, "/qb!-", "basic UI with no buttons: no ExitDialog, so the MSI's Launch Coral cannot fire")
	assert.Equal(t, 1, r.msi.installs(), "one install, no rollback")
	st, err := verify.LoadState(filepath.Join(r.data, stage.UpdatesDir))
	require.NoError(t, err)
	assert.Equal(t, r.p.newVersion, st.Version, "the high-water mark moved")
}

// A package changed after it was staged is refused by the verification that runs again, before Windows Installer is asked anything: the previous
// release stays installed.
func TestRealWindowsTamperedPackageIsRefused(t *testing.T) {
	r := newRealRig(t, nil)
	staged := filepath.Join(r.data, stage.UpdatesDir, stage.StagingDir, r.p.newVersion, msiAsset(r.p.newVersion))
	b, err := os.ReadFile(staged)
	require.NoError(t, err)
	b[len(b)/2] ^= 0xff
	require.NoError(t, os.WriteFile(staged, b, 0o600))

	res := r.run()
	assert.NotEqual(t, ResultRelaunched, res.Result)
	assert.Equal(t, ResultRefused, res.Result, "failed stage: %q", res.FailedStage)
	assert.Zero(t, r.msi.installs(), "msiexec never ran")
	assert.Equal(t, r.p.oldVersion, r.registered(), "the previous release is still installed")
	assert.Contains(t, r.cmdVersion(), r.p.oldVersion)
}

// A click that is not for what is staged (no marker, or another version) is refused before any prompt, and nothing is installed.
func TestRealWindowsStaleClickIsRefused(t *testing.T) {
	for name, clicked := range map[string]string{"no click": "", "another version": "0.0.1"} {
		t.Run(name, func(t *testing.T) {
			r := newRealRig(t, nil)
			r.cfg.ClickedVersion = clicked
			res := r.run()
			assert.Equal(t, ResultRefused, res.Result)
			assert.Zero(t, r.msi.installs())
			assert.Equal(t, r.p.oldVersion, r.registered())
		})
	}
}

// A new release that does not stay up is rolled back for real: the new product is removed and the previous release's MSI is installed again.
func TestRealWindowsHealthFailureIsRolledBack(t *testing.T) {
	r := newRealRig(t, func(registered string) bool {
		return registered != "" && registered != os.Getenv("CORAL_APPLY_OLD_VERSION")
	})
	res := r.run()
	assert.Equal(t, ResultRolledBack, res.Result, "failed stage: %q", res.FailedStage)
	assert.Equal(t, r.p.oldVersion, r.registered(), "the previous release is back")
	assert.Contains(t, r.cmdVersion(), r.p.oldVersion)
	// /i new, /x new, /i old
	var verbs []string
	for _, a := range r.msi.msiexecs {
		verbs = append(verbs, a[0])
	}
	assert.Equal(t, []string{"/i", "/x", "/i"}, verbs, "the install, then the rollback's removal and re-install")
	assert.NotEmpty(t, r.msi.notes, "the person was told before the rollback")
	st, err := verify.LoadState(filepath.Join(r.data, stage.UpdatesDir))
	require.NoError(t, err)
	assert.NotEqual(t, r.p.newVersion, st.Version, "a version that did not come up is not accepted")
}
