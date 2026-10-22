package main

import (
	"context"
	"errors"
	"io"
	"os"
	"runtime"
	"time"

	"github.com/sigstore/sigstore-go/pkg/root"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/updater/apply"
	"github.com/subgentic/coral/internal/updater/trigger"
	"github.com/subgentic/coral/internal/updater/verify"
)

// parentAtStart is the pid of the process that started this one (the server, which quits to be replaced), taken before anything else: it is
// never an argument.
var parentAtStart = os.Getppid()

func runApply(ctx context.Context, stdout io.Writer, dataDir string, trust root.TrustedMaterial, source verify.TrustSource) int {
	// Windows asks the person at this computer (UAC) to allow the install, so the apply starts only after a click in Settings: the dashboard's "Install update" leaves a
	// one-time marker (internal/updater/trigger) and this run consumes it before anything else. Run by hand, from a script or stale, it answers and asks nothing (#166, N5).
	if runtime.GOOS == "windows" {
		if res := installRequest(dataDir, time.Now()); res != "" {
			print(stdout, output{Result: res, Installed: config.Version, Trust: string(source)})
			return 0
		}
	}
	var platform apply.Platform
	pins := verify.DefaultPins()
	switch runtime.GOOS {
	case "darwin":
		platform = &apply.Mac{Run: apply.NewRunner(), TeamIDs: pins.MacTeamIDs, Parent: parentAtStart, Self: os.Getpid(), DataDir: dataDir}
	case "linux":
		platform = &apply.Linux{DataDir: dataDir, Parent: parentAtStart, Self: os.Getpid(), Arch: runtime.GOARCH}
	default:
		print(stdout, output{Result: "unsupported_platform", Installed: config.Version, Trust: string(source)})
		return 0
	}
	res := apply.Run(ctx, apply.Config{
		DataDir:   dataDir,
		Installed: config.Version,
		Pins:      pins,
		Verifier:  verify.Sigstore{Trust: trust},
		Asset:     assetName(runtime.GOOS, runtime.GOARCH, false),
		Platform:  platform,
	})
	print(stdout, output{Result: res.Result, Installed: res.From, Version: res.To, FailedStage: res.FailedStage, RestartFailed: res.RestartFailed, Trust: string(source)})
	return 0
}

// The results of an apply that was not asked for by a click in Settings: fixed words, printed for display only.
const (
	resultNotRequested   = "not_requested"
	resultRequestExpired = "request_expired"
	resultRequestInvalid = "request_invalid"
)

// installRequest consumes the dashboard's one-time install marker and returns "" when there was a good one, else the result word to print. The marker is deleted
// whatever it held, so a second run finds none.
func installRequest(dataDir string, now time.Time) string {
	_, err := trigger.Consume(dataDir, now)
	switch {
	case err == nil:
		return ""
	case errors.Is(err, trigger.ErrNoMarker):
		return resultNotRequested
	case errors.Is(err, trigger.ErrMarkerExpired):
		return resultRequestExpired
	default:
		return resultRequestInvalid
	}
}
