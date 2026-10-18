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
//
// coral db rebuild-search [--home DIR]
//
// Rebuilds the full-text search index of sessions.db from the text stored with it, with Coral stopped: a backup first, then
// FTS5's rebuild, then the checks (#4735). The repair for "fts5: corruption found ...".
func runDB(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "compact" && args[0] != "rebuild-search") {
		fmt.Fprintln(stderr, "usage: coral db compact [--home DIR]        (stop Coral first; needs free disk about twice the database's size)")
		fmt.Fprintln(stderr, "       coral db rebuild-search [--home DIR]  (stop Coral first; repairs the search index, after a backup)")
		return 2
	}
	fs := flag.NewFlagSet("db "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	home := fs.String("home", "", "Coral's data folder, if the server runs with --home (default: CORAL_DATA_DIR or ~/.coral)")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 {
		return 2
	}
	cfg := config.Load(*home)
	if args[0] == "rebuild-search" {
		if err := startup.RebuildSearch(context.Background(), cfg, stdout); err != nil {
			fmt.Fprintln(stderr, "coral db rebuild-search:", err)
			return 1
		}
		return 0
	}
	if err := startup.Compact(context.Background(), cfg, stdout); err != nil {
		fmt.Fprintln(stderr, "coral db compact:", err)
		return 1
	}
	return 0
}
