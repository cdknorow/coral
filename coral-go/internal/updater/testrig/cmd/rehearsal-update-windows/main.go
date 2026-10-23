//go:build windows && windowsrehearsal

// rehearsal-update-windows is the Windows update rehearsal's own program (#6290), the counterpart of rehearsal-update on Linux. It is NOT a released
// program: it builds only with the windowsrehearsal tag, no release path passes that tag (a census holds that) and the guard test runs go list over every released
// program to show none links this package or the fake Sigstore. In the rehearsal it is installed AS coral-update.exe inside the version-N rehearsal MSI, so the
// real server's "Install update" route finds it where it finds the real one: it answers "status --json" for the route's preflight and "apply" from the private copy
// the server starts, and everything after the click is the real code: trigger.Consume (the one-time click), stage.Check (fetch, verify, stage), apply.RunMSI
// (verify again, Authenticode, an unelevated msiexec through Windows Installer, start the new Coral, health, rollback) with the real Windows platform.
// What differs from the real coral-update is only the world: the fake Sigstore's trust, the fake identities' pins and a fake GitHub on loopback. The real
// coral-update compiles in its trust, pins and addresses and cannot be pointed anywhere else (a test holds that).
//
// What the rehearsal wants is in <data>\rehearsal.json (written by the script, never by Coral): the installed version and its MSI, the version to serve and its
// MSI, and how to make the release wrong. The result is written to <data>\rehearsal-result.json (the updater's standard output is the null device). Keys are
// made when the program starts and are never written down; "check" stages the release for the dashboard (a throwaway key), and "apply" stages it again with its
// own key before it installs, since the key of "check" is gone.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/updater/apply"
	"github.com/subgentic/coral/internal/updater/stage"
	"github.com/subgentic/coral/internal/updater/testrig"
	"github.com/subgentic/coral/internal/updater/trigger"
	"github.com/subgentic/coral/internal/updater/verify"
)

type plan struct {
	InstalledMSI string   `json:"installed_msi"`
	NewMSI       string   `json:"new_msi"`
	NewVersion   string   `json:"new_version"`
	Tamper       bool     `json:"tamper"`         // serve the new MSI with one bit flipped
	NoRollback   bool     `json:"no_rollback"`    // do not publish the installed version (so no rollback package is kept)
	RequireSign  bool     `json:"require_signed"` // the apply refuses a package with no Authenticode signature
	StaleClick   bool     `json:"stale_click"`    // serve a newer release than the one the person clicked for (a newer one staged since)
	Unhealthy    bool     `json:"unhealthy"`      // the new Coral is reported not healthy once (the rollback's case)
	ServerArgs   []string `json:"server_args"`    // the server's flags when Coral is started (no spaces in any): --port, --host
}

func msiName(v string) string { return "Coral-" + v + "-x64.msi" }

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) >= 1 && args[0] == "status" {
		return status()
	}
	if len(args) == 2 && args[0] == "serve" { // "serve <install folder>": start Coral there as the apply does (the script's first start)
		p, err := readPlan(config.ResolveCoralDir(""))
		if err != nil {
			fmt.Fprintln(os.Stderr, "rehearsal.json:", err)
			return 2
		}
		if err := (&rehearsalMSI{WindowsMSI: &apply.WindowsMSI{}, serverArgs: p.ServerArgs}).Start(context.Background(), args[1]); err != nil {
			fmt.Fprintln(os.Stderr, "start:", err)
			return 1
		}
		return 0
	}
	if len(args) != 1 || (args[0] != "check" && args[0] != "apply") {
		fmt.Fprintln(os.Stderr, "usage: coral-update check|apply|status [--json]")
		return 2
	}
	dataDir := config.ResolveCoralDir("")
	p, err := readPlan(dataDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "rehearsal.json:", err)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	if args[0] == "apply" {
		// The real apply consumes the click first, and answers (and asks Windows nothing) when there is none, or it is stale or unreadable.
		m, err := trigger.Consume(dataDir, time.Now())
		switch {
		case err == nil:
			return applyRun(ctx, dataDir, p, m)
		case err == trigger.ErrNoMarker:
			return result(dataDir, map[string]any{"result": "not_requested"})
		case err == trigger.ErrMarkerExpired:
			return result(dataDir, map[string]any{"result": "request_expired"})
		default:
			return result(dataDir, map[string]any{"result": "request_invalid"})
		}
	}
	return checkRun(ctx, dataDir, p)
}

