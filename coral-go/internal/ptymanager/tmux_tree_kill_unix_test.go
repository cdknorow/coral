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
func startHelpers(t *testing.T, term *TmuxSessionTerminal, name, dir string) (inSession, inGroup int) {
	t.Helper()
	a, b := filepath.Join(dir, name+".a"), filepath.Join(dir, name+".b")
	cmd := `perl -e 'use POSIX; if (!fork) { POSIX::setsid(); open F, ">` + a + `"; print F $$; close F; exec "sleep", "600" } if (!fork) { setpgrp(0, 0); open F, ">` + b + `"; print F $$; close F; exec "sleep", "600" } wait; wait'`
	if err := term.SendInput(context.Background(), terminalinput.Internal, name, cmd, "", ""); err != nil {
		t.Fatal(err)
	}
	return pidFrom(t, a), pidFrom(t, b)
}

func TestTmuxKillAndRestartEndWhatTheAgentStarted(t *testing.T) {
	needPerl(t)
	for i, c := range []struct {
		name string
		end  func(term *TmuxSessionTerminal, name string) error
	}{
		{"KillSession", func(term *TmuxSessionTerminal, name string) error {
			return term.KillSession(context.Background(), name, "", "")
		}},
		{"KillSessionOnly", func(term *TmuxSessionTerminal, name string) error {
			return term.KillSessionOnly(context.Background(), name, "", "")
		}},
		{"RestartPane", func(term *TmuxSessionTerminal, name string) error {
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
	} {
		t.Run(c.name, func(t *testing.T) {
			term := newTmuxTestTerminal(t)
			dir := t.TempDir()
			gone, kept := fmt.Sprintf("claude-00006141-000%d-4000-8000-000000000000", i), fmt.Sprintf("claude-00006141-001%d-4000-8000-000000000000", i)
			spawnTmuxSession(t, term, gone, dir)
			spawnTmuxSession(t, term, kept, dir)
			a, b := startHelpers(t, term, gone, dir)
			otherA, otherB := startHelpers(t, term, kept, dir)
			if g, _ := syscall.Getpgid(b); g != b {
				t.Fatalf("the second child should lead a group of its own (pgid %d)", g)
			}
			start := time.Now()
			if err := c.end(term, gone); err != nil {
				t.Fatal(err)
			}
			waitGone(t, a, 8*time.Second)
			waitGone(t, b, 8*time.Second)
			if took := time.Since(start); took > tmuxTreeGrace+4*time.Second {
				t.Fatalf("ending the tree took %v", took)
			}
			for _, pid := range []int{otherA, otherB} {
				if err := syscall.Kill(pid, 0); err != nil {
					t.Fatalf("another session's child %d was ended too: %v", pid, err)
				}
			}
		})
	}
}
