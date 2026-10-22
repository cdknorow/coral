//go:build !windows

package ptymanager

import (
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// A helper that detached and was reparented to PID 1 BEFORE the kill (a daemon the agent started: double-forked, a session of its own, ignoring the hangup and SIGTERM) is found by the tag in its environment, and
// ended by the kill. The helper here is node, not perl: macOS shows another process's environment for an ordinary binary but not for one of Apple's own (/usr/bin/perl, /bin/sleep), so those cannot be found
// there (the limit troubleshooting.md names); Linux shows both.
func TestKillEndsADaemonThatDetachedBeforeTheKill(t *testing.T) {
	canFork(t)
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is needed for a helper that is not one of Apple's own binaries")
	}
	pidFile := filepath.Join(t.TempDir(), "pid")
	script := `process.on("SIGTERM",()=>{});process.on("SIGHUP",()=>{});require("fs").writeFileSync(process.argv[1],String(process.pid));setTimeout(()=>{},600000)`
	b := NewPTYBackend()
	t.Cleanup(func() { b.Close() })
	const name = "claude-00000000-0000-4000-8000-000000006168"
	// the agent starts the daemon in a session of its own (nohup-style, detached) and the agent's own process stays
	cmd := `perl -e 'use POSIX; if (!fork) { POSIX::setsid(); if (!fork) { exec "` + node + `", "-e", q{` + script + `}, "` + pidFile + `" } exit 0 } exit 0'; sleep 600`
	if err := b.Spawn(name, "claude", t.TempDir(), "00000000-0000-4000-8000-000000006168", cmd, 80, 24); err != nil {
		t.Fatal(err)
	}
	pid := pidFrom(t, pidFile)
	time.Sleep(500 * time.Millisecond) // the intermediate processes have exited: the helper's parent is PID 1
	if err := b.KillWithin(name, 600*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	waitGone(t, pid, 5*time.Second)
}
