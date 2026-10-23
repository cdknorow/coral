//go:build unix

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

// #6381, on a real terminal: the Hub question at startup is answered on a pty, as a person types it. Enter skips (as Enter declines the Terms) and the skip is stored where the dashboard's own
// Skip stores it (welcome_choice "skipped", welcome_pending "False") so no browser asks again; only the word yes connects, and a connect stores no skip. The prompt's own logic is in
// cloud_startup_test.go (a reader in place of a terminal); this runs the built server, so the terminal, the prompt's order after the first-start questions and the stored answer are the real ones.
func TestCloudStartupPromptOnATerminal(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the server")
	}
	bin := filepath.Join(t.TempDir(), "coral")
	if out, err := exec.Command("go", "build", "-tags", "dev", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, tc := range []struct {
		name, typed string
		skipped     bool
	}{{"Enter skips", "\r", true}, {"yes connects", "yes\r", false}} {
		t.Run(tc.name, func(t *testing.T) {
			port, deadHub := freeLoopbackPort(t), freeLoopbackPort(t)
			dataDir, err := os.MkdirTemp("", "coral6381")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(dataDir) })
			ptmx, tty, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(bin, "--host", "127.0.0.1", "--port", fmt.Sprint(port), "--no-browser", "--cloud-url", fmt.Sprintf("http://127.0.0.1:%d", deadHub), "--cloud-connect", "prompt")
			cmd.Env = append(os.Environ(), "CORAL_DATA_DIR="+dataDir, "HOME="+dataDir, "CORAL_NO_DEMO_SEED=1", "CORAL_TELEMETRY_DISABLED=1")
			cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			tty.Close()
			var mu sync.Mutex
			var out bytes.Buffer
			go func() {
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
			t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait(); ptmx.Close() })
			seen := func() string { mu.Lock(); defer mu.Unlock(); return out.String() }
			waitFor := func(text string) {
				t.Helper()
				for until := time.Now().Add(30 * time.Second); time.Now().Before(until); time.Sleep(50 * time.Millisecond) {
					if strings.Contains(seen(), text) {
						return
					}
				}
				t.Fatalf("the terminal never showed %q; it showed:\n%s", text, seen())
			}
			// a first start asks about remote access before the Hub: declined with Enter, as most people do
			waitFor("Enable remote access? [y/N]:")
			if _, err := ptmx.Write([]byte("\r")); err != nil {
				t.Fatal(err)
			}
			waitFor("Type yes to connect, or press Enter to skip:")
			if _, err := ptmx.Write([]byte(tc.typed)); err != nil {
				t.Fatal(err)
			}
			if tc.skipped {
				waitFor("Skipped.")
			} else {
				waitFor("Coral Hub")
			}
			time.Sleep(300 * time.Millisecond) // sleep-ok: the stored answer is written by the request the step makes right after it prints
			base := fmt.Sprintf("http://127.0.0.1:%d", port)
			got := storedWelcome(t, base, dataDir)
			if tc.skipped {
				if got["welcome_choice"] != "skipped" || got["welcome_pending"] != "False" {
					t.Fatalf("an Enter must store the dashboard's own skip, got %v\nterminal:\n%s", got, seen())
				}
				if !strings.Contains(seen(), "Settings > Coral Hub") {
					t.Fatalf("the skip says where Connect is still offered:\n%s", seen())
				}
			} else {
				if got["welcome_choice"] != "" {
					t.Fatalf("a yes is not a skip, but welcome_choice is %q\nterminal:\n%s", got["welcome_choice"], seen())
				}
				if strings.Contains(seen(), "Skipped.") {
					t.Fatalf("a yes was taken for a skip:\n%s", seen())
				}
			}
		})
	}
}

func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// storedWelcome reads the first-launch answer from /api/settings with the server's API key.
func storedWelcome(t *testing.T, base, dataDir string) map[string]string {
	t.Helper()
	key, err := os.ReadFile(filepath.Join(dataDir, "api_key"))
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("GET", base+"/api/settings", nil)
	req.Header.Set("X-API-Key", strings.TrimSpace(string(key)))
	var resp *http.Response
	for until := time.Now().Add(20 * time.Second); time.Now().Before(until); time.Sleep(100 * time.Millisecond) {
		if resp, err = http.DefaultClient.Do(req); err == nil {
			break
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("settings: %d %s", resp.StatusCode, body)
	}
	var wrapped struct {
		Settings map[string]any `json:"settings"`
	}
	raw := map[string]any{}
	if json.Unmarshal(body, &wrapped) == nil && wrapped.Settings != nil {
		raw = wrapped.Settings
	} else if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("settings: %v: %s", err, body)
	}
	out := map[string]string{}
	for _, k := range []string{"welcome_choice", "welcome_pending"} {
		if v, ok := raw[k]; ok {
			out[k] = fmt.Sprint(v)
		}
	}
	return out
}
