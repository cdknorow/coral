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

// #4360: a start with no terminal names the exact command to run, not just "run Coral from a terminal": the running program's own path, quoted when a shell
// would split it, so it can be pasted as it is.
func TestTerminalEULADialogNonTTYNamesTheCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the quoting cases are POSIX shell quoting; a Windows path is wrapped in double quotes")
	}
	cases := map[string]string{
		"/usr/local/bin/coral":                         "/usr/local/bin/coral",
		"/Applications/Coral.app/Contents/MacOS/coral": "/Applications/Coral.app/Contents/MacOS/coral",
		"/Users/a b/My Apps/coral":                     "'/Users/a b/My Apps/coral'",
		"/tmp/it's/coral":                              "'/tmp/it'\\''s/coral'",
	}
	oldExe := executablePath
	defer func() { executablePath = oldExe }()
	for path, want := range cases {
		executablePath = func() (string, error) { return path, nil }
		if got := eulaCommand(); got != want {
			t.Errorf("eulaCommand for %q = %q, want %q", path, got, want)
		}
	}
	executablePath = func() (string, error) { return "", os.ErrNotExist }
	if got := eulaCommand(); got != "coral" {
		t.Errorf("with no known path the command is the usual name, got %q", got)
	}

	// The message itself, written to stdout, carries that command.
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
	for _, want := range []string{"not been accepted", "type 'accept'", "'/opt/coral dir/coral'"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the message %q lacks %q", msg, want)
		}
	}
}
