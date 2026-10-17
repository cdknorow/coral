package license

import (
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// With a stdin that is not a terminal and never closes, the terminal EULA dialog
// declines at once instead of blocking in read(0) (#3271).
func TestTerminalEULADialogNonTTYDoesNotBlock(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pw.Close()
	defer pr.Close()
	old := os.Stdin
	os.Stdin = pr
	defer func() { os.Stdin = old }()

	done := make(chan bool, 1)
	go func() { done <- TerminalEULADialog("terms") }()
	select {
	case accepted := <-done:
		if accepted {
			t.Fatal("a non-terminal stdin must not accept the terms")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("TerminalEULADialog blocked reading a non-TTY stdin")
	}
}

// #4360: a start with no terminal names the exact command to run, not just "run Coral from a terminal": the running program's own path, quoted for the shell
// when it needs it, so it can be pasted as it is.
func TestEULACommandLines(t *testing.T) {
	posix := map[string]string{
		"/usr/local/bin/coral":                         "/usr/local/bin/coral",
		"/Applications/Coral.app/Contents/MacOS/coral": "/Applications/Coral.app/Contents/MacOS/coral",
		"/Users/a b/My Apps/coral":                     "'/Users/a b/My Apps/coral'",
		"/tmp/it's/coral":                              "'/tmp/it'\\''s/coral'",
	}
	for path, want := range posix {
		if got := eulaCommandLines("linux", path); len(got) != 1 || got[0] != want {
			t.Errorf("linux %q = %q, want %q", path, got, want)
		}
	}
	if got := eulaCommandLines("linux", ""); len(got) != 1 || got[0] != "coral" {
		t.Errorf("an unknown path is the usual name, got %q", got)
	}
}

// A path with a control character is never printed raw, on any OS: a newline would start a second command line, an escape could rewrite the terminal.
func TestEULACommandLinesNeverPrintAControlCharacter(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "windows"} {
		for _, bad := range []string{"/tmp/a\nb/coral", "/tmp/a\rb/coral", "/tmp/a\x1b[2Jb/coral", "/tmp/a\x00b/coral", "/tmp/a\x7fb/coral", "C:\\x\x1fy\\coral.exe", "/tmp/tab\there/coral"} {
			got := eulaCommandLines(goos, bad)
			if len(got) != 1 || got[0] != "coral" {
				t.Errorf("%s %q must fall back to the word coral, got %q", goos, bad, got)
			}
		}
	}
}

// On Windows both forms are given: PowerShell's call operator with a single-quoted path (an apostrophe doubled) and the Command Prompt's double quotes.
func TestEULACommandLinesWindowsForms(t *testing.T) {
	got := eulaCommandLines("windows", `C:\Users\O'Brien\AppData\Local\Coral\coral.exe`)
	want := []string{
		`PowerShell:      & 'C:\Users\O''Brien\AppData\Local\Coral\coral.exe'`,
		`Command Prompt:  "C:\Users\O'Brien\AppData\Local\Coral\coral.exe"`,
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("got %q, want %q", got, want)
	}
}

// The message itself, written to stdout, carries that command.
func TestTerminalEULADialogNonTTYNamesTheCommand(t *testing.T) {
	oldExe := executablePath
	defer func() { executablePath = oldExe }()
	executablePath = func() (string, error) { return "/opt/coral dir/coral", nil }
	pr, pw, _ := os.Pipe()
	defer pw.Close()
	defer pr.Close()
	oldIn := os.Stdin
	os.Stdin = pr
	defer func() { os.Stdin = oldIn }()
	out, outW, _ := os.Pipe()
	oldOut := os.Stdout
	os.Stdout = outW
	accepted := TerminalEULADialog("terms")
	outW.Close()
	os.Stdout = oldOut
	buf := make([]byte, 4096)
	n, _ := out.Read(buf)
	msg := string(buf[:n])
	if accepted {
		t.Fatal("a non-terminal stdin must not accept the terms")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the POSIX form of the command is checked on the other systems; Windows prints both of its forms (TestEULACommandLinesWindowsForms)")
	}
	for _, want := range []string{"not been accepted", "type 'accept'", "'/opt/coral dir/coral'"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the message %q lacks %q", msg, want)
		}
	}
}
