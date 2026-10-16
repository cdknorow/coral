package main

import (
	"os"
	"testing"
)

// #4052: pipes on stdin and stdout (a CI runner, a service, the tray) are never asked; the
// pty test (unix) covers a terminal with a background job and a foreground one.
func TestCanAskAtTerminalNeedsTerminals(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if canAskAtTerminal(r, w) {
		t.Fatal("pipes are not a terminal")
	}
	if canAskAtTerminal(os.Stdin, w) && canAskAtTerminal(r, os.Stdout) {
		t.Fatal("one terminal is not enough")
	}
}
