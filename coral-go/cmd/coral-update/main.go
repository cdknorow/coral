// coral-update is Coral's updater program (#5231, #5292). Its argv is fixed: "check" finds the newest promoted release, has the verifier approve
// it and stages the package; "apply" is not built yet. It prints one JSON object on stdout, for display only, and takes no URL, version, path, hash
// or pid from whoever started it: the pins are compiled in, the data folder is resolved as Coral resolves its own, and the installed version is the
// one this program was built as. It is the only program that links the Sigstore verifier.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/updater/stage"
	"github.com/subgentic/coral/internal/updater/verify"
)

// applyEnabled is the off switch of the apply (#5325), one per platform (apply_switch.go): until a platform passes its upgrade tests it answers not_enabled.
// Compile-time on purpose: nothing a caller, a setting or the environment says turns it on.
var applyEnabled = applyOn(runtime.GOOS, applyEnabledDarwin, applyEnabledLinux, applyEnabledWindows, windowsApplyBuilt)

const (
	checkTimeout   = 30 * time.Minute
	bytesPerSecond = 8 << 20
)

// output is the whole of what is printed.
type output struct {
	Result      string `json:"result"`
	Installed   string `json:"installed,omitempty"`
	Version     string `json:"version,omitempty"`
	FailedStage string `json:"failed_stage,omitempty"`
	NextAttempt string `json:"next_attempt,omitempty"`
	// RestartFailed: the update was rolled back and the previous Coral could not be started again (#5419).
	RestartFailed bool `json:"restart_failed,omitempty"`
	// Trust is where the Sigstore trusted root came from: tuf, tuf_cache or embedded.
	Trust string `json:"trust,omitempty"`
}

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout))
}

// assetName is the package this platform stages, as release.yml names it in SHA256SUMS.txt. Windows stages the portable zip when the folder the
// program runs from is one the user can write to, else the MSI.
func assetName(goos, goarch string, portable bool) func(version string) string {
	return func(version string) string {
		switch goos {
		case "darwin":
			return "Coral.v" + version + ".dmg"
		case "windows":
			if portable {
				return "Coral-" + version + "-x64-portable.zip"
			}
			return "Coral-" + version + "-x64.msi"
		}
		return "coral-linux-" + goarch + "-" + version + ".tar.gz"
	}
}

// writableFolder reports whether dir can be written to by this user.
func writableFolder(dir string) bool {
	f, err := os.CreateTemp(dir, ".coral-update-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

func print(w io.Writer, o output) {
	_ = json.NewEncoder(w).Encode(o)
}

// run is main without the process: args after the program name, the environment, and where the JSON goes. It returns the exit code.
func run(args []string, getenv func(string) string, stdout io.Writer) int {
	// status only reads the updater's own files: no network, and it answers when the feature is off.
	if (len(args) == 1 && args[0] == "status") || (len(args) == 2 && args[0] == "status" && args[1] == "--json") {
		return runStatus(args, getenv, stdout)
	}
	if len(args) != 1 || (args[0] != "check" && args[0] != "apply") {
		fmt.Fprintln(os.Stderr, "usage: coral-update check|apply|status [--json]")
		return 2
	}
	// Only a prod release build updates itself: a dev, beta or dropboxers build, a build with no version, and an install turned off say so.
	if !config.TierChecksForUpdates || config.Version == "" || getenv("CORAL_AUTO_UPDATE") == "off" {
		print(stdout, output{Result: "off"})
		return 0
	}
	if args[0] == "apply" && !applyEnabled {
		print(stdout, output{Result: "not_enabled", Installed: config.Version})
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
	defer cancel()
	// The Sigstore trusted root, through TUF from the embedded starting root, refreshed at most daily (#5303).
	dataDir := config.ResolveCoralDir("")
	trust, source, err := verify.LoadTrust(ctx, filepath.Join(dataDir, stage.UpdatesDir, "tuf"))
	if err != nil {
		print(stdout, output{Result: stage.ResultFailed, Installed: config.Version, FailedStage: "trusted_root"})
		return 0
	}
	portable := false
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			portable = writableFolder(filepath.Dir(real))
		}
	}
	if args[0] == "apply" {
		return runApply(ctx, stdout, dataDir, trust, source)
	}
	res := stage.Check(ctx, stage.Config{
		DataDir:        dataDir,
		Installed:      config.Version,
		Pins:           verify.DefaultPins(),
		Verifier:       verify.Sigstore{Trust: trust},
		Fetcher:        &verify.Fetcher{Client: newClient()},
		Asset:          assetName(runtime.GOOS, runtime.GOARCH, portable),
		BytesPerSecond: bytesPerSecond,
	})
	print(stdout, output{Result: res.Result, Installed: res.Installed, Version: res.Version, FailedStage: res.FailedStage, NextAttempt: res.NextAttempt, Trust: string(source)})
	return 0
}
