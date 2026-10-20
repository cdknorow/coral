package main

import (
	"context"
	"io"
	"os"
	"runtime"

	"github.com/sigstore/sigstore-go/pkg/root"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/updater/apply"
	"github.com/cdknorow/coral/internal/updater/verify"
)

// parentAtStart is the pid of the process that started this one (the server, which quits to be replaced), taken before anything else: it is
// never an argument.
var parentAtStart = os.Getppid()

func runApply(ctx context.Context, stdout io.Writer, dataDir string, trust root.TrustedMaterial, source verify.TrustSource) int {
	if runtime.GOOS != "darwin" {
		print(stdout, output{Result: "unsupported_platform", Installed: config.Version, Trust: string(source)})
		return 0
	}
	pins := verify.DefaultPins()
	res := apply.Run(ctx, apply.Config{
		DataDir:   dataDir,
		Installed: config.Version,
		Pins:      pins,
		Verifier:  verify.Sigstore{Trust: trust},
		Asset:     assetName(runtime.GOOS, runtime.GOARCH, false),
		Platform:  &apply.Mac{Run: apply.NewRunner(), TeamIDs: pins.MacTeamIDs, Parent: parentAtStart, Self: os.Getpid()},
	})
	print(stdout, output{Result: res.Result, Installed: res.From, Version: res.To, FailedStage: res.FailedStage, Trust: string(source)})
	return 0
}
