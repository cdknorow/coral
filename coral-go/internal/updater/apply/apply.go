// Package apply is coral-update's apply step (#5325): it replaces the installed Coral with the update the check staged and the verifier approved,
// only when Coral is not running, keeping the old install for rollback, and failing closed at every step. macOS first (mac.go); the platform's
// part is behind Platform so the order and the rollback are tested without a Mac.
//
// The order, and why (the design is #5231 section 4 and #5245 section 12):
//
//  1. One apply, and none during a check (locks).
//  2. Verify again, from the staged files, against the compiled-in pins, with the trusted root: the record proves nothing.
//  3. Copy the package into a private folder while hashing it, and use only the copy: a swap of the staged file after it was verified is useless.
//  4. Build the new install beside the old (same volume) from the copy, and verify THAT, where it will run: signature, signer, notarization, version.
//     Steps 2 to 4 run while Coral is still running: a package that fails any of them costs the person nothing (#5982).
//  5. Say "ready to swap" (status.ReadyFile): the server that started this apply ends itself only on that. Then wait for Coral (its parent) to end;
//     it never stops Coral and never swaps while it runs.
//  6. Swap in one atomic step, keeping the old for rollback; start the new; check it is up; only then remove the old and move the high-water mark.
//  7. If anything after the swap fails, swap back and start the old again; if anything stops the apply after Coral ended and before the swap, start
//     the old one again (never leave the person with no Coral), and say restart_failed when that fails too.
package apply

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/subgentic/coral/internal/updater/stage"
	"github.com/subgentic/coral/internal/updater/status"
	"github.com/subgentic/coral/internal/updater/verify"
)

// What an apply ended as (the JSON "result"; display only).
const (
	ResultRelaunched    = "relaunched"          // updated, and the new one is up
	ResultRefused       = "refused"             // a verification refused it: nothing was changed
	ResultRolledBack    = "failed_rolled_back"  // the swap happened, the new one was not healthy, the old one is back
	ResultFailed        = "failed"              // nothing was changed (or the rollback itself failed: FailedStage says so)
	ResultNothingStaged = "nothing_staged"      //
	BusyCheck           = "check_running"       // FailedStage of a busy answer from the MSI apply: a check held the lock (the server had quit, Coral is started again)
	BusyApply           = "apply_running"       // ...or another apply did (it is replacing the install: Coral is not started under it)
	ResultBusy          = "busy"                // another apply or a check is running
	ResultCoralRunning  = "coral_running"       // Coral did not end in time: nothing was changed
	ResultUnsupported   = "unsupported_install" // Homebrew, a package manager, a volume without an atomic swap
	ResultNeedsManual   = "needs_manual"        // an install the user cannot write: the verified package is opened for them
	ResultPinsNotSet    = "pins_not_set"        // no signer is pinned for this platform: nothing is installed
)

// Result is one apply's outcome. It holds no path, URL or hash.
type Result struct {
	Result      string `json:"result"`
	From        string `json:"from,omitempty"`
	To          string `json:"to,omitempty"`
	FailedStage string `json:"failed_stage,omitempty"`
	// RestartFailed: the update was rolled back and the previous Coral could not be started again, so nothing is running (#5419). The user is told so.
	RestartFailed bool `json:"restart_failed,omitempty"`
}

