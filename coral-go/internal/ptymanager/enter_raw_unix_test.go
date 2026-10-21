//go:build !windows

package ptymanager

import (
	"strings"
	"testing"
	"time"
)

// An agent TUI runs its terminal in raw mode, where nothing translates the Enter a send types: it must
// arrive as the byte a keyboard's Enter sends, "\r" (a "\n" is Ctrl-J, a newline in the input box, not a
// submit). A cooked shell turns "\r" into "\n" itself (ICRNL), so a command line still runs (#4606).
func TestPTYSendLineDeliversACarriageReturnToARawModeReader(t *testing.T) {
	canFork(t)
	backend := NewPTYBackend()
	defer backend.Close()

	// stty raw: no ICRNL, no echo; od prints the next 7 bytes as they came.
	if err := backend.Spawn("raw-enter", "test", t.TempDir(), "sid-raw", "stty raw -echo; dd bs=1 count=7 2>/dev/null | od -An -c; sleep 1", 80, 24); err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}
	ch, err := backend.Attach("raw-enter", "ws-raw")
	if err != nil {
		t.Fatalf("Attach failed: %v", err)
	}
	time.Sleep(300 * time.Millisecond) // the shell is in raw mode before the line arrives
	if err := backend.SendLine("raw-enter", "submit"); err != nil {
		t.Fatalf("SendLine failed: %v", err)
	}
	var output strings.Builder
	timeout := time.After(5 * time.Second)
	for !strings.Contains(output.String(), `\r`) && !strings.Contains(output.String(), `\n`) {
		select {
		case data, ok := <-ch:
			if !ok {
				t.Fatalf("channel closed, got: %q", output.String())
			}
			output.Write(data)
		case <-timeout:
			t.Fatalf("timeout waiting for the bytes, got: %q", output.String())
		}
	}
	got := strings.Join(strings.Fields(output.String()), " ")
	if !strings.Contains(got, `s u b m i t \r`) {
		t.Fatalf("a raw-mode reader got %q; want the line followed by \\r", got)
	}
}

// A cooked shell still runs a line typed with the carriage return.
func TestPTYSendLineStillRunsACommandInACookedShell(t *testing.T) {
	t.Parallel()
	canFork(t)
	backend := NewPTYBackend()
	defer backend.Close()
	if err := backend.Spawn("cooked-enter", "test", t.TempDir(), "sid-cooked", "sh", 80, 24); err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}
	ch, err := backend.Attach("cooked-enter", "ws-cooked")
	if err != nil {
		t.Fatalf("Attach failed: %v", err)
	}
	if err := backend.SendLine("cooked-enter", "echo cooked-$((40+2))"); err != nil {
		t.Fatalf("SendLine failed: %v", err)
	}
	var output strings.Builder
	timeout := time.After(5 * time.Second)
	for !strings.Contains(output.String(), "cooked-42") {
		select {
		case data, ok := <-ch:
			if !ok {
				t.Fatalf("channel closed, got: %q", output.String())
			}
			output.Write(data)
		case <-timeout:
			t.Fatalf("the command did not run, got: %q", output.String())
		}
	}
}
