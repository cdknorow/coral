//go:build windows

package main

import (
	"context"
	"time"

	"github.com/subgentic/coral/internal/updater/apply"
)

func init() {
	registeredInstall = func() (string, error) { return (&apply.WindowsMSI{DataDir: dataDirForRestart()}).Install() }
	startCoralFrom = func(dataDir, dir string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return (&apply.WindowsMSI{DataDir: dataDir}).Start(ctx, dir)
	}
}
