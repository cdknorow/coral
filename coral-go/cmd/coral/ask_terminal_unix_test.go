//go:build unix

package main

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

// #4052: "coral &" from a shell keeps the terminal as stdin and stdout, but Coral is a
// background job there, so a read would stop it (SIGTTIN) before the server listens. It must
// not ask: the server listens and nothing is saved. Run in the foreground on the same kind of
// terminal, it still asks, and listens once answered.
func TestFirstStartAsABackgroundJobDoesNotAsk(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the server")
	}
	bin := filepath.Join(t.TempDir(), "coral")
	if out, err := exec.Command("go", "build", "-tags", "dev", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, background := range []bool{true, false} {
		t.Run(fmt.Sprintf("background=%v", background), func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := ln.Addr().(*net.TCPAddr).Port
			ln.Close()
			dataDir, err := os.MkdirTemp("", "coral4052")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(dataDir) })

			// A shell that owns the terminal (its session and foreground job) starts Coral,
			// with job control on, either as "&" (its own background process group) or in
			// the foreground; it prints Coral's PID so the test can stop exactly that process.
			run := fmt.Sprintf("%q --host 127.0.0.1 --port %d --no-browser", bin, port)
			script := "set -m; " + run + " & echo CORALPID:$!; wait"
			if !background {
				script = "echo CORALPID:$$; exec " + run
			}
			ptmx, tty, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("/bin/sh", "-c", script)
			cmd.Env = append(os.Environ(), "CORAL_DATA_DIR="+dataDir, "HOME="+dataDir, "CORAL_NO_DEMO_SEED=1", "CORAL_CLOUD_URL=")
			cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			tty.Close()
			var mu sync.Mutex
			var out bytes.Buffer
			go func() { // drain the terminal, or Coral blocks writing its log to it
				buf := make([]byte, 4096)
				for {
					n, err := ptmx.Read(buf)
					mu.Lock()
					out.Write(buf[:n])
					mu.Unlock()
					if err != nil {
						return
					}
				}
			}()
			output := func() string { mu.Lock(); defer mu.Unlock(); return out.String() }
			pidRe := regexp.MustCompile(`CORALPID:(\d+)`)
			t.Cleanup(func() {
				if m := pidRe.FindStringSubmatch(output()); m != nil {
					if pid, err := strconv.Atoi(m[1]); err == nil && pid > 1 {
						syscall.Kill(pid, syscall.SIGKILL) // the Coral this test started, by its PID
					}
				}
				cmd.Process.Kill()
				cmd.Wait()
				ptmx.Close()
			})

			base := fmt.Sprintf("http://127.0.0.1:%d", port)
			healthy := func(within time.Duration) bool {
				deadline := time.Now().Add(within)
				for time.Now().Before(deadline) {
					if resp, err := http.Get(base + "/api/health"); err == nil {
						resp.Body.Close()
						return true
					}
					time.Sleep(100 * time.Millisecond)
				}
				return false
			}
			if background {
				if !healthy(20 * time.Second) {
					t.Fatalf("a background Coral never listened; terminal:\n%s", output())
				}
				if bytes.Contains([]byte(output()), []byte("Enable remote access?")) {
					t.Fatalf("a background Coral asked:\n%s", output())
				}
				if enabled, saved := remoteSetting(t, base, dataDir); saved || enabled {
					t.Fatalf("nothing was asked, so remote access is off and unsaved (enabled %v, saved %v)", enabled, saved)
				}
				return
			}
			deadline := time.Now().Add(20 * time.Second)
			for !bytes.Contains([]byte(output()), []byte("Enable remote access? [y/N]")) {
				if time.Now().After(deadline) {
					t.Fatalf("a foreground Coral on a terminal didn't ask; terminal:\n%s", output())
				}
				time.Sleep(100 * time.Millisecond)
			}
			if _, err := ptmx.Write([]byte("n\r")); err != nil {
				t.Fatal(err)
			}
			if !healthy(20 * time.Second) {
				t.Fatalf("Coral never listened after the answer; terminal:\n%s", output())
			}
			if enabled, saved := remoteSetting(t, base, dataDir); !saved || enabled {
				t.Fatalf("the answer no is saved (enabled %v, saved %v)", enabled, saved)
			}
		})
	}
}
