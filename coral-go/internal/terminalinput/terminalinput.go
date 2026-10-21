// Package terminalinput is the one place that decides what a write to an agent's terminal is, before either backend (PTY or tmux)
// puts it on the wire: whose input it is (Origin), how the text is made safe (the Prepare functions), and how it is recorded in the
// input debug log (Record). The backends keep only how bytes reach the terminal (#5692, #5706).
//
// An Origin is an explicit argument of every exported sender and every choke point, never a value carried in a context: a caller
// has to choose Person, Coral, Internal or Unclassified, and a send that forgets to is a compile error. (Both bugs of 148, a
// person's paste logged in full, were a context dropped on the way down: a context that is missing still compiles and silently
// means "not a person".) A context only cancels, and carries the hints the transport reads (the pane's bracketed paste mode, the
// check before the Enter).
//
// The zero value, Unknown, is the origin of a value nobody set. It fails closed in both directions: its text is made safe like
// Coral's (control characters, the paste terminator and line breaks never reach the terminal as keys) and it is recorded like a
// person's: by its length only (#5715). No caller passes Unknown today; a value nobody set cannot leak a text into the log.
//
// Unclassified is what the callers that were never marked as anyone's still are: their text is neither changed nor hidden, as
// before. The census test (census_test.go) lists every exported sender's callers and the origin each passes, so marking them
// (the dashboard's answer-prompt, /send and /keys are a person's) is a one-line change per row.
package terminalinput

import (
	"errors"
	"fmt"
	"strings"

	"github.com/subgentic/coral/internal/inputlog"
)

// Origin says whose input a write to a terminal is.
type Origin int

const (
	// Unknown is the zero value: nobody said. Made safe as Coral's text and recorded as a person's (by length), see the package comment.
	Unknown Origin = iota
	// Person is a person's own input from the dashboard (typed in its terminal, or sent from its chat box): never changed, and
	// recorded by its length only, unless it is only control input (Enter, arrows, Ctrl-C), which is recorded as it is.
	Person
	// Coral is Coral's own text for an agent (a board message, a task notice, a nudge: the automated-input gate's): made safe,
	// and recorded in full.
	Coral
	// Internal is a control byte or key Coral sends itself (Enter, the Ctrl-C of a stop, a named key): sent as it is, recorded as it is.
	Internal
	// Unclassified is a sender nobody has classified yet: text neither changed nor hidden, as before this package. See the census (what is left: the option digits of the dashboard's answer cards, which are choices, not text).
	Unclassified
)

func (o Origin) String() string {
	switch o {
	case Person:
		return "person"
	case Coral:
		return "coral"
	case Internal:
		return "internal"
	case Unclassified:
		return "unclassified"
	}
	return "unknown"
}

// SafeText says whether the text of o is made safe (flattened, control characters dropped) before it is typed.
func (o Origin) SafeText() bool { return o == Coral || o == Unknown }

// Kind is how a write was sent (inputlog.Kind).
type Kind = inputlog.Kind

const (
	Text  = inputlog.Text  // literal text (tmux send-keys -l)
	Keys  = inputlog.Keys  // tmux key names (Enter, C-c, Escape, ...)
	Paste = inputlog.Paste // a tmux buffer pasted into the pane
	Raw   = inputlog.Raw   // bytes written to a PTY
)

// ErrNothingToSend: a text of Coral's was only control characters and line breaks, so nothing is left to send. The send is
// refused: typing nothing and pressing Enter would submit whatever else is in the agent's input (a person's draft).
var ErrNothingToSend = errors.New("the text has nothing left to send once control characters and line breaks are removed")

// PrepareTyped makes text safe to type as keystrokes for o: Coral's (and an Unknown) text has its line breaks turned into spaces
// and its control characters dropped, and is refused (ErrNothingToSend) when nothing is left of a non-empty text. A person's
// text, and an internal or unclassified one, is returned unchanged (a person's text is never changed: tested).
func PrepareTyped(o Origin, text string) (string, error) {
	if !o.SafeText() {
		return text, nil
	}
	out := AutomatedText(text)
	if text != "" && strings.TrimSpace(out) == "" {
		return "", ErrNothingToSend
	}
	return out, nil
}

// PreparePaste is the text of a paste for o: CRLF and CR are one LF, and ESC (an ESC [ 201 ~ would end the paste early) and every
// other control character but LF and Tab are dropped, whoever sent it. Coral's (and an Unknown) text of which nothing is left is
// refused (ErrNothingToSend): no empty paste and Enter over whatever is in the input.
func PreparePaste(o Origin, text string) (string, error) {
	body := SanitizePaste(text)
	if o.SafeText() && text != "" && strings.TrimSpace(body) == "" {
		return "", ErrNothingToSend
	}
	return body, nil
}

// SanitizePaste prepares text for a bracketed paste: it keeps the text intact (including newlines and tabs) but removes ESC and
// other control characters that could end the paste early (ESC [ 2 0 1 ~) or act as input, and normalizes CRLF/CR to LF.
func SanitizePaste(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case isControl(r):
			// dropped
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// SanitizeTyped makes text safe to type as keystrokes: ESC (an ESC[201~ would end a paste, an ESC sequence is a key), every other
// C0 control (Ctrl-C, Ctrl-D, Tab, CR, LF...), DEL and the C1 range U+0080-009F are dropped. What remains is printable text. (A
// byte that is not valid UTF-8, such as a lone 0x9b, which some terminals take as CSI, comes out as U+FFFD, never as itself.)
func SanitizeTyped(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		if isControl(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// AutomatedText is text from Coral for an agent, to be typed: line breaks (CRLF, CR, LF) become one space each run, then
// SanitizeTyped. A person's own text is never changed by it (PrepareTyped does not call it for one).
func AutomatedText(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	space := false
	for _, r := range text {
		if r == '\r' || r == '\n' {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	return SanitizeTyped(b.String())
}

func isControl(r rune) bool { return r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) }

// Record logs one write to target when the input debug log is on: the only caller of inputlog.Write. A person's text (and an unknown
// origin's) is logged by its length only, unless it is only control input; everything else as it is. Coral's own annotations are inputlog.Note.
func Record(o Origin, target string, kind Kind, data string) {
	if (o == Person || o == Unknown) && kind != Keys && hasPrintable(data) {
		kind, data = inputlog.Typed, fmt.Sprintf("[typed %d bytes]", len(data))
	}
	inputlog.Write(target, kind, data)
}

// hasPrintable reports whether s holds anything but control bytes and escape sequences: text a person typed, as opposed to Enter,
// Tab, arrows or Ctrl-<key>.
func hasPrintable(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == 0x1b { // an escape sequence: ESC [ A, ESC O P, ESC [ 1 ; 5 C ...
			i++
			for i < len(s) && (s[i] == '[' || s[i] == 'O' || s[i] >= '0' && s[i] <= '9' || s[i] == ';') {
				i++
			}
			continue // the sequence's final byte
		}
		if c >= 0x20 && c != 0x7f {
			return true
		}
	}
	return false
}
