package main

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/subgentic/coral/internal/clierr"
)

type withheldErr struct{}

func (withheldErr) Error() string {
	return "coral: not sending this agent's session token to port 8603"
}
func (withheldErr) TokenWithheld() bool { return true }

type failingTransport struct{ err error }

func (f failingTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, f.err }

// Security's #260 pass: a request whose token was withheld (#99) notes Refused, so the command that then fails exits 5, whichever
// request met it (PID resolution swallows its error); any other failure leaves the noted kind as it was.
func TestAWithheldTokenIsNotedAsRefused(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:8603/api/sessions/resolve", nil)
	clierr.Note(clierr.Other)
	if _, err := (withheldNoter{base: failingTransport{errors.New("connection reset")}}).RoundTrip(req); err == nil || clierr.Noted() != clierr.Other {
		t.Fatalf("an ordinary failure: err=%v noted=%v", err, clierr.Noted())
	}
	_, err := (withheldNoter{base: failingTransport{fmt.Errorf("wrapped: %w", withheldErr{})}}).RoundTrip(req)
	if err == nil || clierr.Noted() != clierr.Refused || clierr.Noted().Code() != clierr.ExitRefused {
		t.Fatalf("a withheld token: err=%v noted=%v", err, clierr.Noted())
	}
	clierr.Note(clierr.Other)
}

// #279 (must-fix): a withheld token at PID resolution prints the refusal and exits 5; it is never reported as "Not subscribed
// to any board. Run: coral-board join …", which would send the agent the wrong way. Any other failure is not a refusal.
func TestAWithheldResolutionPrintsTheRefusal(t *testing.T) {
	clierr.Note(clierr.Other)
	defer clierr.Note(clierr.Other)
	if _, ok := withheldMessage(errors.New("connection reset")); ok {
		t.Fatal("an ordinary failure is not a withheld token")
	}
	if _, ok := withheldMessage(nil); ok {
		t.Fatal("no error is not a withheld token")
	}
	msg, ok := withheldMessage(fmt.Errorf("Get \"http://127.0.0.1:8603/api/sessions/resolve\": %w", withheldErr{}))
	if !ok || clierr.Noted().Code() != clierr.ExitRefused {
		t.Fatalf("a withheld token: ok=%v noted=%v", ok, clierr.Noted())
	}
	if !strings.HasPrefix(msg, "Refused: ") || !strings.Contains(msg, "port 8603") || strings.Contains(msg, "Not subscribed") || strings.Contains(msg, "join") {
		t.Fatalf("message = %q", msg)
	}
}
