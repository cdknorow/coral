package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A first start with a stdin that is not a terminal and never closes (a service,
// or a shell that started Coral in the background) must not block in a prompt: the
// server comes up, remote access stays off and nothing is saved (#3271).
func TestFirstStartWithOpenNonTTYStdinDoesNotBlock(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the server")
	}
	bin := filepath.Join(t.TempDir(), "coral")
	if runtime.GOOS == "windows" {
		bin += ".exe" // exec only runs a name with a PATHEXT extension there (#3914)
	}
	if out, err := exec.Command("go", "build", "-tags", "dev", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	// A pipe whose write end stays open: reading it blocks, never EOF.
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pw.Close(); pr.Close() })

	dataDir, err := os.MkdirTemp("", "coral3271")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dataDir) })
	cmd := exec.Command(bin, "--host", "127.0.0.1", "--port", fmt.Sprint(port), "--no-browser")
	cmd.Env = append(os.Environ(), "CORAL_DATA_DIR="+dataDir, "HOME="+dataDir, "USERPROFILE="+dataDir, "CORAL_NO_DEMO_SEED=1")
	cmd.Stdin = pr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(20 * time.Second)
	for {
		resp, err := http.Get(base + "/api/health")
		if err == nil {
			resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server never answered /api/health with an open non-TTY stdin: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}

	if enabled, saved := remoteSetting(t, base, dataDir); saved || enabled {
		t.Fatalf("remote access must stay off and unsaved when nothing was asked (enabled %v, saved %v)", enabled, saved)
	}
}

// remoteSetting reads the saved remote access answer from /api/system/privacy, with the
// server's API key (strict local auth is the default since #3936: without a credential the
// read is refused and proves nothing). saved is false when nothing was saved.
func remoteSetting(t *testing.T, base, dataDir string) (enabled, saved bool) {
	t.Helper()
	key, err := os.ReadFile(filepath.Join(dataDir, "api_key"))
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("GET", base+"/api/system/privacy", nil)
	req.Header.Set("X-API-Key", strings.TrimSpace(string(key)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("privacy: %d %s", resp.StatusCode, body)
	}
	var privacy struct {
		Enabled    *bool `json:"remote_access_enabled"`
		Configured *bool `json:"remote_access_configured"`
	}
	if err := json.Unmarshal(body, &privacy); err != nil || privacy.Enabled == nil || privacy.Configured == nil {
		t.Fatalf("privacy: %v: %s", err, body)
	}
	return *privacy.Enabled, *privacy.Configured
}
