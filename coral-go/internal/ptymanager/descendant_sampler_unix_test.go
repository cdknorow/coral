//go:build !windows

package ptymanager

import (
	"context"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/process"

	"github.com/subgentic/coral/internal/terminalinput"
	"github.com/subgentic/coral/internal/tmux"
)

// #6183, Codex Test's six-second perl case. The agent starts a helper (perl, then sleep: Apple's own binaries, whose environment macOS does not show another process, so the tag cannot find them) in a session
// of its own, and six seconds later the process that started it exits: the helper is now the child of PID 1 and descent from the shell no longer reaches it. What finds it is the sampler, which saw it as
// a descendant at a tick before that (every sampleEvery). Each case below ends the terminal a different way and expects the helper gone; without the sampler the helper survives on macOS.

// lateDetachCmd is a shell command line: perl starts the helper (a session of its own, its pid in pidFile, then sleep) and exits six seconds later; the shell goes on.
func lateDetachCmd(pidFile string) string {
	return `perl -e 'use POSIX; if (!fork) { POSIX::setsid(); open F, ">` + pidFile + `"; print F $$; close F; exec "sleep", "600" } select(undef, undef, undef, 6); exit 0'; sleep 600`
}

// waitOrphaned waits for the helper's parent to be gone (PID 1 has it): the six seconds are over.
func waitOrphaned(t *testing.T, pid int) {
	t.Helper()
	for end := time.Now().Add(15 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		p, err := process.NewProcess(int32(pid))
		if err != nil {
			t.Fatalf("the helper %d is gone before its parent exited", pid)
		}
		if pp, err := p.Ppid(); err == nil && pp == 1 {
			return
		}
	}
	t.Fatalf("the helper %d was never orphaned", pid)
}

func TestPTYKillEndsAHelperThatDetachedSixSecondsIn(t *testing.T) {
	needPerl(t)
	for _, c := range []struct {
		name string
		end  func(b *PTYBackend, session string)
	}{
		{"Kill", func(b *PTYBackend, session string) {
			if err := b.KillWithin(session, 600*time.Millisecond); err != nil {
				t.Fatal(err)
			}
		}},
		{"quit", func(b *PTYBackend, session string) { b.Close() }},
	} {
		t.Run(c.name, func(t *testing.T) {
			pidFile := filepath.Join(t.TempDir(), "pid")
			b := NewPTYBackend()
			t.Cleanup(func() { b.Close() })
			const id = "00000000-0000-4000-8000-000000006183"
			session := "claude-" + id
			if err := b.Spawn(session, "claude", t.TempDir(), id, lateDetachCmd(pidFile), 80, 24); err != nil {
				t.Fatal(err)
			}
			pid := pidFrom(t, pidFile)
			waitOrphaned(t, pid)
			c.end(b, session)
			waitGone(t, pid, 10*time.Second)
		})
	}
}

func TestTmuxKillAndRestartEndAHelperThatDetachedSixSecondsIn(t *testing.T) {
	needPerl(t)
	for _, c := range []struct {
		name string
		end  func(t *testing.T, term *TmuxSessionTerminal, name string)
	}{
		{"KillSession", func(t *testing.T, term *TmuxSessionTerminal, name string) {
			if err := term.KillSession(context.Background(), name, "", ""); err != nil {
				t.Fatal(err)
			}
		}},
		{"RestartPane", func(t *testing.T, term *TmuxSessionTerminal, name string) {
			panes, err := term.ListSessions(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range panes {
				if p.SessionName == name {
					if err := term.RestartPane(context.Background(), p.Target, ""); err != nil {
						t.Fatal(err)
					}
					return
				}
			}
			t.Fatalf("no pane for %s", name)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := newTestTmuxBackend(t)
			term := NewTmuxSessionTerminal(b.client)
			dir := t.TempDir()
			const name = "claude-00006183-0001-4000-8000-000000000000"
			spawnTmuxSession(t, term, name, dir)
			pidFile := filepath.Join(dir, "pid")
			if err := term.SendInput(context.Background(), terminalinput.Internal, name, lateDetachCmd(pidFile), "", ""); err != nil {
				t.Fatal(err)
			}
			pid := pidFrom(t, pidFile)
			waitOrphaned(t, pid)
			c.end(t, term, name)
			waitGone(t, pid, 12*time.Second) // after the 3 s grace
		})
	}
}

// The same helper, with Coral killed (SIGKILL) instead of a Kill: the guardian ended what it saw of the tree while the helper was still a descendant.
func TestAHelperThatDetachedSixSecondsInIsEndedWhenCoralIsKilled(t *testing.T) {
	child, guardianPID, parent, _ := runGuardParent(t, false, "CORAL_GUARD_TEST_LATE=1")
	waitOrphaned(t, child)
	if err := syscall.Kill(parent.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	parent.Wait()
	waitGone(t, child, 10*time.Second)
	waitGone(t, guardianPID, 10*time.Second)
}

// What is remembered is capped and forgets what has ended: past the cap the first members found stay and the rest are dropped (found by the sweep at the kill), and an ended process is pruned.
func TestTheRememberedSetIsCappedAndForgetsWhatEnded(t *testing.T) {
	var many []member
	for i := 0; i < maxRemembered+10; i++ {
		many = append(many, member{pid: 100000 + i, created: int64(i)})
	}
	merged, dropped := mergeCapped(nil, many, maxRemembered)
	if len(merged) != maxRemembered || !dropped || merged[0] != many[0] {
		t.Fatalf("capped to %d keeping the first found (got %d, dropped %v)", maxRemembered, len(merged), dropped)
	}
	if again, d := mergeCapped(merged, many[:5], maxRemembered); len(again) != maxRemembered || d {
		t.Fatalf("members already remembered are not dropped (%d, %v)", len(again), d)
	}

	c := exec.Command("sleep", "600")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Process.Kill(); c.Wait() })
	p, err := process.NewProcess(int32(c.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	created, err := p.CreateTime()
	if err != nil {
		t.Fatal(err)
	}
	live := member{c.Process.Pid, created}
	var s seen
	s.add([]member{live, {pid: 99999999, created: 1}}) // the second is no process
	s.prune()
	if got := s.all(); len(got) != 1 || got[0] != live {
		t.Fatalf("an ended member is forgotten and a live one stays: %v", got)
	}
	c.Process.Kill()
	c.Wait()
	s.prune()
	if got := s.all(); len(got) != 0 {
		t.Fatalf("a member that has ended is forgotten: %v", got)
	}
}

// The sampler runs only while something is tracked, and ends with the last of it (the leak check would find it otherwise).
func TestTheSamplerEndsWithTheLastTerminal(t *testing.T) {
	d := &descendantSampler{pty: map[member]*seen{}, clients: map[*tmux.Client]struct{}{}, panes: map[member]*seen{}}
	if d.stop != nil {
		t.Fatal("nothing runs before a terminal is tracked")
	}
	untrackA := d.trackPTY(member{pid: 424242, created: 1}, &seen{})
	untrackB := d.trackPTY(member{pid: 424243, created: 1}, &seen{})
	d.mu.Lock()
	running := d.stop != nil
	d.mu.Unlock()
	if !running {
		t.Fatal("a tracked terminal starts the sampler")
	}
	untrackA()
	d.mu.Lock()
	still := d.stop != nil
	d.mu.Unlock()
	if !still {
		t.Fatal("it goes on while one terminal is left")
	}
	untrackB()
	d.mu.Lock()
	stopped := d.stop == nil
	d.mu.Unlock()
	if !stopped {
		t.Fatal("it ends with the last terminal")
	}
}
