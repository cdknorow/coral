package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	iofs "io/fs"

	"github.com/cdknorow/coral/internal/auth"
	"github.com/cdknorow/coral/internal/config"
)

// runAPIKey is `coral api-key` (#3936): it prints the API key a script on this computer sends
// to Coral (Authorization: Bearer <key>, or X-API-Key), read from Coral's data folder. It talks
// to no server, and only the user Coral runs as can read the file.
func runAPIKey(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("api-key", flag.ContinueOnError)
	fs.SetOutput(stderr)
	home := fs.String("home", "", "Coral's data folder, if the server was started with --home")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		fmt.Fprintln(stderr, "usage: coral api-key [--home DIR]   (a script sends it as: Authorization: Bearer <key>)")
		return 2
	}
	key, err := auth.ReadAPIKey(config.ResolveCoralDir(*home))
	if err != nil {
		if errors.Is(err, iofs.ErrNotExist) {
			fmt.Fprintln(stderr, "coral api-key: there is no API key yet: start Coral once (with --home if it uses one), then run this again")
			return 1
		}
		fmt.Fprintf(stderr, "coral api-key: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, key)
	return 0
}
