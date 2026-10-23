//go:build linux

// rehearsal-update is the update rehearsal's own program (#5401). It is NOT a released program: no list in tests/release names it and a test runs
// go list over every released program to show none links this package or the fake Sigstore. It plays coral-update's part against a fake world, with the
// REAL code of the updater: stage.Check (fetch, verify, stage) and apply.Run (verify again, extract, check, swap, start, health, roll back) with the real
// Linux platform, wired with a fake Sigstore's trust, the fake identities' pins, and a fake GitHub on loopback. The real coral-update compiles in its
// trust, pins and addresses and cannot be pointed anywhere else (a test holds that); this program is how the rest of the chain is run for real.
//
//	rehearsal-update --data-dir D --install I --parent PID --installed N --package P.tar.gz --version N1 [--tamper bit] [--mode check|both]
//
// It prints one JSON line per step. In both mode it prints {"step":"ready"} when the release is staged, and then the apply waits for the process PID (the
// server it is to replace) to end and for no program of the install to be running: whoever started it ends the server then, as the server ends itself in
// the real flow. Keys are made when the program starts and are never written down.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/subgentic/coral/internal/updater/apply"
	"github.com/subgentic/coral/internal/updater/stage"
	"github.com/subgentic/coral/internal/updater/testrig"
	"github.com/subgentic/coral/internal/updater/verify"
)

func say(v any) {
	b, _ := json.Marshal(v)
	fmt.Println(string(b))
}

func main() { os.Exit(run()) }

func run() int {
	dataDir := flag.String("data-dir", "", "Coral's data folder")
	install := flag.String("install", "", "the install folder (its coral-update is the program this one stands in for)")
	parent := flag.Int("parent", 0, "the pid of the server to be replaced")
	installed := flag.String("installed", "", "the installed version")
	pkg := flag.String("package", "", "the new release's tarball")
	version := flag.String("version", "", "the new release's version")
	tamper := flag.String("tamper", "", `"bit": serve the package with one bit flipped; "break": answer 404 for the package (a download that fails, the control of "bit": a different result)`)
	mode := flag.String("mode", "both", "check or both")
	waitFor := flag.Duration("wait", 2*time.Minute, "how long the apply waits for the server to end")
	healthFor := flag.Duration("health-for", 30*time.Second, "how long the new install has to stay up")
	restage := flag.Bool("restage", false, "forget what an earlier run of this program staged before staging again: the fake world's keys are made when the program starts, so what an earlier run staged cannot be verified by this one (the ask rehearsal stages in one run and applies in another)")
	crash := flag.Bool("crash-after-swap", false, "the apply dies right after the swap (a panic here), and is then run again: the install must come out consistent")
	flag.Parse()
	if *dataDir == "" || *install == "" || *installed == "" || *pkg == "" || *version == "" {
		fmt.Fprintln(os.Stderr, "usage: rehearsal-update --data-dir D --install I --parent PID --installed N --package P --version N1 [--tamper bit] [--mode check|both]")
		return 2
	}
	data, err := os.ReadFile(*pkg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "package:", err)
		return 2
	}
	signer, err := testrig.NewSigner()
	if err != nil {
		fmt.Fprintln(os.Stderr, "signer:", err)
		return 2
	}
	rel, err := signer.NewRelease(testrig.Spec{Version: *version, Sequence: 1, Package: data})
	if err != nil {
		fmt.Fprintln(os.Stderr, "release:", err)
		return 2
	}
	gh := testrig.NewFakeGitHub()
	defer gh.Close()
	gh.Publish(rel, true)
	if *tamper == "bit" {
		gh.Mutate = func(p string, b []byte) []byte {
			if strings.HasSuffix(p, ".tar.gz") && len(b) > 0 {
				b[len(b)/2] ^= 1
			}
			return b
		}
	}

	if *restage {
		_ = os.RemoveAll(filepath.Join(*dataDir, stage.UpdatesDir, stage.StagingDir)) // the staging folder of this rehearsal's own data folder; the state file keeps saying what was staged
	}
	if *tamper == "break" {
		gh.Status["v"+*version+"/"+testrig.Asset(*version)] = http.StatusNotFound
	}
	cfg := stage.Config{DataDir: *dataDir, Installed: *installed, Pins: testrig.Pins(), Verifier: verify.Sigstore{Trust: signer.Trust()}, Asset: testrig.Asset}
	gh.Config(&cfg)
	cfg.Fetcher.Client = &http.Client{Timeout: 60 * time.Second, Transport: tracingTransport{base: http.DefaultTransport}} // the fetcher's own default, with a trace of what each download did
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	res := stage.Check(ctx, cfg)
	// What the check refused for, in the updater's own words (the state file's last failure), and whether the package was really asked for: a test that a tampered package was refused
	// for its digest, and not for a rig that was down, needs both.
	reason := ""
	if st, err := verify.LoadState(filepath.Join(*dataDir, stage.UpdatesDir)); err == nil && st.LastFailure != nil {
		reason = st.LastFailure.Reason
	}
	say(map[string]any{"step": "check", "result": res.Result, "installed": res.Installed, "version": res.Version, "failed_stage": res.FailedStage, "reason": reason, "package_requests": gh.Requests(testrig.Asset(*version))})
	if *mode == "check" || res.Result != stage.ResultStaged {
		if res.Result == stage.ResultStaged {
			return 0
		}
		return 1
	}

	platform := &apply.Linux{
		DataDir: *dataDir, Exe: filepath.Join(*install, "coral-update"), Parent: *parent, Self: os.Getpid(), Arch: runtime.GOARCH,
		Manager: func() bool { return false }, WaitFor: *waitFor, HealthFor: *healthFor,
	}
	say(map[string]any{"step": "ready"})
	acfg := apply.Config{
		DataDir: *dataDir, Installed: *installed, Pins: testrig.Pins(), Verifier: verify.Sigstore{Trust: signer.Trust()}, Asset: testrig.Asset, Platform: platform,
	}
	if *crash {
		acfg.AfterSwap = func() { panic("rehearsal: the apply dies after the swap") }
	}
	ares := applyOnce(ctx, acfg)
	if *crash {
		say(map[string]any{"step": "crashed"})
		acfg.AfterSwap = nil
		ares = apply.Run(ctx, acfg) // the next apply: it finds the journal and makes the install consistent
	}
	say(map[string]any{"step": "apply", "result": ares.Result, "from": ares.From, "to": ares.To, "failed_stage": ares.FailedStage})
	if ares.Result == apply.ResultRelaunched {
		return 0
	}
	return 1
}

// applyOnce is apply.Run, returning a zero result instead of dying when the run panics (the crash a rehearsal asks for).
func applyOnce(ctx context.Context, cfg apply.Config) (res apply.Result) {
	defer func() { _ = recover() }()
	return apply.Run(ctx, cfg)
}
