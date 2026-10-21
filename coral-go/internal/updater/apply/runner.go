package apply

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// Runner runs one of the system's tools. The real one runs a program by its absolute path, with a scrubbed environment, under a timeout; a test
// replaces it to see the commands and to answer them.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (output string, err error)
}

// maxToolOutput bounds what one tool may print. It must be larger than the longest honest answer: `ps -axo pid=,command=` is about 500 KB on a busy Mac (#5581: with
// 64 KB the new Coral was not in what was read, so the apply never saw it come up and rolled every update back, and a running Coral could have been missed too).
const maxToolOutput = 16 << 20

type execRunner struct{ timeout time.Duration }

// NewRunner is the real Runner.
func NewRunner() Runner { return execRunner{timeout: 10 * time.Minute} }

func (r execRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "HOME=" + os.Getenv("HOME"), "LANG=C"}
	var out limited
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if err != nil {
		return out.String(), fmt.Errorf("%s: %w", name, err)
	}
	// Fail closed: an answer that was cut is not an answer (a process list with the Coral missing from it reads as "not running").
	if out.truncated {
		return "", fmt.Errorf("%s: the output is longer than %d bytes", name, maxToolOutput)
	}
	return out.String(), nil
}

// limited keeps the first maxToolOutput bytes of a tool's output and notes that it dropped the rest.
type limited struct {
	b         bytes.Buffer
	truncated bool
}

func (l *limited) Write(p []byte) (int, error) {
	n := len(p)
	room := maxToolOutput - l.b.Len()
	if room < 0 {
		room = 0
	}
	if len(p) > room {
		l.truncated = true
		p = p[:room]
	}
	l.b.Write(p)
	return n, nil // the writer never fails: the tool must not be killed by a full buffer, the truncation is noted
}

func (l *limited) String() string { return l.b.String() }
