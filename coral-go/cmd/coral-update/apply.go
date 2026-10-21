package main

import (
	"context"
	"io"
	"os"
	"runtime"

	"github.com/sigstore/sigstore-go/pkg/root"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/updater/apply"
	"github.com/subgentic/coral/internal/updater/verify"
)

// parentAtStart is the pid of the process that started this one (the server, which quits to be replaced), taken before anything else: it is
// never an argument.
var parentAtStart = os.Getppid()

func runApply(ctx context.Context, stdout io.Writer, dataDir string, trust root.TrustedMaterial, source verify.TrustSource) int {
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
