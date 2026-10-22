//go:build !windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
	"time"
)

// The guardian's hidden mode (internal/ptymanager, #6129) starts from an init in a package every program that runs terminals links, so it runs before that program's main and before anything opens a data
// folder, a setting or the network. The ptymanager test binary alone cannot show that holds in the real programs, with their own packages' initialisers, so the programs are built here and the mode is started
// in each with an empty home and data folder: a file as its input is refused (exit 2), an empty pipe is its end (exit 0), and nothing is created.

func buildTags() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "-tags" {
				return s.Value
			}
		}
	}
	return ""
}

func TestTheGuardianModeRunsFirstInTheRealPrograms(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the programs")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain to build the programs with")
	}
	bin := t.TempDir()
	built := 0
	for _, prog := range []string{"coral", "coral-tray", "launch-coral", "coral-app"} {
		out := filepath.Join(bin, prog)
		args := []string{"build", "-o", out}
		tags := buildTags()
		if prog == "coral-app" { // the window needs its own tag, and only builds where the system has the web view
			tags = strings.Trim(tags+",webview", ",")
		}
		if tags != "" {
			args = append(args, "-tags", tags)
		}
		pkg := "github.com/subgentic/coral/cmd/" + prog
		// Only a program that links the package that starts terminals can be started as the guardian (and so needs the check): the window (coral-app) runs Coral as another program and does not link it, and
		// started with the argument it would be an ordinary window.
		listArgs := []string{"list", "-deps"}
		if tags != "" {
			listArgs = append(listArgs, "-tags", tags)
		}
		if deps, err := exec.Command("go", append(listArgs, pkg)...).Output(); err != nil || !strings.Contains("\n"+string(deps), "/internal/ptymanager\n") {
			t.Logf("%s does not link the terminal package: it is never started as the guardian, not checked", prog)
			continue
		}
		args = append(args, pkg)
		if b, err := exec.Command("go", args...).CombinedOutput(); err != nil {
			t.Logf("%s does not build here, not checked: %s", prog, strings.TrimSpace(string(b)[:min(len(b), 200)]))
			continue
		}
		built++
		t.Run(prog, func(t *testing.T) {
			data, home := t.TempDir(), t.TempDir()
			env := []string{"CORAL_PTY_GUARDIAN=1", "CORAL_DATA_DIR=" + data, "HOME=" + home, "XDG_CONFIG_HOME=" + home, "PATH=/usr/bin:/bin"}
			run := func(stdin *os.File) (code int, timedOut bool) {
				c := exec.Command(out, "__coral-pty-guardian")
				c.Env = env
				c.Stdin = stdin
				if err := c.Start(); err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() { done <- c.Wait() }()
				select {
				case err := <-done:
					var ee *exec.ExitError
					if errors.As(err, &ee) {
						return ee.ExitCode(), false
					}
					return 0, false
				case <-time.After(20 * time.Second):
					c.Process.Kill()
					return -1, true
				}
			}
			file := filepath.Join(t.TempDir(), "in")
			if err := os.WriteFile(file, []byte("add 12345 1 tag\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(file)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if code, to := run(f); to || code != 2 {
				t.Errorf("a file as input should be refused with exit 2, got %d (timed out: %v)", code, to)
			}
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			w.Close() // an empty pipe: EOF at once
			if code, to := run(r); to || code != 0 {
				t.Errorf("an empty pipe should end the mode with exit 0, got %d (timed out: %v)", code, to)
			}
			r.Close()
			for _, d := range []string{data, home} {
				if entries, _ := os.ReadDir(d); len(entries) != 0 {
					t.Errorf("the guardian mode of %s created %d entries in %s", prog, len(entries), d)
				}
			}
		})
	}
	if built == 0 {
		t.Skip("no program built here")
	}
	if built < 2 {
		t.Errorf("only %d program was checked: coral, coral-tray and launch-coral all link the terminal package and build here", built)
	}
}
