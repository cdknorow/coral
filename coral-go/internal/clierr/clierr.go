// Package clierr is what coral, coral-board and coral-agent share about a failed request: the exit-code table, the words for "cannot reach
// Coral", and the CORAL_TIMEOUT override of their request timeouts (#228).
//
// Exit codes: 0 ok, 1 any other failure, 2 usage (an option or argument the command does not take; nothing was sent), 3 cannot reach the
// server (refused, host not found), 4 the server did not answer in time, 5 refused for want of authority (HTTP 401 or 403), 6 not found (404).
package clierr

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Kind is why a command failed.
type Kind int

const (
	Other       Kind = iota // exit 1
	Usage                   // exit 2
	Unreachable             // exit 3
	TimedOut                // exit 4
	Refused                 // exit 5: HTTP 401 or 403
	NotFound                // exit 6: HTTP 404
)

// Exit codes, by kind.
const (
	ExitOther       = 1
	ExitUsage       = 2
	ExitUnreachable = 3
	ExitTimedOut    = 4
	ExitRefused     = 5
	ExitNotFound    = 6
)

// Code is the exit code of kind.
func (k Kind) Code() int {
	switch k {
	case Usage:
		return ExitUsage
	case Unreachable:
		return ExitUnreachable
	case TimedOut:
		return ExitTimedOut
	case Refused:
		return ExitRefused
	case NotFound:
		return ExitNotFound
	}
	return ExitOther
}

// Table is the exit-code table as help text.
const Table = `Exit codes: 0 ok; 1 any other failure; 2 usage (nothing was sent); 3 cannot reach the
server; 4 the server did not answer in time; 5 refused (HTTP 401 or 403); 6 not found (404).
CORAL_TIMEOUT (a duration such as 30s, 1s to 10m) sets how long a request waits.`

// last is the kind of the most recent failed request this process saw, for Exit1.
var last atomic.Int32

// Note records the kind of a failed request, so that a command that prints its own message and exits "with an error" (Exit1) exits with the
// code of what actually went wrong. A later Note replaces an earlier one.
func Note(k Kind) { last.Store(int32(k)) }

// NoteStatus records the kind of an HTTP status (anything but 401, 403 and 404 is Other; a 2xx clears it).
func NoteStatus(status int) { Note(StatusKind(status)) }

// StatusKind is the kind of an HTTP status.
func StatusKind(status int) Kind {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return Refused
	case http.StatusNotFound:
		return NotFound
	}
	return Other
}

// Exit1 exits for a failure the caller has already reported: with the code of the last failed request noted, else 1. Exit(1) calls in the
// CLIs are this, so a script can tell "Coral is not running" from "refused" from "no such task".
func Exit1() { os.Exit(Kind(last.Load()).Code()) }

// Error is an error with a Kind.
type Error struct {
	Kind Kind
	Msg  string
	Err  error
}

func (e *Error) Error() string { return e.Msg }
func (e *Error) Unwrap() error { return e.Err }

// KindOf is the kind of err: its own if it is an *Error, else classified from the network error it wraps.
func KindOf(err error) Kind {
	var ce *Error
	if errors.As(err, &ce) {
		return ce.Kind
	}
	k, _ := classify(err)
	return k
}

// classify says whether a transport error is a timeout, a connection refused, a host that cannot be found, or something else (Other).
func classify(err error) (Kind, string) {
	var dns *net.DNSError
	var ne net.Error
	var errno syscall.Errno
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return TimedOut, "timeout"
	case errors.As(err, &dns):
		return Unreachable, "dns"
	case errors.As(err, &errno) && connRefused(errno):
		return Unreachable, "refused"
	case errors.As(err, &errno) && netUnreachable(errno):
		return Unreachable, "unreachable"
	}
	return Other, ""
}

// CannotReach turns a failed request into the error a CLI prints: it always begins "cannot reach Coral server at <url>" and then says which
// of refused, no answer in time and host not found it was, with what to try, instead of Go's raw message. url must already be free of
// credentials; waited is the timeout the request had.
func CannotReach(url string, err error, waited time.Duration) error {
	kind, why := classify(err)
	var msg string
	switch why {
	case "refused":
		msg = fmt.Sprintf("cannot reach Coral server at %s: the connection was refused. Is Coral running? Check the tray icon, and that the port in CORAL_URL (or CORAL_PORT) is the one Coral listens on.", url)
	case "timeout":
		msg = fmt.Sprintf("cannot reach Coral server at %s: it did not answer within %s. It may be busy: try again, or set CORAL_TIMEOUT (for example CORAL_TIMEOUT=30s) to wait longer.", url, waited.Round(time.Millisecond))
	case "dns":
		msg = fmt.Sprintf("cannot reach Coral server at %s: the host name cannot be found. Check CORAL_URL.", url)
	case "unreachable":
		msg = fmt.Sprintf("cannot reach Coral server at %s: the network cannot reach that address. Check CORAL_URL and your network.", url)
	default:
		msg = fmt.Sprintf("cannot reach Coral server at %s: %s", url, strings.TrimSpace(err.Error()))
		kind = Unreachable // it never got an answer
	}
	Note(kind)
	return &Error{Kind: kind, Msg: msg, Err: err}
}

// MaxTimeout and MinTimeout bound CORAL_TIMEOUT.
const (
	MinTimeout = time.Second
	MaxTimeout = 10 * time.Minute
)

// Timeout is how long a request waits: CORAL_TIMEOUT (a Go duration such as 30s or 2m) when it is set and valid, kept between MinTimeout and
// MaxTimeout, else def. A value that is not a duration is ignored (a warning is printed once).
func Timeout(def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv("CORAL_TIMEOUT"))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		warnOnce.Do(func() {
			fmt.Fprintf(os.Stderr, "Note: CORAL_TIMEOUT=%q is not a duration such as 30s or 2m; using the default.\n", v)
		})
		return def
	}
	return min(max(d, MinTimeout), MaxTimeout)
}

var warnOnce sync.Once
