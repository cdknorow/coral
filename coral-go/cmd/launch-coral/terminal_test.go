package main

import (
	"errors"
	"regexp"
	"strings"
	"testing"
)

// #3588: a folder name (anything the target folder holds) never becomes part of a script or
// a command line: osascript gets it as an argument, and every title is safe text.
func TestTerminalCommandsNeverEmbedTheFolderName(t *testing.T) {
	safe := regexp.MustCompile(`^Coral [A-Za-z0-9 _.-]{0,80}$`)
	const attach = "TERMINFO_DIRS=/x/terminfo /x/tmux -S /x/sock attach -t claude-7d1e"
	for _, folder := range []string{`a"b`, `x" & (do shell script "touch M") & "`, "a&b", "a|b", "%PATH%", "a^b!c", "back\\slash", "new\nline", "é—✓", ""} {
		title := folder + " - claude"
		for _, c := range []struct {
			goos string
			look func(string) (string, error)
		}{
			{"darwin", nil},
			{"windows", func(string) (string, error) { return `C:\wt.exe`, nil }},
			{"windows", func(string) (string, error) { return "", errors.New("none") }},
			{"linux", func(n string) (string, error) { return "/usr/bin/" + n, nil }},
		} {
			cmds := terminalCommands(c.goos, attach, "claude-7d1e", title, c.look)
			if len(cmds) == 0 {
				t.Fatalf("%s: no command", c.goos)
			}
			for _, argv := range cmds {
				for i, arg := range argv {
					isScript := i > 0 && argv[i-1] == "-e" && argv[0] == "osascript"
					switch {
					case arg == attach:
						// The attach command is passed whole, as its own argument.
					case isScript:
						if folder != "" && strings.Contains(arg, folder) {
							t.Errorf("%s: the folder name is inside the script: %q", c.goos, arg)
						}
						if strings.Contains(arg, attach) {
							t.Errorf("%s: the attach command is inside the script: %q", c.goos, arg)
						}
					case strings.HasPrefix(arg, "Coral "):
						if !safe.MatchString(arg) {
							t.Errorf("%s: title %q isn't safe text", c.goos, arg)
						}
					}
				}
				if c.goos == "windows" {
					line := strings.Join(argv, " ")
					if strings.ContainsAny(line, "&|^%!\"<>") {
						t.Errorf("windows: cmd would re-parse %q", line)
					}
				}
			}
		}
	}
}

func TestSafeTitle(t *testing.T) {
	for in, want := range map[string]string{
		"repo - claude": "Coral repo - claude",
		`a"b&c|d%e`:     "Coral a_b_c_d_e",
		"":              "Coral ",
		"é":             "Coral _",
	} {
		if got := safeTitle(in); got != want {
			t.Errorf("safeTitle(%q) = %q, want %q", in, got, want)
		}
	}
	if got := safeTitle(strings.Repeat("a", 200)); len(got) != len("Coral ")+80 {
		t.Errorf("not capped: %d", len(got))
	}
}

// The osascript text is fixed: whatever the folder, the -e lines are the same, so nothing
// from a title or the attach command can ever be read as AppleScript.
func TestTerminalScriptsAreConstant(t *testing.T) {
	scripts := func(title, attach string) []string {
		var out []string
		for _, argv := range terminalCommands("darwin", attach, "claude-7d1e", title, nil) {
			for i := 1; i < len(argv); i++ {
				if argv[i-1] == "-e" {
					out = append(out, argv[i])
				}
			}
		}
		return out
	}
	want := strings.Join(scripts("plain - claude", "tmux attach -t a"), "\n")
	for _, title := range []string{`x" & (do shell script "touch M") & "`, "a&b - claude", "other"} {
		if got := strings.Join(scripts(title, `tmux "attach" -t b`), "\n"); got != want {
			t.Errorf("the script changed with the title %q:\n%s", title, got)
		}
	}
}

// The session name only reaches cmd as safe text too (defence in depth: Coral names
// sessions <type>-<uuid>).
func TestWindowsSessionTextIsSafe(t *testing.T) {
	for _, look := range []func(string) (string, error){
		func(string) (string, error) { return `C:\wt.exe`, nil },
		func(string) (string, error) { return "", errors.New("none") },
	} {
		argv := terminalCommands("windows", "", `claude&calc|x%PATH%"`, "t", look)[0]
		if line := strings.Join(argv, " "); strings.ContainsAny(line, "&|^%!\"<>") {
			t.Errorf("cmd would re-parse %q", line)
		}
	}
}
