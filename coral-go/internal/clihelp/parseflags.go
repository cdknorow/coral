package clihelp

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// ParseFlags parses args into fs the way the CLIs speak: a flag the command does not have is `unknown option "--title"` with a "Did you mean" when it is a
// typo, never Go's own "flag provided but not defined: -title" and the dump of every flag. A failure prints the message and the command's usage on stderr and
// exits 2 (usage; nothing was sent). usage is the command's usage text, asked only when there is something to print.
func ParseFlags(fs *flag.FlagSet, args []string, usage func() string) {
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	err := fs.Parse(args)
	if err == nil {
		return
	}
	if errors.Is(err, flag.ErrHelp) {
		fmt.Println(usage())
		os.Exit(0)
	}
	fmt.Fprintf(os.Stderr, "Error: %s\n%s\n", FlagError(fs, err), usage())
	os.Exit(2)
}

// FlagError is the words for a flag package error: Go's text turned into the CLIs' wording.
func FlagError(fs *flag.FlagSet, err error) string {
	msg := err.Error()
	switch {
	case strings.HasPrefix(msg, "flag provided but not defined: "):
		name := strings.TrimLeft(strings.TrimPrefix(msg, "flag provided but not defined: "), "-")
		var known []string
		fs.VisitAll(func(f *flag.Flag) { known = append(known, "--"+f.Name) })
		out := fmt.Sprintf("unknown option %q", "--"+name)
		if near, ok := NearFlag("--"+name, known); ok {
			out += ". Did you mean " + near + "?"
		}
		return out
	case strings.HasPrefix(msg, "flag needs an argument: "):
		return "--" + strings.TrimLeft(strings.TrimPrefix(msg, "flag needs an argument: "), "-") + " needs a value"
	}
	return msg
}