// Platform is what differs per operating system.
type Platform interface {
	// Pinned reports whether a signer is pinned for this platform. Without one nothing is installed.
	Pinned() bool
	// Install is the install this program runs from (never an argument): the path of what is to be replaced.
	Install() (string, error)
	// Unsupported says why this install cannot be updated automatically ("" when it can): a package manager's, a volume without an atomic swap.
	Unsupported(install string) string
	// Writable reports whether this user can replace the install.
	Writable(install string) bool
	// Build makes the new install beside the old one from the package at pkg and returns its path; cleanup undoes what it mounted or made in
	// the work folder. It runs no check of its own beyond what building needs.
	Build(ctx context.Context, pkg, install, work string) (newPath string, cleanup func(), err error)
	// Version is the version of the install at path (its bundle's own).
	Version(path string) (string, error)
	// Verify checks the new install where it is: signature, signer (one of teamIDs), notarization and that it is version.
	Verify(ctx context.Context, newPath, version string) error
	// Swap exchanges the two installs in one atomic step.
	Swap(a, b string) error
	// Running reports, once and without waiting, whether the install is running (a process of it exists).
	Running(install string) bool
	// CanRestart says why Coral could not be started again after the swap ("" when it can). The apply asks it before it builds or ends anything and again
	// before the swap: an update that could end Coral and not bring it back does not begin.
	CanRestart(install string) string
	// Start starts the install. Healthy waits until it is up.
	Start(ctx context.Context, install string) error
	Healthy(ctx context.Context, install string) error
	// Show opens the verified package for the user to install by hand (an install this user cannot write).
	Show(ctx context.Context, pkg string) error
	// WaitCoralGone waits for Coral to end (its parent), and reports false when it did not within the time.
	WaitCoralGone(ctx context.Context, install string) bool
}

// Config is what an apply needs; coral-update fills it with the compiled-in values, a test with its own.
type Config struct {
	DataDir   string
	Installed string
	Pins      verify.Pins
	Verifier  verify.BundleVerifier
	Asset     func(version string) string
	Platform  Platform
	Now       func() time.Time
	MaxPkg    int64
	// BeforeSwap and AfterSwap are test seams: a crash, or a racing process, at those points.
	BeforeSwap, AfterSwap func()
}

func (c *Config) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

const journalFile = "apply-journal.json"

type journal struct {
	Phase   string `json:"phase"` // "swapping": before the swap; "swapped": after it
	From    string `json:"from"`
	To      string `json:"to"`
	NewPath string `json:"new_path"` // where the other install is (the old one once swapped)
	Install string `json:"install"`
	// Record is what was verified, so an apply that died after the new install came up can finish: remove the old one and move the high-water mark.
	Record *verify.Record `json:"record,omitempty"`
}

func writeJournal(dir string, j journal) error {
	b, _ := json.Marshal(j)
	return verify.WritePrivate(filepath.Join(dir, journalFile), b)
}

func readJournal(dir string) (journal, bool) {
	b, err := verify.ReadPrivate(filepath.Join(dir, journalFile))
	if err != nil {
		return journal{}, false
	}
	var j journal
	if json.Unmarshal(b, &j) != nil {
		return journal{}, false
	}
	return j, true
}

func removeJournal(dir string) { _ = os.Remove(filepath.Join(dir, journalFile)) }

