package agentsignin

import (
	"context"
	"testing"
	"time"

	"github.com/subgentic/coral/internal/claudestub"
)

// A verdict is never HELD BACK (#5924): asking the CLI that is signed out, or in the middle of a login, takes no longer than asking one that is signed in. A hold (a sleep, a wait on a
// lock or on a login put into the check, or after it before the verdict returns) is told from a busy machine by comparing against a signed-in control timed the same way at the same
// moment, fastest of five each (claudestub.NoSlower), not by an absolute number of seconds that a loaded machine overruns.
func TestAVerdictIsNotHeldBackInAnyState(t *testing.T) {
	claudestub.Isolate(t)
	control := claudestub.New(t, claudestub.SignedIn)
	ask := func(s *claudestub.Stub) func() {
		return func() {
			Forget("claude", s.Path())
			_ = RequireFresh(context.Background(), "claude", s.Path())
		}
	}
	for _, st := range []claudestub.State{claudestub.SignedOut, claudestub.LoginInProgress} {
		stub := claudestub.New(t, st)
		claudestub.NoSlower(t, "the sign-in verdict in "+string(st), ask(control), ask(stub), time.Second)
		t.Cleanup(func() { Forget("claude", stub.Path()) })
	}
	t.Cleanup(func() { Forget("claude", control.Path()) })
}
