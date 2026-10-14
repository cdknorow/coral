package license

import (
	"os"
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
