package ptymanager

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/subgentic/coral/internal/terminalinput"
)

// #3211: with a session id, the PTY terminal acts only on that session. A sibling in the
// same folder, or whose name or key the caller passes, is never written to, read or killed.
func TestPTYMissingSessionNeverReachesASibling(t *testing.T) {
	withFastSubmit(t)
	const sibSID = "99999999-8888-4777-8666-555555555555"
	const goneSID = "11111111-2222-4333-8444-555555555555"
	sibKey := "claude-" + sibSID
	b, s, proc := newFakeBackend(t, sibKey)
	s.agentType, s.sessionID, s.workingDir = "claude", sibSID, "/work/team"
	p := NewPTYSessionTerminal(b)
	ctx := context.Background()

	gone := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, ErrTerminalGone) {
			t.Errorf("%s: err = %v, want ErrTerminalGone", what, err)
		}
	}
	// The folder, a prefix, a short substring, the sibling's own key, and the gone agent's key.
	for _, name := range []string{"team", "claude", "te", sibKey, "claude-" + goneSID} {
		for _, sid := range []string{goneSID, ""} {
			if sid == "" && name != "claude-"+goneSID {
				continue // bare names without a sid are the legacy path
			}
			if pane, _ := p.FindSession(ctx, name, "claude", sid); pane != nil {
				t.Errorf("FindSession(%q, %q) = %+v", name, sid, pane)
			}
			gone("SendInput "+name, p.SendInput(ctx, terminalinput.Unclassified, name, "echo hi", "claude", sid))
			gone("SendPrompt "+name, p.SendPrompt(ctx, terminalinput.Unclassified, name, "hi", "claude", sid))
			gone("SendRawInput "+name, p.SendRawInput(ctx, terminalinput.Unclassified, name, []string{"1"}, "claude", sid))
			gone("ResizeSession "+name, p.ResizeSession(ctx, name, 33, "claude", sid))
			gone("KillSessionOnly "+name, p.KillSessionOnly(ctx, name, "claude", sid))
			gone("KillSession "+name, p.KillSession(ctx, name, "claude", sid))
			if text, err := p.CaptureOutput(ctx, name, 50, "claude", sid); err != nil || text != "" {
				t.Errorf("CaptureOutput(%q, %q) = %q, %v", name, sid, text, err)
			}
		}
	}
	if w := proc.written(); len(w) != 0 {
		t.Fatalf("the sibling's PTY was written to: %q", w)
	}
	if !b.IsRunning(sibKey) {
		t.Fatal("the sibling was killed")
	}

	// Its own session id still reaches it, whatever name is passed; and the legacy name match stays.
	if err := p.SendInput(ctx, terminalinput.Unclassified, "team", "own", "claude", sibSID); err != nil {
		t.Fatalf("own sid: %v", err)
	}
	if pane, _ := p.FindSession(ctx, sibKey, "", ""); pane == nil {
		t.Fatal("an exact session name without a sid no longer finds its session")
	}
	// Kill, resize and capture resolve the same way (the fake process cannot be killed here).
	if key, err := p.keyFor("team", sibSID); err != nil || key != sibKey {
		t.Fatalf("own key = %q, %v", key, err)
	}
}

// #3214: a PTY lookup by bare name is deprecated and logged; an exact key is not.
func TestPTYBareNameLookupIsLogged(t *testing.T) {
	b, _, _ := newFakeBackend(t, "claude-sid")
	p := NewPTYSessionTerminal(b)
	var buf logBuf
	old, oldFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(old); log.SetFlags(oldFlags) })

	p.resolveSessionKey("claude-sid")                         // the exact key
	bare := fmt.Sprintf("pty-bare-%d", time.Now().UnixNano()) // logged once per name per process
	p.resolveSessionKey(bare)
	p.resolveSessionKey(bare)
	out := buf.String()
	assert.Equal(t, 1, strings.Count(out, "deprecated"), out)
	assert.Contains(t, out, `"`+bare+`" looked up by bare name`)
}
