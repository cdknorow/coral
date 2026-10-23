package main

import (
	"flag"
	"os"

	"github.com/subgentic/coral/internal/config"
)

// runningServerURL is where a command reaches this computer's Coral (#235). --url, CORAL_URL and CORAL_PORT say so when given;
// otherwise the address the server for the data folder (--home, else CORAL_DATA_DIR or ~/.coral) recorded in its lock file, so a
// Coral started on another port is found; and only then base, the platform's default port.
func runningServerURL(fs *flag.FlagSet, base, home string) string {
	given := false
	fs.Visit(func(f *flag.Flag) { given = given || f.Name == "url" })
	if given || os.Getenv("CORAL_URL") != "" || os.Getenv("CORAL_PORT") != "" {
		return base
	}
	if _, u := config.RecordedServer(config.ResolveCoralDir(home)); u != "" {
		return u
	}
	return base
}
