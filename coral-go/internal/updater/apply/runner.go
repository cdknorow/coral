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

const maxToolOutput = 64 << 10

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
	return out.String(), nil
}

// limited keeps the first maxToolOutput bytes of a tool's output.
type limited struct{ b bytes.Buffer }

func (l *limited) Write(p []byte) (int, error) {
	if room := maxToolOutput - l.b.Len(); room > 0 {
		if len(p) > room {
			l.b.Write(p[:room])
		} else {
			l.b.Write(p)
		}
	}
	return len(p), nil
}

func (l *limited) String() string { return l.b.String() }