func readPlan(dataDir string) (plan, error) {
	var p plan
	b, err := os.ReadFile(filepath.Join(dataDir, "rehearsal.json"))
	if err != nil {
		return p, err
	}
	return p, json.Unmarshal(b, &p)
}

func result(dataDir string, v map[string]any) int {
	b, _ := json.Marshal(v)
	_ = os.WriteFile(filepath.Join(dataDir, "rehearsal-result.json"), b, 0o600)
	return 0
}

type world struct {
	gh     *testrig.FakeGitHub
	signer *testrig.Signer
	cfg    stage.Config
}

// newWorld publishes the new release (and the installed one, whose MSI is the rollback package) to a fake GitHub.
func newWorld(dataDir string, p plan) (*world, error) {
	signer, err := testrig.NewSigner()
	if err != nil {
		return nil, err
	}
	gh := testrig.NewFakeGitHub()
	publish := func(version, msi string, seq int64, latest bool) error {
		data, err := os.ReadFile(msi)
		if err != nil {
			return err
		}
		rel, err := signer.NewRelease(testrig.Spec{Version: version, Sequence: seq, Package: data, AssetName: msiName(version)})
		if err != nil {
			return err
		}
		gh.Publish(rel, latest)
		return nil
	}
	if !p.NoRollback {
		if err := publish(config.Version, p.InstalledMSI, 1, false); err != nil {
			gh.Close()
			return nil, err
		}
	}
	if err := publish(p.NewVersion, p.NewMSI, 2, true); err != nil {
		gh.Close()
		return nil, err
	}
	if p.Tamper {
		gh.Mutate = func(path string, b []byte) []byte {
			if strings.HasSuffix(path, "-x64.msi") && strings.Contains(path, p.NewVersion) && len(b) > 0 {
				b[len(b)/2] ^= 1
			}
			return b
		}
	}
	w := &world{gh: gh, signer: signer}
	w.cfg = stage.Config{DataDir: dataDir, Installed: config.Version, Pins: testrig.Pins(), Verifier: verify.Sigstore{Trust: signer.Trust()},
		Asset: msiName, Rollback: true}
	gh.Config(&w.cfg)
	return w, nil
}

// checkRun stages the new release for the dashboard to offer.
func checkRun(ctx context.Context, dataDir string, p plan) int {
	w, err := newWorld(dataDir, p)
	if err != nil {
		fmt.Fprintln(os.Stderr, "world:", err)
		return 2
	}
	defer w.gh.Close()
	res := stage.Check(ctx, w.cfg)
	result(dataDir, map[string]any{"step": "check", "result": res.Result, "version": res.Version, "failed_stage": res.FailedStage})
	if res.Result == stage.ResultStaged {
		return 0
	}
	return 1
}

// rehearsalMSI is the real Windows Installer and computer (apply.WindowsMSI) with two differences the runner needs: Notify writes to the result instead of opening a
// message box nobody can click, and Healthy can report the new Coral unhealthy once (the rollback's case). Everything else, msiexec included, is the real thing.
type rehearsalMSI struct {
	*apply.WindowsMSI
	unhealthy  bool
	serverArgs []string
	notes      []string
}

