package main

// Raw-input mode for input-fidelity tests (tests/stress/run_input_fidelity.sh).
//
// With MOCK_AGENT_RAW_DIR set, the mock reads its terminal the way an agent TUI does: stdin in
// raw mode (no line editing, no echo, Ctrl-C is a byte, not a signal), and bracketed paste
// enabled (ESC[?2004h, as Claude Code and Codex print it) unless MOCK_AGENT_BRACKETED_PASTE=0.
// Every byte it reads is appended, unchanged, to MOCK_AGENT_RAW_DIR/<session-id>.raw, so a test
// can compare what a terminal backend delivered with what was sent, byte for byte. Each
// submitted line (text up to a CR) is also echoed as "[mock-agent] raw: <Go-quoted text>".
// Off by default: the line-mode echo in main is what every other suite reads.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"
)

// rawInputDir is the folder raw-input mode records into, "" when it is off.
func rawInputDir() string { return os.Getenv("MOCK_AGENT_RAW_DIR") }

// runRawInput reads stdin in raw mode until it ends, recording every byte. It never returns
// an error to the caller: a test that finds no .raw file reports the failure.
func runRawInput(dir, sessionID string, turn func(string)) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		if old, err := term.MakeRaw(int(os.Stdin.Fd())); err == nil {
			defer term.Restore(int(os.Stdin.Fd()), old)
		} else {
			fmt.Fprintf(os.Stderr, "[mock-agent] raw mode: %v\r\n", err)
		}
	}
	if os.Getenv("MOCK_AGENT_BRACKETED_PASTE") != "0" {
		fmt.Print("\x1b[?2004h")
	}
	name := sessionID
	if name == "" {
		name = fmt.Sprintf("pid-%d", os.Getpid())
	}
	f, err := os.OpenFile(filepath.Join(dir, name+".raw"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[mock-agent] raw log: %v\r\n", err)
		return
	}
	defer f.Close()
	fmt.Print("[mock-agent] raw input ready\r\n")

	var line strings.Builder
	buf := make([]byte, 64*1024)
	for {
		n, err := os.Stdin.Read(buf)
		if n > 0 {
			_, _ = f.Write(buf[:n])
			for _, b := range buf[:n] {
				if b == '\r' {
					fmt.Printf("[mock-agent] raw: %q\r\n", line.String())
					turn(line.String())
					line.Reset()
					continue
				}
				line.WriteByte(b)
			}
		}
		if err != nil {
			return
		}
	}
}
