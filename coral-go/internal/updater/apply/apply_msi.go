package apply

// The Windows apply (#162): the installed Coral is a per-machine MSI under Program Files, which this user's process can't replace by building
// beside it and swapping, so the update is a major upgrade that Windows Installer performs. The order and the failing closed are apply.Run's;
// what differs is the install step and the rollback. The design is the board's #1339 with the Orchestrator's answers and the Security
// Reviewer's conditions A-I (#1342):
//
//  1. Nothing running: Coral quit for the update (the server exits with code 5 and coral-tray quits on it); this waits for every process of the
//     install folder to end and never stops one. coral-update itself runs from a copy outside the install folder (B), so msiexec can replace it.
//  2. One apply, and none during a check (locks); a declined or failed attempt is never repeated by itself (A).
//  3. Verify again, from the staged files (sums + Sigstore bundle, and strictly newer than the installed version: I).
//  4. Copy the package into the private work folder while hashing it, then HOLD the copy: opened without following a reparse point, shared for
//     reading only (no write, no delete, no rename), and hashed again through that handle right before msiexec gets it (D).
//  5. Authenticode, when the MSI is signed: WinVerifyTrust with revocation, and the chain pinned to Microsoft Trusted Signing and the subject to
//     Subgentic LLC; an invalid or foreign signature is a refusal. An unsigned MSI goes by the sums and the bundle alone, and the history says so.
//  6. msiexec, from the system folder (E), run by this user, NOT elevated, with basic UI and a fixed set of properties: Windows Installer itself asks
//     for consent (UAC) for the per-machine part, showing the package, and no process of Coral's is ever elevated. Basic UI runs no ExitDialog,
//     so the MSI's own "Launch Coral" (#78) can't fire.
//  7. Start the new Coral as this user, never from the installer, and check it is up. If it isn't: the rollback, with the user told first: msiexec
//     /x the new product (its code read from the verified package and matched to the registered one: C), then /i the previous version's MSI,
//     verified again where it is used (C). Each is its own consent prompt.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/subgentic/coral/internal/updater/stage"
	"github.com/subgentic/coral/internal/updater/verify"
)

// ResultDeclined is an update the person declined in Windows' consent prompt: nothing was changed, the old Coral is started again.
const ResultDeclined = "declined"

// msiexec's exit codes that matter here (Windows Installer error codes).
const (
	msiSuccess        = 0
	msiRebootRequired = 3010 // done; the reboot it asks for is suppressed (REBOOT=ReallySuppress)
	msiUserExit       = 1602 // the person cancelled
	msiBusy           = 1618 // another installation is in progress
	msiPolicyRejected = 1625 // seen when elevation is refused
	errCancelled      = 1223 // ERROR_CANCELLED: the consent prompt was declined
)

// msiDeclined reports the codes that mean "the person said no, nothing changed": only these three.
func msiDeclined(code int) bool {
	return code == msiUserExit || code == msiPolicyRejected || code == errCancelled
}

// Held is a package file this program holds open for the install: no other process can write, delete or rename it until Close.
type Held interface {
	// Path is the file's path, for msiexec (which opens it for reading, which the hold allows).
	Path() string
	Close() error
}

