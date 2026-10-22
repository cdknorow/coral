//go:build !windows

package tmux

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// PrivateAgentSessionsChecked tells "no server" (none) from "could not ask" (unknown): the backend choice must never take the second for the first, or an upgraded install would start on the
// built-in terminal and hide the agents still running in tmux (#6064, Security's must).
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pc") // a short path: socket paths have a length limit
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestPrivateAgentSessionsCheckedNoSocketIsNone(t *testing.T) {
	names, known := PrivateAgentSessionsChecked(context.Background(), filepath.Join(shortDir(t), "tmux.sock"))
	if len(names) != 0 || !known {
		t.Fatalf("no socket: %v, known=%v", names, known)
	}
}

func TestPrivateAgentSessionsCheckedAStaleSocketIsNone(t *testing.T) {
	bin, ok := IsAvailable()
	if !ok {
		t.Skip("tmux not installed")
	}
	t.Setenv("CORAL_TMUX_BIN", bin)
	sock := filepath.Join(shortDir(t), "tmux.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	l.Close() // the file stays, nobody listens: "connection refused", a server that is gone
	names, known := PrivateAgentSessionsChecked(context.Background(), sock)
	if len(names) != 0 || !known {
		t.Fatalf("stale socket: %v, known=%v", names, known)
	}
}

func TestPrivateAgentSessionsCheckedAServerThatCannotBeAskedIsUnknown(t *testing.T) {
	dir := shortDir(t)
	sock := filepath.Join(dir, "tmux.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	// a tmux that cannot talk to the server there: a client/server version mismatch, as after an upgrade
	fake := filepath.Join(dir, "tmux")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho 'protocol version mismatch (client 8, server 7)' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CORAL_TMUX_BIN", fake)
	names, known := PrivateAgentSessionsChecked(context.Background(), sock)
	if len(names) != 0 || known {
		t.Fatalf("a server that could not be asked was taken for none: %v, known=%v", names, known)
	}
}

func TestPrivateAgentSessionsCheckedListsTheAgentsOfARealServer(t *testing.T) {
	bin, ok := IsAvailable()
	if !ok {
		t.Skip("tmux not installed")
	}
	dir := shortDir(t)
	sock := filepath.Join(dir, "tmux.sock")
	cmd := exec.Command(bin, "-S", sock, "new-session", "-d", "-s", "claude-0b2f6c1e-3d4a-4b5c-8d6e-7f8091a2b3c4", "sleep 600")
	cmd.Env = append(os.Environ(), "TMUX=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	t.Cleanup(func() { exec.Command(bin, "-S", sock, "kill-server").Run() })
	t.Setenv("CORAL_TMUX_BIN", bin)
	names, known := PrivateAgentSessionsChecked(context.Background(), sock)
	if len(names) != 1 || !known {
		t.Fatalf("a server with one agent: %v, known=%v", names, known)
	}
}