// copyVerified copies the staged package into work while hashing it and returns the copy's path, after checking the hash is the verified sums
// entry's. The staged file is opened without following a link and read once; everything after this uses the copy.
func copyVerified(staged string, rec *verify.Record, work string, max int64) (string, error) {
	src, err := stage.OpenPlain(staged)
	if err != nil {
		return "", err
	}
	defer src.Close()
	dst := filepath.Join(work, rec.Asset)
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, verify.PrivateMode)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(src, max+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	if n > max {
		return "", errors.New("the package is larger than the limit")
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != rec.PackageSHA256 {
		return "", &verify.Failure{Stage: verify.StagePackage, Reason: fmt.Sprintf("the staged package is %s, the verified sums file says %s", got, rec.PackageSHA256)}
	}
	return dst, nil
}

// freshDir makes dir empty and 0700.
func freshDir(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return verify.EnsureDir(dir)
}

func (c *Config) history(st *verify.State, from, to, result, reason string) {
	st.History = append(st.History, verify.HistoryEntry{At: c.now().UTC().Format(time.RFC3339), From: from, To: to, By: "local", Result: result, Reason: reason})
}

// Run applies the staged update. It never returns an error: every outcome is a Result, and a refusal is also recorded in the state for Settings.
func Run(ctx context.Context, cfg Config) Result {
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
	defer os.Remove(filepath.Join(dir, status.ReadyFile)) // the marker is only true while this apply runs

	if !cfg.Platform.Pinned() {
		res.Result = ResultPinsNotSet
		return res
	}
	install, err := cfg.Platform.Install()
	if err != nil {
		res.Result, res.FailedStage = ResultFailed, "install"
		return res
	}
	if r, ok := recoverInterrupted(ctx, cfg, dir, install); ok {
		return r
	}
	if why := cfg.Platform.Unsupported(install); why != "" {
		res.Result, res.FailedStage = ResultUnsupported, why
		return res
	}
	// Before anything is built or ended: Coral must be startable again (#5419). Nothing is changed when it is not.
	if why := cfg.Platform.CanRestart(install); why != "" {
		res.Result, res.FailedStage = ResultUnsupported, why
		return res
	}
	staged, err := stage.LoadStaged(cfg.DataDir)
	if err != nil {
		res.Result = ResultNothingStaged
		return res
	}
	res.To = staged.Version
	st, _ := verify.LoadState(dir) // an unreadable state is a first run, never permission

	// 2: the whole chain again, from the staged files. Coral is still running: nothing here touches it, and a refusal here leaves it as it was.
	rec, err := stage.Reverify(cfg.Verifier, cfg.Pins, staged, cfg.Asset, st, cfg.Installed, now, cfg.MaxPkg)
	if err != nil {
		var f *verify.Failure
		if errors.As(err, &f) {
			st.Refuse(staged.Version, f, now)
			cfg.history(&st, cfg.Installed, staged.Version, "refused", f.Stage)
			_ = verify.SaveState(dir, st)
			res.Result, res.FailedStage = ResultRefused, f.Stage
			return res
		}
		res.Result, res.FailedStage = ResultFailed, "reverify"
		return res
	}

	// 3: the copy everything else uses.
	work := filepath.Join(dir, "apply")
	if err := freshDir(work); err != nil {
		res.Result, res.FailedStage = ResultFailed, "work_folder"
		return res
	}
	defer os.RemoveAll(work)
	pkg, err := copyVerified(staged.PackagePath, rec, work, cfg.MaxPkg)
	if err != nil {
		var f *verify.Failure
		if errors.As(err, &f) {
			st.Refuse(staged.Version, f, now)
			_ = verify.SaveState(dir, st)
			res.Result, res.FailedStage = ResultRefused, f.Stage
			return res
		}
		res.Result, res.FailedStage = ResultFailed, "copy"
		return res
	}

	// An install this user cannot write: no swap, no helper; the verified package is opened for them to install by hand.
	if !cfg.Platform.Writable(install) {
		if err := cfg.Platform.Show(ctx, pkg); err != nil {
			res.Result, res.FailedStage = ResultFailed, "show"
			return res
		}
		res.Result = ResultNeedsManual
		return res
	}

	// 4: the new install beside the old, verified where it will run.
	newPath, cleanup, err := cfg.Platform.Build(ctx, pkg, install, work)
	if err != nil {
		res.Result, res.FailedStage = ResultFailed, "build"
		return res
	}
	defer cleanup()
	if err := cfg.Platform.Verify(ctx, newPath, rec.Version); err != nil {
		_ = os.RemoveAll(newPath)
		st.Refuse(staged.Version, &verify.Failure{Stage: "platform_signature", Reason: err.Error()}, now)
		cfg.history(&st, cfg.Installed, rec.Version, "refused", "platform_signature")
		_ = verify.SaveState(dir, st)
		res.Result, res.FailedStage = ResultRefused, "platform_signature"
		return res
	}

	// 5: everything that can be done while Coral runs is done. Say so: the server that started this apply ends itself on this and not before (#5982), then wait for it to end;
	// this never stops Coral. When it does not end nothing is swapped and Coral is still the one that was running.
	if cfg.BeforeSwap != nil {
		cfg.BeforeSwap()
	}
	if err := writeReady(dir, rec.Version); err != nil {
		_ = os.RemoveAll(newPath)
		res.Result, res.FailedStage = ResultFailed, "ready_marker"
		return res
	}
	if !cfg.Platform.WaitCoralGone(ctx, install) {
		_ = os.RemoveAll(newPath)
		res.Result = ResultCoralRunning
		return res
	}
	// From here Coral has ended (this run's wait saw it go): any stop before the swap starts the old one again.
	// 6: swap, start, check; roll back on any failure from here.
	// The new install sits beside the old one between its first check and now; check it again right before the swap (it narrows what anyone who can
	// write that folder could change in between).
	if err := cfg.Platform.Verify(ctx, newPath, rec.Version); err != nil {
		_ = os.RemoveAll(newPath)
		st.Refuse(staged.Version, &verify.Failure{Stage: "platform_signature", Reason: err.Error()}, now)
		_ = verify.SaveState(dir, st)
		res.Result, res.FailedStage = ResultRefused, "platform_signature"
		return startPreviousAgain(ctx, cfg, dir, &st, install, res)
	}
	if why := cfg.Platform.CanRestart(install); why != "" { // the record may have gone while the package was copied and checked
		_ = os.RemoveAll(newPath)
		res.Result, res.FailedStage = ResultUnsupported, why
		return startPreviousAgain(ctx, cfg, dir, &st, install, res)
	}
	if err := writeJournal(dir, journal{Phase: "swapping", From: cfg.Installed, To: rec.Version, NewPath: newPath, Install: install, Record: rec}); err != nil {
		_ = os.RemoveAll(newPath)
		res.Result, res.FailedStage = ResultFailed, "journal"
		return startPreviousAgain(ctx, cfg, dir, &st, install, res)
	}
	if err := cfg.Platform.Swap(newPath, install); err != nil {
		_ = os.RemoveAll(newPath)
		removeJournal(dir)
		res.Result, res.FailedStage = ResultFailed, "swap"
		return startPreviousAgain(ctx, cfg, dir, &st, install, res)
	}
	_ = writeJournal(dir, journal{Phase: "swapped", From: cfg.Installed, To: rec.Version, NewPath: newPath, Install: install, Record: rec})
	if cfg.AfterSwap != nil {
		cfg.AfterSwap()
	}
	startErr := cfg.Platform.Start(ctx, install)
	if startErr == nil {
		startErr = cfg.Platform.Healthy(ctx, install)
	}
	if startErr != nil {
		return rollBack(ctx, cfg, dir, &st, install, newPath, rec.Version, "health")
	}
	// The new one is up: the old one goes, and the high-water mark moves.
	_ = os.RemoveAll(newPath)
	removeJournal(dir)
	if err := st.Accept(rec); err == nil {
		st.Stage = &verify.StageState{}
	}
	cfg.history(&st, cfg.Installed, rec.Version, "relaunched", "")
	_ = verify.SaveState(dir, st)
	_ = os.RemoveAll(filepath.Join(dir, stage.StagingDir, rec.Version))
	res.Result = ResultRelaunched
	return res
}

// rollBack exchanges the installs again and starts the old one.
func rollBack(ctx context.Context, cfg Config, dir string, st *verify.State, install, other, to, why string) Result {
	res := Result{Result: ResultRolledBack, From: cfg.Installed, To: to, FailedStage: why}
	if err := cfg.Platform.Swap(other, install); err != nil {
		res.Result, res.FailedStage = ResultFailed, "rollback_swap"
		return res
	}
	removeJournal(dir)
	_ = os.RemoveAll(other)
	restartErr := cfg.Platform.Start(ctx, install)
	if restartErr != nil {
		res.RestartFailed = true // nothing is running now: said in the result and in the history, never silence (#5419)
		cfg.history(st, cfg.Installed, to, "restart_failed", why)
	} else {
		cfg.history(st, cfg.Installed, to, "failed_rolled_back", why)
	}
	st.Refuse(to, &verify.Failure{Stage: why, Reason: "the new version did not come up; the previous one is back"}, cfg.now())
	_ = verify.SaveState(dir, *st)
	return res
}

// writeReady says the update is ready to swap: the marker the server that started this apply waits for before it ends itself.
func writeReady(dir, version string) error {
	b, _ := json.Marshal(status.Ready{Version: version, PID: os.Getpid(), At: time.Now().UTC().Format(time.RFC3339)})
	return verify.WritePrivate(filepath.Join(dir, status.ReadyFile), b)
}

// startPreviousAgain is what an apply does when it stops, after Coral ended, without having swapped: the person asked for an update and must not be left with no Coral. The previous
// install is intact (nothing was swapped), so it is started; when that fails too the result says restart_failed and the history records it (#5419, #5982). res keeps its own result.
func startPreviousAgain(ctx context.Context, cfg Config, dir string, st *verify.State, install string, res Result) Result {
	if err := cfg.Platform.Start(ctx, install); err != nil {
		res.RestartFailed = true
		cfg.history(st, cfg.Installed, res.To, "restart_failed", res.FailedStage)
		_ = verify.SaveState(dir, *st)
	}
	return res
}

// recoverInterrupted finishes what an apply that died in the middle left. Which install is at the install path decides: when it is the new
// version (the swap happened) and the old one is at the other path, the old one is put back; when it is the old version nothing is installed
// and the leftover copy is removed. It reports whether it ended this run.
func recoverInterrupted(ctx context.Context, cfg Config, dir, install string) (Result, bool) {
	j, ok := readJournal(dir)
	if !ok {
		return Result{}, false
	}
	defer removeJournal(dir)
	// The journal is a file; what it names is not trusted beyond what this run can derive: the other install is a sibling of the install this
	// program runs from, and a copy that was never swapped is the only thing removed without a rollback (#5338).
	if j.Install != install || filepath.Dir(j.NewPath) != filepath.Dir(install) || j.NewPath == install {
		return Result{}, false
	}
	now, err := cfg.Platform.Version(install)
	if err != nil || now != j.To {
		// The old install is in place. Only a journal written before the swap says what is at the other path is a leftover copy: after the swap an
		// install that cannot be read is the NEW one, and the other path then holds the old, which is the rollback.
		if j.Phase == "swapping" {
			_ = os.RemoveAll(j.NewPath)
		}
		return Result{}, false
	}
	if _, err := os.Lstat(j.NewPath); err != nil {
		return Result{}, false // the swapped-out install is gone: nothing to put back, and the new one runs
	}
	st, _ := verify.LoadState(dir)
	// The record in the journal is only a pointer: it counts when it is the very release that is still staged (the journal is a file, not proof).
	if sr, lerr := stage.LoadStaged(cfg.DataDir); j.Record != nil && lerr == nil && sr.Record.Version == j.Record.Version &&
		sr.Record.PackageSHA256 == j.Record.PackageSHA256 && sr.Record.SumsSHA256 == j.Record.SumsSHA256 && cfg.Platform.Running(install) {
		// The new install came up and the apply that started it died before it finished (a service manager that restarted Coral can end the
		// updater with it): the new one is running, so it stays. Finish: remove the old one, move the high-water mark.
		_ = os.RemoveAll(j.NewPath)
		if err := st.Accept(j.Record); err == nil {
			st.Stage = &verify.StageState{}
		}
		cfg.history(&st, j.From, j.To, "relaunched", "")
		_ = verify.SaveState(dir, st)
		_ = os.RemoveAll(filepath.Join(dir, stage.StagingDir, sr.Version)) // the staged record's own version folder, not the journal's text
		return Result{Result: ResultRelaunched, From: j.From, To: j.To}, true
	}
	return rollBack(ctx, cfg, dir, &st, install, j.NewPath, j.To, "interrupted"), true
}
