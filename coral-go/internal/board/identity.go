package board

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/eskriett/confusables"
)

// Board identities (#3376, SEC-BD-01): subscriber ids, job titles and the like are
// typed into agents' terminals inside notifications ("… from Orchestrator …"). A
// newline in one became an Enter keystroke: in a terminal agent's shell, the rest of
// the name ran as a command. So identities never carry control characters, and
// automated input is always one line (OneLine).

// MaxIdentityRunes bounds an identity's length.
const MaxIdentityRunes = 200

// ValidateIdentity reports why s can't be a board identity: control characters (C0,
// DEL, C1), line or paragraph separators, bidi overrides (a name that displays as
// another), or more than MaxIdentityRunes. Emoji joiners and variation selectors are
// fine.
func ValidateIdentity(s string) error {
	n := 0
	for _, r := range s {
		n++
		if badIdentityRune(r) {
			return fmt.Errorf("must not contain control or formatting characters (found %U)", r)
		}
	}
	if n > MaxIdentityRunes {
		return fmt.Errorf("is too long (%d characters, at most %d)", n, MaxIdentityRunes)
	}
	if s != "" && normalizeName(s) == "" {
		return fmt.Errorf("must contain a visible character")
	}
	return nil
}

// normalizeName is the form names are compared in, so a name can't pass for a
// reserved one by looking the same (#3376): format characters (zero-width, joiners,
// bidi) are dropped, every Unicode space (NBSP, U+2000-200A, U+202F, U+3000…) becomes
// one ASCII space and runs collapse, fullwidth ASCII (Ｃｏｒａｌ) becomes ASCII, then trimmed
// and lower-cased. Letters from other scripts that look like Latin ones (a Cyrillic а or о,
// a Greek Η or ο, a mathematical or roman-numeral form) become the Latin letter, from Unicode's
// confusables table (foldConfusables, #4207), so no spelling in another script passes for
// another name. (The compatibility folding is the fullwidth block, not all of NFKC.)
func normalizeName(s string) string {
	s = foldConfusables(s)
	var b strings.Builder
	space := false
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Cf, r):
			continue
		case unicode.IsSpace(r) || unicode.Is(unicode.Zs, r):
			r = ' '
		case r >= 0xFF01 && r <= 0xFF5E: // fullwidth ASCII
			r -= 0xFEE0
		}
		if r == ' ' {
			if space || b.Len() == 0 {
				continue
			}
			space = true
		} else {
			space = false
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return strings.TrimRight(b.String(), " ")
}

func badIdentityRune(r rune) bool {
	switch {
	case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
		return true
	case r == '\u2028', r == '\u2029':
		return true
	case r == '\u200d': // zero-width joiner: emoji sequences
		return false
	case unicode.Is(unicode.Cf, r): // bidi overrides and isolates, zero-width marks
		return true
	}
	return false
}

// ReservedNames are Coral's own senders. The server posts as them directly; a client
// using one could have its message read as Coral's (muted in the chat, #3343, or taken
// as a health report). Compared case-insensitively, ignoring surrounding spaces.
var ReservedNames = []string{"Coral Task Queue", "Coral Health Monitor", HubUnknownSender,
	// Coral, System and Health: names an agent could use to pass its words off as Coral's
	// (#4176 rule 4). CoralSender is Coral's own notice sender.
	CoralSender, "System", "Health"}

// HubUnknownSender is the sender of a Coral Hub chat post that names no Hub user (a hub
// on an older pkg/hubproxy, #3377): reserved, so no member can look like it.
const HubUnknownSender = "Coral Hub"

// HubUserPrefix starts the sender id of a Coral Hub user's chat post (#3377): reserved,
// so no subscriber or role can look like one.
const HubUserPrefix = "hub-user-"

// confusableFold maps characters that look like ASCII ones to those ASCII ones (Unicode's
// confusables.txt, Unicode 17, package github.com/eskriett/confusables, MIT). It leaves ASCII
// alone and drops combining marks (Zoë and Zoe are one name).
var confusableFold = confusables.NewSafe()

// foldConfusables is s with look-alike letters from other scripts as ASCII; a pure ASCII
// name, almost every name, is returned as it is, without the table.
func foldConfusables(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return confusableFold.ToASCII(s)
		}
	}
	return s
}

// asciiLookalikes folds the ASCII characters that read alike in common typefaces: a capital I,
// a digit 1 and a bar for a lower-case l, and a zero for an O. IsReservedName compares in this
// form too (CoraI, Syst3m... for the reserved few); identity checks do not, so QA1 and QAl stay
// two names.
func asciiLookalikes(s string) string {
	s = foldConfusables(s)
	return strings.Map(func(r rune) rune {
		switch r {
		case 'I', '1', '|':
			return 'l'
		case '0', 'O':
			return 'o'
		}
		return r
	}, s)
}

// OperatorName is the Operator's identity on a board: the dashboard's own posts and the
// command line signed as the Operator use it. An agent may not (IsOperatorName, #4207).
const OperatorName = "Operator"

// IsOperatorName reports whether s is the Operator's name in any spelling that
// normalizeName reads as the same: case, spacing, invisible characters, fullwidth letters
// and look-alike letters from other scripts.
func IsOperatorName(s string) bool { return SameIdentity(s, OperatorName) }

// IsReservedName reports whether s is one of ReservedNames or starts with HubUserPrefix,
// compared in normalizeName's form (case, spacing and invisible characters don't help).
func IsReservedName(s string) bool {
	n := normalizeName(s)
	k := normalizeName(asciiLookalikes(s))
	for _, r := range ReservedNames {
		if n == normalizeName(r) || k == normalizeName(asciiLookalikes(r)) {
			return true
		}
	}
	return strings.HasPrefix(n, HubUserPrefix)
}

// SameIdentity reports whether two names are the same identity in normalizeName's form
// (case, spacing, invisible characters and fullwidth letters don't make a different one).
func SameIdentity(a, b string) bool {
	na := normalizeName(a)
	return na != "" && na == normalizeName(b)
}

// OneLine makes text safe to type into a terminal as one line of automated input:
// every control character (newlines and carriage returns included, which a terminal
// takes as Enter) and line separator becomes a space, and runs of spaces collapse.
func OneLine(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || r == '\u2028' || r == '\u2029' {
			r = ' '
		}
		if r == ' ' {
			if space {
				continue
			}
			space = true
		} else {
			space = false
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}