// Start starts Coral's server the way the tray's supervisor does (coral-tray --server-child, which is where the server's "quit for the update" is wired), headless:
// a runner has no tray to supervise it. The child stops when its standard input closes, so a long-lived pipe (ping) feeds it; cmd.exe holds that pipe after this
// process is gone.
func (r *rehearsalMSI) Start(ctx context.Context, install string) error {
	tray := filepath.Join(install, "coral-tray.exe")
	cmd := exec.Command(os.Getenv("ComSpec"))
	cmd.Dir = install
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CmdLine:       `cmd.exe /c "ping -n 3000 127.0.0.1 | "` + tray + `" --server-child ` + strings.Join(r.serverArgs, " ") + ` 2>>"` + filepath.Join(filepath.Dir(config.ResolveCoralDir("")), "server-child.log") + `""`,
		CreationFlags: 0x00000008 | 0x00000200, // DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP
		HideWindow:    true,
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func (r *rehearsalMSI) Notify(title, text string) { r.notes = append(r.notes, title+": "+text) }

func (r *rehearsalMSI) Healthy(ctx context.Context, install string) error {
	if r.unhealthy {
		r.unhealthy = false
		return fmt.Errorf("rehearsal: the new Coral is not healthy")
	}
	return r.WindowsMSI.Healthy(ctx, install)
}

// applyRun is the real Windows apply against the fake world. The staging of "check" is cleared first: it was verified against a key that is gone.
func applyRun(ctx context.Context, dataDir string, p plan, m trigger.Marker) int {
	_ = os.RemoveAll(filepath.Join(dataDir, stage.UpdatesDir, "staging"))
	_ = os.Remove(filepath.Join(dataDir, stage.UpdatesDir, verify.StateFile))
	w, err := newWorld(dataDir, p)
	if err != nil {
		return result(dataDir, map[string]any{"result": "rehearsal_error", "error": err.Error()})
	}
	defer w.gh.Close()
	if res := stage.Check(ctx, w.cfg); res.Result != stage.ResultStaged {
		return result(dataDir, map[string]any{"step": "check", "result": res.Result, "failed_stage": res.FailedStage})
	}
	msi := &rehearsalMSI{WindowsMSI: &apply.WindowsMSI{DataDir: dataDir}, unhealthy: p.Unhealthy, serverArgs: p.ServerArgs}
	clicked := m.Version
	if p.StaleClick {
		clicked = "0.0.1"
	}
	verifier := w.cfg.Verifier
	res := apply.RunMSI(ctx, apply.MSIConfig{
		DataDir: dataDir, Installed: config.Version, Pins: testrig.Pins(), Verifier: verifier, Asset: msiName, MSI: msi,
		RequireSigned: p.RequireSign, ClickedVersion: clicked,
		Rollback: func() (string, *verify.Record, error) {
			s, err := stage.LoadRollback(dataDir, config.Version)
			if err != nil {
				return "", nil, err
			}
			st, _ := verify.LoadState(filepath.Join(dataDir, stage.UpdatesDir))
			rec, err := stage.ReverifyRollback(verifier, testrig.Pins(), s, msiName, config.Version, st, time.Now(), stage.DefaultMaxPackage)
			if err != nil {
				return "", nil, err
			}
			return s.PackagePath, rec, nil
		},
	})
	// An answer that installed nothing and left no Coral: the real coral-update starts Coral again from the click's folder; so does this.
	out := map[string]any{"result": res.Result, "from": res.From, "to": res.To, "failed_stage": res.FailedStage, "notes": msi.notes}
	if res.Result == apply.ResultUnsupported || (res.Result == apply.ResultFailed && res.FailedStage == "updates_folder") {
		sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		out["restarted"] = msi.Start(sctx, m.InstallDir) == nil
	}
	return result(dataDir, out)
}

func status() int {
	msi := &apply.WindowsMSI{}
	kind, unsupported := "msi", ""
	if install, err := msi.Install(); err != nil {
		kind = "unregistered"
	} else {
		unsupported = msi.Unsupported(install)
	}
	b, _ := json.Marshal(map[string]any{"feature": "on", "apply_enabled": true, "installed": config.Version,
		"install": map[string]any{"kind": kind, "unsupported": unsupported, "writable": true}})
	fmt.Println(string(b))
	return 0
}
