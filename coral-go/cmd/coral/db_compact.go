package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/startup"
)

// coral db compact [--home DIR]
//
// Shrinks sessions.db with Coral stopped: a backup first, then VACUUM (#3261).
// It refuses while a Coral server runs on the same data folder.
func runDB(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "compact" {
		fmt.Fprintln(stderr, "usage: coral db compact [--home DIR]   (stop Coral first; needs free disk about twice the database's size)")
		return 2
	}
	fs := flag.NewFlagSet("db compact", flag.ContinueOnError)
	fs.SetOutput(stderr)
	home := fs.String("home", "", "Coral's data folder, if the server runs with --home (default: CORAL_DATA_DIR or ~/.coral)")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 {
		return 2
	}
	cfg := config.Load(*home)
	if err := startup.Compact(context.Background(), cfg, stdout); err != nil {
		fmt.Fprintln(stderr, "coral db compact:", err)
		return 1
	}
	return 0
}