// MSI is what the Windows apply does on the system; a test fakes it.
type MSI interface {
	// Install is the folder of the registered Coral product (by the MSI's UpgradeCode), never this program's own folder (it runs from a copy).
	Install() (string, error)
	// Unsupported says why this install can't be updated automatically ("" when it can): a portable install, no registered product.
	Unsupported(install string) string
	// WaitCoralGone waits for every process running from the install folder to end, and reports false when one didn't within the time.
	WaitCoralGone(ctx context.Context, install string) bool
	// Hold opens path without following a reparse point, holds it shared for reading only, and checks its sha256 through that handle.
	Hold(path, sha256 string) (Held, error)
	// Authenticode checks the held MSI's signature: signed is false for an unsigned file; err refuses an invalid signature or one not from the
	// pinned chain and subject.
	Authenticode(h Held) (signed bool, err error)
	// ProductCode is the held MSI's ProductCode property; RegisteredProduct is the code of the product registered PER MACHINE for Coral's
	// UpgradeCode (a per-user product with the same, public, UpgradeCode is never it: N1), and RegisteredVersion that product's version.
	ProductCode(h Held) (string, error)
	RegisteredProduct() (string, error)
	RegisteredVersion() (string, error)
	// Msiexec runs the system's msiexec with args, unelevated, and returns its exit code.
	Msiexec(ctx context.Context, args ...string) (int, error)
	// Start starts Coral (coral-tray) from the install folder as this user; Healthy waits until it is up.
	Start(ctx context.Context, install string) error
	Healthy(ctx context.Context, install string) error
	// Notify tells the person something while Coral is down (a message box).
	Notify(title, text string)
}

// Rollback is the previous version's package for the rollback, verified when it is called (at use, not when it was staged: C): its path and
// the record it was verified against. An error means there is none to use.
type Rollback func() (pkg string, rec *verify.Record, err error)

// MSIConfig is what a Windows apply needs; coral-update fills it with the compiled-in values, a test with its own.
type MSIConfig struct {
	DataDir   string
	Installed string
	Pins      verify.Pins
	Verifier  verify.BundleVerifier
	Asset     func(version string) string
	MSI       MSI
	Rollback  Rollback
	// ClickedVersion is the staged version the person saw when they clicked Install update (the one-time install marker's, #82). Required:
	// without it there was no click to honour, and an apply never asks Windows anything; when the staged version isn't it (a newer one staged
	// since, a stale marker), the apply refuses before any prompt.
	ClickedVersion string
	Now            func() time.Time
	MaxPkg         int64
	// BeforeInstall is a test seam: a crash, or a racing process, just before msiexec.
	BeforeInstall func()
}

func (c *MSIConfig) cfg() Config {
	return Config{DataDir: c.DataDir, Installed: c.Installed, Pins: c.Pins, Verifier: c.Verifier, Asset: c.Asset, MaxPkg: c.MaxPkg, Now: c.Now}
}

// msiInstallArgs is msiexec's fixed argument list for installing pkg, logging to logPath: nothing in it comes from a request (D).
func msiInstallArgs(pkg, logPath string) []string {
	return []string{"/i", pkg, "/qb!-", "/norestart", "REBOOT=ReallySuppress", "MSIRESTARTMANAGERCONTROL=Disable", "/l*v", logPath}
}

// msiUninstallArgs removes the product with code, the same way.
func msiUninstallArgs(code, logPath string) []string {
	return []string{"/x", code, "/qb!-", "/norestart", "REBOOT=ReallySuppress", "MSIRESTARTMANAGERCONTROL=Disable", "/l*v", logPath}
}

const msiJournalFile = "apply-msi-journal.json"

type msiJournal struct {
	Phase   string         `json:"phase"` // "installing": msiexec was started; "installed": it succeeded, the health check is next
	From    string         `json:"from"`
	To      string         `json:"to"`
	Product string         `json:"product"` // the new package's ProductCode
	Record  *verify.Record `json:"record,omitempty"`
}

