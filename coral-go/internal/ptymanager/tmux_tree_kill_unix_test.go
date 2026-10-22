//go:build !windows

package ptymanager

import (
	"context"
	"fmt"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/subgentic/coral/internal/terminalinput"
)

// #6141: tmux hangs up only the pane's own process group when a session is killed or a pane respawned, so an agent in a group of its own and the helpers it started in sessions of their own were left running.
// A real tmux on a private socket and real children (perl): one in a session of its own, one in a process group of its own, both children of a process the pane's shell runs.

// startHelpers types into the pane's shell a command that starts the two children and waits, and returns their pids.
func startHelpers(t *testing.T, term *TmuxSessionTerminal, name, dir string, ignoreStop bool) (inSession, inGroup int) {
	t.Helper()
	a, b := filepath.Join(dir, name+".a"), filepath.Join(dir, name+".b")
	ignore := ""
	if ignoreStop { // children that ignore the hangup and SIGTERM: only the grace and the SIGKILL after it end them
		ignore = `$SIG{HUP} = "IGNORE"; $SIG{TERM} = "IGNORE"; `
	}
	cmd := `perl -e 'use POSIX; ` + ignore + `if (!fork) { POSIX::setsid(); open F, ">` + a + `"; print F $$; close F; exec "sleep", "600" } if (!fork) { setpgrp(0, 0); open F, ">` + b + `"; print F $$; close F; exec "sleep", "600" } wait; wait'`
	if err := term.SendInput(context.Background(), terminalinput.Internal, name, cmd, "", ""); err != nil {
		t.Fatal(err)
	}
	return pidFrom(t, a), pidFrom(t, b)
}

func TestTmuxKillAndRestartEndWhatTheAgentStarted(t *testing.T) {
	needPerl(t)
	for i, c := range []struct {
		name       string
		ignoreStop bool
		end        func(term *TmuxSessionTerminal, name string) error
	}{
		{"KillSession", false, func(term *TmuxSessionTerminal, name string) error {
			return term.KillSession(context.Background(), name, "", "")
		}},
		{"KillSessionOnly", false, func(term *TmuxSessionTerminal, name string) error {
			return term.KillSessionOnly(context.Background(), name, "", "")
		}},
		{"RestartPane", false, func(term *TmuxSessionTerminal, name string) error {
			panes, err := term.ListSessions(context.Background())
			if err != nil {
				return err
			}
			for _, p := range panes {
				if p.SessionName == name {
					return term.RestartPane(context.Background(), p.Target, "")
				}
			}
			t.Fatalf("no pane for %s", name)
			return nil
		}},
		{"KillSessionOfChildrenThatIgnoreTheStop", true, func(term *TmuxSessionTerminal, name string) error {
			return term.KillSession(context.Background(), name, "", "")
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			term := newTmuxTestTerminal(t)
			dir := t.TempDir()
			gone, kept := fmt.Sprintf("claude-00006141-000%d-4000-8000-000000000000", i), fmt.Sprintf("claude-00006141-001%d-4000-8000-000000000000", i)
			spawnTmuxSession(t, term, gone, dir)
			spawnTmuxSession(t, term, kept, dir)
			a, b := startHelpers(t, term, gone, dir, c.ignoreStop)
			otherA, otherB := startHelpers(t, term, kept, dir, false)
			if g, _ := syscall.Getpgid(b); g != b {
				t.Fatalf("the second child should lead a group of its own (pgid %d)", g)
			}
			start := time.Now()
			if err := c.end(term, gone); err != nil {
				t.Fatal(err)
			}
			if took := time.Since(start); took > 2*time.Second {
				t.Fatalf("the request waited %v for the tree to end: the person's Kill, sleep or Restart is answered at once, and the tree ends in the background", took)
			}
			waitGone(t, a, 12*time.Second) // after the 3 s grace, by SIGKILL when they ignore the stop
			waitGone(t, b, 12*time.Second)
			for _, pid := range []int{otherA, otherB} {
				if err := syscall.Kill(pid, 0); err != nil {
					t.Fatalf("another session's child %d was ended too: %v", pid, err)
				}
			}
		})
	}
}

// A Coral that quits while a tmux tree kill is waiting out its grace finishes it: the children here ignore the hangup and SIGTERM, so only the SIGKILL after the grace would end them, and the process
// that was going to send it is the one quitting.
func TestQuittingFinishesAPendingTmuxTreeKill(t *testing.T) {
	needPerl(t)
	b := newTestTmuxBackend(t)
	term := NewTmuxSessionTerminal(b.client)
	dir := t.TempDir()
	const gone = "claude-00006141-0020-4000-8000-000000000000"
	spawnTmuxSession(t, term, gone, dir)
	x, y := startHelpers(t, term, gone, dir, true)
	if err := term.KillSession(context.Background(), gone, "", ""); err != nil {
		t.Fatal(err)
	}
	for _, pid := range []int{x, y} {
		if err := syscall.Kill(pid, 0); err != nil {
			t.Fatalf("the children ignore the stop, so they are still there during the grace: %v", err)
		}
	}
	start := time.Now()
	b.Close() // quitting
	waitGone(t, x, 2*time.Second)
	waitGone(t, y, 2*time.Second)
	if took := time.Since(start); took > tmuxTreeGrace-time.Second {
		t.Fatalf("they were ended after %v, which is the grace running out, not the quit finishing the kill", took)
	}
}
