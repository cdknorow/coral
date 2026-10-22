// Package clihelp holds the help and free-text checks shared by the Coral
// CLIs (coral-board, coral-agent). Both run these before dispatching a
// command, so "--help" can never be taken as data: posted as a message,
// used as a board or workflow name, or sent as a task note.
package clihelp

import (
	"fmt"
	"regexp"
	"strings"
)

// IsHelpArg reports whether s asks for help.
func IsHelpArg(s string) bool { return s == "--help" || s == "-h" || s == "-help" }

// Requested reports whether a command's arguments ask for help: any argument
// that is exactly a help flag. A sentence that contains "--help" does not.
func Requested(rest []string) bool {
	for _, a := range rest {
		if a == "--" {
			return false
		}
		if IsHelpArg(a) {
			return true
		}
	}
	return false
}

// UsageLines returns the entries of a usage listing that document name: each
// line indented by exactly two spaces whose first word is name (or lists it
// in an a/b/c group), with its more deeply indented continuation lines.
func UsageLines(text, name string) string {
	var out []string
	in := false
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "   "):
			in = false
			if fields := strings.Fields(line); len(fields) > 0 {
				for _, alt := range strings.Split(fields[0], "/") {
					in = in || alt == name
				}
			}
		case strings.HasPrefix(line, "   "):
			// continuation of the current entry
		default:
			in = false
		}
		if in {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// flagLike matches an argument that is an option rather than text: -x, --name.
var flagLike = regexp.MustCompile(`^--?[A-Za-z][A-Za-z0-9-]*$`)

// IsFlagLike reports whether s is shaped like an option.
func IsFlagLike(s string) bool { return flagLike.MatchString(s) }

// CheckTextFlags rejects a text-taking option (one of textFlags, given
// without dashes) whose value is itself an option, as in --message --help or
// --reason=-x. Arguments after "--" are not checked.
func CheckTextFlags(args []string, textFlags map[string]bool) error {
	for i, a := range args {
		if a == "--" {
			return nil
		}
		if !strings.HasPrefix(a, "-") {
			continue
		}
		name, value, inline := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if !textFlags[name] {
			continue
		}
		if !inline {
			if i+1 >= len(args) {
				continue
			}
			value = args[i+1]
		}
		if IsFlagLike(value) {
			return fmt.Errorf("--%s needs text, got the option %q", name, value)
		}
	}
	return nil
}

// FlagSpec lists the options and positional arguments one command takes, for CheckFlags.
type FlagSpec struct {
	Bools      []string // options without a value, given with their dashes: "--json", "-a"
	Values     []string // options that take the next argument (or --name=value): "--id"
	Positional int      // how many arguments that are not options it takes; -1 for any number
}

// CheckFlags rejects what a command does not take: an option that is not in spec, a value option with no value, and more positional
// arguments than spec allows. Arguments after "--" are positional. A command that did not check used to ignore a misspelt option and
// carry on (coral-board read --jsno printed a table; task list --json printed a table, and a script read it as JSON).
func CheckFlags(args []string, spec FlagSpec) error {
	known := map[string]bool{}
	for _, f := range spec.Bools {
		known[f] = false
	}
	for _, f := range spec.Values {
		known[f] = true
	}
	var positional []string
	note := func(a string) { positional = append(positional, a) }
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			for _, rest := range args[i+1:] {
				note(rest)
			}
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" || !flagLike.MatchString(strings.SplitN(a, "=", 2)[0]) {
			note(a) // text, a number or a path: not an option
			continue
		}
		name, _, inline := strings.Cut(a, "=")
		takesValue, ok := known[name]
		if !ok {
			return fmt.Errorf("unknown option %q", name)
		}
		if takesValue && !inline {
			if i+1 >= len(args) {
				return fmt.Errorf("%s needs a value", name)
			}
			i++
		} else if !takesValue && inline {
			return fmt.Errorf("%s takes no value", name)
		}
	}
	if spec.Positional >= 0 && len(positional) > spec.Positional {
		return fmt.Errorf("unexpected argument %q", positional[spec.Positional])
	}
	return nil
}