// RunMSI applies the staged update on a Windows MSI install. It never returns an error: every outcome is a Result, recorded in the state.
func RunMSI(ctx context.Context, mc MSIConfig) Result {
	cfg := mc.cfg()
	res := Result{From: cfg.Installed}
	if cfg.MaxPkg <= 0 {
		cfg.MaxPkg = stage.DefaultMaxPackage
	}
	now := cfg.now()
	dir := filepath.Join(cfg.DataDir, stage.UpdatesDir)
	if err := verify.EnsureDir(dir); err != nil {
		res.Result, res.FailedStage = ResultFailed, "updates_folder"
		return res
	}
	if stage.CheckRunning(dir, now) {
		res.Result = ResultBusy
		return res
	}
	release, err := stage.TakeLock(dir, stage.ApplyLockFile, now)
	if err != nil {
		res.Result = ResultBusy
		return res
	}
	defer release()

	m := mc.MSI
	install, err := m.Install()
	if err != nil {
		res.Result, res.FailedStage = ResultUnsupported, "no_registered_product"
		return res
	}
	if why := m.Unsupported(install); why != "" {
		res.Result, res.FailedStage = ResultUnsupported, why
		return res
	}
	if r, ok := recoverMSI(ctx, mc, cfg, dir, install); ok {
		return r
	}
	staged, err := stage.LoadStaged(cfg.DataDir)
	if err != nil {
		res.Result = ResultNothingStaged
		startOld(ctx, m, install, &res)
		return res
	}
	res.To = staged.Version
	st, _ := verify.LoadState(dir)

	// 1: Coral quit for this; wait for every process of the install to end, never stop one.
	if !m.WaitCoralGone(ctx, install) {
		res.Result = ResultCoralRunning
		return res
	}
	// From here Coral is down: every outcome but success starts the old one again (the install is unchanged until msiexec succeeds).
	// The click this apply honours must be for what is staged now (#82's marker): nothing is verified or asked for otherwise.
	if mc.ClickedVersion == "" || mc.ClickedVersion != staged.Version {
		why := "stale_request"
		if mc.ClickedVersion == "" {
			why = "no_request"
		}
		cfg.history(&st, cfg.Installed, staged.Version, "refused", why)
		_ = verify.SaveState(dir, st)
		res.Result, res.FailedStage = ResultRefused, why
		startOld(ctx, m, install, &res)
		return res
	}
	refuse := func(stageName string, f *verify.Failure) Result {
		st.Refuse(staged.Version, f, now)
		cfg.history(&st, cfg.Installed, staged.Version, "refused", stageName)
		_ = verify.SaveState(dir, st)
		res.Result, res.FailedStage = ResultRefused, stageName
		startOld(ctx, m, install, &res)
		return res
	}
	fail := func(stageName string) Result {
		res.Result, res.FailedStage = ResultFailed, stageName
		startOld(ctx, m, install, &res)
		return res
	}

	// 3: the whole chain again, from the staged files (strictly newer than the installed version: I).
	rec, err := stage.Reverify(cfg.Verifier, cfg.Pins, staged, cfg.Asset, st, cfg.Installed, now, cfg.MaxPkg)
	if err != nil {
		var f *verify.Failure
		if errors.As(err, &f) {
			return refuse(f.Stage, f)
		}
		return fail("reverify")
	}

	// 4: the copy everything else uses, held from here until msiexec has finished with it.
	work := filepath.Join(dir, "apply-msi")
	if err := freshDir(work); err != nil {
		return fail("work_folder")
	}
	defer os.RemoveAll(work)
	pkg, err := copyVerified(staged.PackagePath, rec, work, cfg.MaxPkg)
	if err != nil {
		var f *verify.Failure
		if errors.As(err, &f) {
			return refuse(f.Stage, f)
		}
		return fail("copy")
	}
	held, err := m.Hold(pkg, rec.PackageSHA256)
	if err != nil {
		return refuse(verify.StagePackage, &verify.Failure{Stage: verify.StagePackage, Reason: "the copy could not be held and checked again: " + err.Error()})
	}
	closeHeld := func() {
		if held != nil {
			_ = held.Close()
			held = nil
		}
	}
	defer closeHeld()

	// 5: Authenticode, when signed: invalid or foreign is a refusal.
	signed, err := m.Authenticode(held)
	if err != nil {
		closeHeld()
		return refuse("authenticode", &verify.Failure{Stage: "authenticode", Reason: err.Error()})
	}
	product, err := m.ProductCode(held)
	if err != nil || product == "" {
		closeHeld()
		return refuse(verify.StagePackage, &verify.Failure{Stage: verify.StagePackage, Reason: "the package has no ProductCode"})
	}

	// 6: msiexec, unelevated, basic UI; Windows Installer asks for consent itself.
	if mc.BeforeInstall != nil {
		mc.BeforeInstall()
	}
	if err := writeMSIJournal(dir, msiJournal{Phase: "installing", From: cfg.Installed, To: rec.Version, Product: product, Record: rec}); err != nil {
		closeHeld()
		return fail("journal")
	}
	code, err := m.Msiexec(ctx, msiInstallArgs(held.Path(), filepath.Join(dir, "msi-install.log"))...)
	closeHeld()
	switch {
	case err != nil:
		removeMSIJournal(dir)
		return fail("msiexec")
	case msiDeclined(code):
		removeMSIJournal(dir)
		cfg.history(&st, cfg.Installed, rec.Version, ResultDeclined, "consent")
		_ = verify.SaveState(dir, st)
		res.Result, res.FailedStage = ResultDeclined, "consent"
		startOld(ctx, m, install, &res)
		return res
	case code == msiBusy:
		removeMSIJournal(dir)
		cfg.history(&st, cfg.Installed, rec.Version, "failed", "installer_busy")
		_ = verify.SaveState(dir, st)
		return fail("installer_busy")
	case code != msiSuccess && code != msiRebootRequired:
		// Windows Installer rolled the failed install back itself: the old version is still installed.
		removeMSIJournal(dir)
		cfg.history(&st, cfg.Installed, rec.Version, "failed", "msiexec_"+strconv.Itoa(code))
		_ = verify.SaveState(dir, st)
		return fail("msiexec_" + strconv.Itoa(code))
	}
	_ = writeMSIJournal(dir, msiJournal{Phase: "installed", From: cfg.Installed, To: rec.Version, Product: product, Record: rec})

	// 7: start the new Coral as this user, never from the installer; roll back if it doesn't come up.
	startErr := m.Start(ctx, install)
	if startErr == nil {
		startErr = m.Healthy(ctx, install)
	}
	if startErr != nil {
		return rollBackMSI(ctx, mc, cfg, dir, &st, install, product, rec.Version, "health")
	}
	removeMSIJournal(dir)
	if err := st.Accept(rec); err == nil {
		st.Stage = &verify.StageState{}
	}
	reason := ""
	if !signed {
		reason = "unsigned_package" // verified by the sums and the bundle alone; said in the history
	}
	if code == msiRebootRequired {
		reason = joinReason(reason, "reboot_suppressed")
	}
	cfg.history(&st, cfg.Installed, rec.Version, ResultRelaunched, reason)
	_ = verify.SaveState(dir, st)
	_ = os.RemoveAll(filepath.Join(dir, stage.StagingDir, rec.Version))
	res.Result = ResultRelaunched
	return res
}

