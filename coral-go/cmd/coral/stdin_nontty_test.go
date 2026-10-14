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
	cmd.Env = append(os.Environ(), "CORAL_DATA_DIR="+dataDir, "HOME="+dataDir, "CORAL_NO_DEMO_SEED=1")
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

	resp, err := http.Get(base + "/api/settings")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var settings map[string]any
	if err := json.Unmarshal(body, &settings); err != nil {
		t.Fatalf("settings: %v: %s", err, body)
	}
	if v, saved := settings["remote_access_enabled"]; saved {
		t.Fatalf("remote_access_enabled must not be saved when nothing was asked, got %v", v)
	}
}
