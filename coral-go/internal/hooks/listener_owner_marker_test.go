package hooks

import (
	"errors"
	"fmt"
	"testing"
)

// Both refusals to send the token say so (TokenWithheld), through the wrapping an HTTP client adds, so the CLIs can exit
// "refused" (5) for them instead of "cannot reach" (3) (Security's #260 pass).
func TestTheTokenRefusalsAreMarked(t *testing.T) {
	for _, err := range []error{&ForeignListenerError{Port: 8603, PID: 1, Why: "is stub.exe"}, &PlainHTTPTokenError{Host: "example.com"}} {
		wrapped := fmt.Errorf("Get \"http://127.0.0.1:8603/api\": %w", err)
		var w interface{ TokenWithheld() bool }
		if !errors.As(wrapped, &w) || !w.TokenWithheld() {
			t.Fatalf("%T is not marked as a withheld token", err)
		}
	}
}
