package clierr

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// withheldErr stands in for hooks.ForeignListenerError and PlainHTTPTokenError (#99): the agent tools' own refusal to send the token.
type withheldErr struct{}

func (withheldErr) Error() string {
	return "Refused: the agent's token was not sent to port 8603 on this computer. The program on that port is stub.exe (PID 7), not Coral."
}
func (withheldErr) TokenWithheld() bool { return true }

// oldWithheldErr is a refusal whose text doesn't start "Refused:".
type oldWithheldErr struct{}

func (oldWithheldErr) Error() string       { return "not sending the token" }
func (oldWithheldErr) TokenWithheld() bool { return true }

// Security's #260 pass: a withheld token is "refused" (5) in both CLIs, never "cannot reach" (3), wrapped as the HTTP client wraps
// it; the message is the refusal's own whole sentences, with no "cannot reach" and no colon chain in front (#279).
func TestAWithheldTokenIsRefusedNotUnreachable(t *testing.T) {
	err := fmt.Errorf("Get \"http://127.0.0.1:8603/api/board/x\": %w", withheldErr{})
	if k := KindOf(err); k != Refused {
		t.Fatalf("KindOf = %v, want Refused", k)
	}
	ce := CannotReach("http://127.0.0.1:8603", err, 10*time.Second)
	var e *Error
	if !errors.As(ce, &e) || e.Kind != Refused || e.Kind.Code() != ExitRefused {
		t.Fatalf("CannotReach kind = %+v, want Refused (exit %d)", e, ExitRefused)
	}
	msg := ce.Error()
	if msg != (withheldErr{}).Error() {
		t.Fatalf("message = %q", msg)
	}
	if Kind(last.Load()) != Refused {
		t.Fatalf("the noted kind is %v, want Refused (Exit1 exits with it)", Kind(last.Load()))
	}
	if msg := CannotReach("http://127.0.0.1:8603", oldWithheldErr{}, time.Second).Error(); msg != "Refused: nothing was sent to http://127.0.0.1:8603. not sending the token" {
		t.Fatalf("a refusal without its own start gets one: %q", msg)
	}
	if k := KindOf(errors.New("connection reset")); k == Refused {
		t.Fatal("an unmarked error is not refused")
	}
}
