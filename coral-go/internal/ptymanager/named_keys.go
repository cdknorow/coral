package ptymanager

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// ErrUnknownKey is a key name the PTY backend can't turn into bytes. tmux types a name it does not know as
// text, so a typo ("Enterr") reached an agent as the word; on a PTY nothing is typed for a name that is not a key
// (and not a single character), and the /keys route answers 400.
var ErrUnknownKey = errors.New("unknown key name")

// KeyValidator is implemented by terminals that refuse key names they can't send (the PTY terminal), so a route can
// answer 400 before anything is typed.
type KeyValidator interface {
	ValidateKeys(keys []string) error
}

// namedKeys are the tmux key names Coral and its docs use, as the bytes a terminal sends for them. The cursor keys
// are in the table in their normal-mode form (CSI); namedKeyBytes encodes them for application cursor-key mode
// (DECCKM, SS3) when the program asked for it.
var namedKeys = map[string]string{
	"Enter":    "\r",
	"Escape":   "\x1b",
	"Esc":      "\x1b",
	"Tab":      "\t",
	"BTab":     "\x1b[Z",
	"BSpace":   "\x7f",
	"Space":    " ",
	"Up":       "\x1b[A",
	"Down":     "\x1b[B",
	"Right":    "\x1b[C",
	"Left":     "\x1b[D",
	"Home":     "\x1b[H",
	"End":      "\x1b[F",
	"PageUp":   "\x1b[5~",
	"PgUp":     "\x1b[5~",
	"PPage":    "\x1b[5~",
	"PageDown": "\x1b[6~",
	"PgDn":     "\x1b[6~",
	"NPage":    "\x1b[6~",
	"DC":       "\x1b[3~",
	"Delete":   "\x1b[3~",
	"IC":       "\x1b[2~",
	"Insert":   "\x1b[2~",
	"F1":       "\x1bOP",
	"F2":       "\x1bOQ",
	"F3":       "\x1bOR",
	"F4":       "\x1bOS",
	"F5":       "\x1b[15~",
	"F6":       "\x1b[17~",
	"F7":       "\x1b[18~",
	"F8":       "\x1b[19~",
	"F9":       "\x1b[20~",
	"F10":      "\x1b[21~",
	"F11":      "\x1b[23~",
	"F12":      "\x1b[24~",
}

// namedKeyBytes is what a terminal sends for the key called name. cursorApp is the program's DECCKM state: the arrow
// keys and Home and End are SS3 (ESC O x) rather than CSI (ESC [ x). ok is false for a name that is not a key;
// KeyBytes decides what a name that is not a key is.
func namedKeyBytes(name string, cursorApp bool) (b []byte, ok bool) {
	if seq, found := namedKeys[name]; found {
		if cursorApp && len(seq) == 3 && strings.HasPrefix(seq, "\x1b[") && strings.ContainsRune("ABCDHF", rune(seq[2])) {
			seq = "\x1bO" + seq[2:]
		}
		return []byte(seq), true
	}
	switch {
	case len(name) == 3 && name[0] == 'C' && name[1] == '-':
		// C-a..C-z are the control characters 1..26; C-Space is NUL.
		c := name[2]
		switch {
		case c >= 'a' && c <= 'z':
			return []byte{c - 'a' + 1}, true
		case c >= 'A' && c <= 'Z':
			return []byte{c - 'A' + 1}, true
		}
	case name == "C-Space":
		return []byte{0}, true
	case strings.HasPrefix(name, "M-") && utf8.RuneCountInString(name) == 3:
		// Meta: ESC, then the character.
		return []byte("\x1b" + name[2:]), true
	}
	return nil, false
}

// KeyBytes is the bytes for one entry of a /keys request: a key name (Enter, Escape, Up, C-c...), or text that is
// not a key name and is one character or only digits (tmux types such text: the digits Coral answers a menu with).
// Any other entry is ErrUnknownKey and nothing is sent.
func KeyBytes(entry string, cursorApp bool) ([]byte, error) {
	if b, ok := namedKeyBytes(entry, cursorApp); ok {
		return b, nil
	}
	if entry != "" && (utf8.RuneCountInString(entry) == 1 || isDigits(entry)) {
		return []byte(entry), nil
	}
	return nil, fmt.Errorf("%w: %s", ErrUnknownKey, strconv.Quote(entry))
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// sleepCtx waits d, or until ctx ends (its error).
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
