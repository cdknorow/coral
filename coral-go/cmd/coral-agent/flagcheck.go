package main

import (
	"fmt"
	"strings"

	"github.com/subgentic/coral/internal/clihelp"
)

// checkTaskFlags refuses an option that "task list", "task current" or "task detail" does not take (exit 2, with the usage), as
// coral-board does: they used to ignore it, so "task list --json" printed a table that a script took for JSON.
func checkTaskFlags(sub string, args []string) error {
	switch sub {
	case "list", "current":
		return clihelp.CheckFlags(args, clihelp.FlagSpec{Bools: []string{"--json"}})
	case "detail":
		return clihelp.CheckFlags(args, clihelp.FlagSpec{Bools: []string{"--json"}, Positional: 1})
	}
	return nil
}

// hasFlag reports whether args holds the option flag.
func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

// withoutFlag is args without the option flag.
func withoutFlag(args []string, flag string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if a != flag {
			out = append(out, a)
		}
	}
	return out
}

// optionArgs is a command line split into its positional arguments and its options, wherever the options stand.
type optionArgs struct {
	Positional []string
	Values     map[string]string
	Bools      map[string]bool
}

// splitOptions splits args into positional arguments and the options in spec (the names without dashes). Options may come before, between or
// after the positional arguments, and be written -name, --name, -name=v or --name=v, as the flag package took them. An unknown option, or a
// value option with no value, is an error. Go's flag package stops at the first positional argument, so "artifact download --output f <uri>"
// used to take "--output" for the uri.
func splitOptions(args []string, values, bools []string) (optionArgs, error) {
	known := map[string]bool{}
	for _, n := range values {
		known[n] = true
	}
	for _, n := range bools {
		known[n] = false
	}
	out := optionArgs{Values: map[string]string{}, Bools: map[string]bool{}}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			out.Positional = append(out.Positional, args[i+1:]...)
			break
		}
		if len(a) < 2 || a[0] != '-' || !clihelp.IsFlagLike(strings.SplitN(a, "=", 2)[0]) {
			out.Positional = append(out.Positional, a)
			continue
		}
		name, value, inline := strings.Cut(strings.TrimLeft(a, "-"), "=")
		takesValue, ok := known[name]
		if !ok {
			return out, fmt.Errorf("unknown option %q", "--"+name)
		}
		switch {
		case !takesValue && inline:
			return out, fmt.Errorf("--%s takes no value", name)
		case !takesValue:
			out.Bools[name] = true
		case inline:
			out.Values[name] = value
		case i+1 >= len(args):
			return out, fmt.Errorf("--%s needs a value", name)
		default:
			i++
			out.Values[name] = args[i]
		}
	}
	return out, nil
}
