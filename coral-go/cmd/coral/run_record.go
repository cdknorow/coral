package main

import (
	"log"
	"os"
	"runtime"

	"github.com/subgentic/coral/internal/updater/runrecord"
)

// recordRun writes how this Coral was started (its own program, arguments and working directory) to <data dir>/updates/run.json, for coral-update's Linux apply to
// start Coral again after the swap (#5339): without it the apply could not restart and rolled every update back. The record is the server's own, never from a request.
// A failure only logs: Coral runs as before, and an update then ends with "no record of how Coral was started" instead of starting a copy blindly. A command line with a
// secret in a flag is not recorded (runrecord.ErrSecretFlag): start Coral with the secret in its environment or a file.
func recordRun(dataDir string, args []string) {
	if runtime.GOOS != "linux" {
		return // the other platforms' applies restart Coral their own way
	}
	exe, err := os.Executable() // absolute, and the file that is running: the apply checks it is Coral of the install
	if err != nil {
		log.Printf("[update] how Coral was started is not recorded: %v", err)
		return
	}
	cwd, err := os.Getwd()
	if err != nil {
		log.Printf("[update] how Coral was started is not recorded: %v", err)
		return
	}
	argv := append([]string{exe}, args[min(1, len(args)):]...)
	if err := runrecord.Write(dataDir, argv, os.Environ(), cwd); err != nil {
		log.Printf("[update] how Coral was started is not recorded (a Linux update will not be able to restart it): %v", err)
	}
}
