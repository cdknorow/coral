//go:build !windows

package main

import (
	"context"

	"github.com/sigstore/sigstore-go/pkg/root"

	"github.com/subgentic/coral/internal/updater/apply"
	"github.com/subgentic/coral/internal/updater/verify"
)

// runApplyWindows is built for Windows only.
func runApplyWindows(context.Context, string, root.TrustedMaterial, verify.Pins) apply.Result {
	return apply.Result{Result: apply.ResultUnsupported, FailedStage: "not_windows"}
}

// installSupport is Windows' (apply_windows.go): elsewhere the status has no such field.
func installSupport() string { return "" }