func joinReason(a, b string) string {
	if a == "" {
		return b
	}
	return a + "," + b
}

// startOld starts the installed (unchanged) Coral again after an attempt that didn't install anything; a failure is said in the result.
func startOld(ctx context.Context, m MSI, install string, res *Result) {
	if err := m.Start(ctx, install); err != nil {
		res.RestartFailed = true
	}
}

// rollBackMSI puts the previous version back after the new one didn't come up: the person is told first, the new product is removed by its
// verified code, and the previous version's package, verified again here (C), is installed. Each step is its own consent prompt; when one is
// declined or fails the person is shown what to do, never left guessing.
func rollBackMSI(ctx context.Context, mc MSIConfig, cfg Config, dir string, st *verify.State, install, product, to, why string) Result {
	m := mc.MSI
	res := Result{Result: ResultRolledBack, From: cfg.Installed, To: to, FailedStage: why}
	done := func(result, stageName, history string) Result {
		removeMSIJournal(dir)
		res.Result, res.FailedStage = result, stageName
		cfg.history(st, cfg.Installed, to, history, stageName)
		st.Refuse(to, &verify.Failure{Stage: why, Reason: "the new version did not come up"}, cfg.now())
		_ = verify.SaveState(dir, *st)
		return res
	}
	if mc.Rollback == nil {
		m.Notify("Coral update", fmt.Sprintf("Coral %s was installed but didn't start, and the previous version isn't available to put back. Install Coral %s again from its release page.", to, cfg.Installed))
		return done(ResultFailed, "no_rollback_package", "failed")
	}
	// C: the code removed is the verified package's, and it must be the product registered for Coral now.
	if reg, err := m.RegisteredProduct(); err != nil || !sameProductCode(reg, product) {
		m.Notify("Coral update", fmt.Sprintf("Coral %s didn't start, and Coral couldn't confirm which product to remove. Nothing else was changed.", to))
		return done(ResultFailed, "rollback_product_mismatch", "failed")
	}
	pkg, rbRec, err := mc.Rollback()
	if err != nil || rbRec == nil || rbRec.Version != cfg.Installed {
		m.Notify("Coral update", fmt.Sprintf("Coral %s didn't start, and the previous version's package didn't pass its checks. Install Coral %s again from its release page.", to, cfg.Installed))
		return done(ResultFailed, "rollback_package_refused", "failed")
	}
	held, err := m.Hold(pkg, rbRec.PackageSHA256)
	if err != nil {
		m.Notify("Coral update", fmt.Sprintf("Coral %s didn't start, and the previous version's package changed after it was checked. Install Coral %s again from its release page.", to, cfg.Installed))
		return done(ResultFailed, "rollback_package_refused", "failed")
	}
	defer held.Close()
	if _, err := m.Authenticode(held); err != nil {
		m.Notify("Coral update", fmt.Sprintf("Coral %s didn't start, and the previous version's package has a signature Coral doesn't trust. Install Coral %s again from its release page.", to, cfg.Installed))
		return done(ResultFailed, "rollback_authenticode", "failed")
	}
	m.Notify("Coral update", fmt.Sprintf("Coral %s didn't start. Windows will ask you twice to put back Coral %s: once to remove %s, once to install %s.", to, cfg.Installed, to, cfg.Installed))
	code, err := m.Msiexec(ctx, msiUninstallArgs(product, filepath.Join(dir, "msi-rollback-remove.log"))...)
	if err != nil || (code != msiSuccess && code != msiRebootRequired) {
		// The new version stays: a complete install, just one that didn't come up. The person is pointed at the release, never at the kept
		// copy, which is no longer held once this returns (N3).
		m.Notify("Coral update", fmt.Sprintf("Coral %s is still installed. To go back, %s", to, releaseHint(cfg.Installed, rbRec)))
		res.RestartFailed = true
		return done(ResultFailed, "rollback_remove_"+codeOrErr(code, err), "restart_failed")
	}
	code, err = m.Msiexec(ctx, msiInstallArgs(held.Path(), filepath.Join(dir, "msi-rollback-install.log"))...)
	if err != nil || (code != msiSuccess && code != msiRebootRequired) {
		m.Notify("Coral update", fmt.Sprintf("Coral was removed but Coral %s wasn't installed again. To install it, %s", cfg.Installed, releaseHint(cfg.Installed, rbRec)))
		res.RestartFailed = true
		return done(ResultFailed, "rollback_install_"+codeOrErr(code, err), "restart_failed")
	}
	if err := m.Start(ctx, install); err != nil {
		res.RestartFailed = true
		return done(ResultRolledBack, why, "restart_failed")
	}
	return done(ResultRolledBack, why, ResultRolledBack)
}

