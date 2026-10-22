package clierr

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// withheldErr stands in for hooks.ForeignListenerError and PlainHTTPTokenError (#99): the agent tools' own refusal to send the token.
type withheldErr struct{}

func (withheldErr) Error() string {
	return "coral: not sending this agent's session token to port 8603 on this computer: the process listening there (PID 7) is stub.exe"
}
func (withheldErr) TokenWithheld() bool { return true }

// Security's #260 pass: a withheld token is "refused" (5) in both CLIs, never "cannot reach" (3), wrapped as the HTTP client wraps
// it; the message says nothing was sent and keeps the reason.
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
	if strings.HasPrefix(msg, "cannot reach") || !strings.Contains(msg, "refused before anything was sent") || !strings.Contains(msg, "stub.exe") {
		t.Fatalf("message = %q", msg)
	}
	if Kind(last.Load()) != Refused {
		t.Fatalf("the noted kind is %v, want Refused (Exit1 exits with it)", Kind(last.Load()))
	}
	if k := KindOf(errors.New("connection reset")); k == Refused {
		t.Fatal("an unmarked error is not refused")
	}
}