// releaseHint tells the person where to get version's installer and what its SHA-256 must be (the verified sums entry), instead of pointing at
// the kept copy in a user-writable folder (N3).
func releaseHint(version string, rec *verify.Record) string {
	return fmt.Sprintf("download Coral %s from https://github.com/%s/releases/tag/v%s and run it (its SHA-256 is %s).", version, verify.Repo, version, rec.PackageSHA256)
}

func codeOrErr(code int, err error) string {
	if err != nil {
		return "error"
	}
	return strconv.Itoa(code)
}

// msiVersionIs reports whether an MSI ProductVersion ("1.4.130", or "1.4.130.0") is the release version ("1.4.130").
func msiVersionIs(msi, version string) bool {
	return msi == version || msi == version+".0"
}

// sameProductCode compares two ProductCodes ({GUID}, case-insensitive).
func sameProductCode(a, b string) bool {
	return a != "" && len(a) == len(b) && equalFoldASCII(a, b)
}

func equalFoldASCII(a, b string) bool {
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if 'a' <= x && x <= 'z' {
			x -= 'a' - 'A'
		}
		if 'a' <= y && y <= 'z' {
			y -= 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}

func writeMSIJournal(dir string, j msiJournal) error {
	b, err := json.Marshal(j)
	if err != nil {
		return err
	}
	return verify.WritePrivate(filepath.Join(dir, msiJournalFile), b)
}

func readMSIJournal(dir string) (msiJournal, bool) {
	b, err := verify.ReadPrivate(filepath.Join(dir, msiJournalFile))
	if err != nil {
		return msiJournal{}, false
	}
	var j msiJournal
	if json.Unmarshal(b, &j) != nil {
		return msiJournal{}, false
	}
	return j, true
}

func removeMSIJournal(dir string) { _ = os.Remove(filepath.Join(dir, msiJournalFile)) }

// recoverMSI finishes what an apply that died in the middle left, by what Windows reports as installed now (the journal is a file, not proof):
// the registered product is the journal's new one and Coral is up → accept it; it is the new one and Coral isn't up → it gets the health check,
// and the rollback if that fails; anything else → nothing was installed, the journal goes.
func recoverMSI(ctx context.Context, mc MSIConfig, cfg Config, dir, install string) (Result, bool) {
	j, ok := readMSIJournal(dir)
	if !ok {
		return Result{}, false
	}
	m := mc.MSI
	reg, err := m.RegisteredProduct()
	if err != nil || j.Product == "" || !sameProductCode(reg, j.Product) {
		removeMSIJournal(dir)
		return Result{}, false
	}
	// N2: the registered product must also BE the journal's new version, or nothing of this apply was installed and there is nothing to
	// finish or roll back (a journal is a file the same user can write).
	if v, err := m.RegisteredVersion(); err != nil || !msiVersionIs(v, j.To) {
		removeMSIJournal(dir)
		return Result{}, false
	}
	st, _ := verify.LoadState(dir)
	// The record counts only when it is the release still staged (the journal is a file, not proof).
	sr, lerr := stage.LoadStaged(cfg.DataDir)
	if j.Record == nil || lerr != nil || sr.Record.Version != j.Record.Version || sr.Record.PackageSHA256 != j.Record.PackageSHA256 {
		removeMSIJournal(dir)
		return Result{}, false
	}
	if err := m.Healthy(ctx, install); err != nil {
		if serr := m.Start(ctx, install); serr == nil {
			err = m.Healthy(ctx, install)
		}
		if err != nil {
			return rollBackMSI(ctx, mc, Config{DataDir: cfg.DataDir, Installed: j.From, Now: cfg.Now}, dir, &st, install, j.Product, j.To, "interrupted"), true
		}
	}
	removeMSIJournal(dir)
	if err := st.Accept(j.Record); err == nil {
		st.Stage = &verify.StageState{}
	}
	cfg.history(&st, j.From, j.To, ResultRelaunched, "")
	_ = verify.SaveState(dir, st)
	_ = os.RemoveAll(filepath.Join(dir, stage.StagingDir, sr.Version))
	return Result{Result: ResultRelaunched, From: j.From, To: j.To}, true
}
