// Package settings is the one place Coral's user settings are declared and read (#5147).
//
// Every key is declared once, with its type, its default, its limits, who may write it and when a change takes effect. Code that needs a
// setting asks for it with a typed reader (Bool, Int, Enum, Text, Raw, IntState) on the map the store returns, never by indexing that
// map and parsing the string itself: six different hand-written readers disagreed about which words mean "off" (#5129 was one of them),
// and each carried its own copy of the default and the clamp. The census test (census_test.go) fails on a raw read of a settings map
// anywhere else in the program.
//
// The store keeps every value as text. PUT /api/settings stores a JSON boolean as "True" or "False" (Python's str(), kept for the
// database's sake), and the words it accepts for a boolean are 1, true, yes, on and 0, false, no, off in any case: Bool reads them all.
package settings

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/cdknorow/coral/internal/config"
)

// Kind is what a setting holds.
type Kind int

const (
	KindBool   Kind = iota // on or off
	KindInt                // a whole number
	KindEnum               // one of Values
	KindText               // any text
	KindList               // a JSON list kept as text (the dashboard's saved personas, macros, orders)
	KindOpaque             // text Coral writes or reads itself (a timestamp, a JSON summary): the settings table's bookkeeping
)

// Apply says when a changed value takes effect.
type Apply int

const (
	ApplyLive    Apply = iota // read where it is used: the next request or tick sees it
	ApplyRestart              // read once, when Coral starts
	ApplyClient               // the dashboard reads it; the server only keeps it
	ApplyNone                 // Coral's own bookkeeping
)

func (a Apply) String() string {
	switch a {
	case ApplyLive:
		return "live"
	case ApplyRestart:
		return "restart"
	case ApplyClient:
		return "client"
	}
	return "none"
}

// Class says who writes a setting.
type Class int

const (
	ClassGeneric  Class = iota // PUT /api/settings
	ClassOwnRoute              // only its own route; the generic PUT refuses it
	ClassCoral                 // Coral's own bookkeeping; no client writes it
)

// ReadMode says what a reader does with a whole number that is outside Min..Max (a value a person typed into the database, or a limit
// that was later narrowed): the write side always refuses it, or clamps it when WriteClamp is set.
type ReadMode int

const (
	ReadDefault ReadMode = iota // out of range reads as the default
	ReadClamp                   // out of range is brought into range
	ReadAny                     // the range is for writing only; any whole number reads as itself
)

// Spec is one setting. The zero value of every optional field is the common case.
type Spec struct {
	Key     string
	Kind    Kind
	Default string // the value when the setting is unset or unreadable, in stored form ("true", "30", an enum value, text)
	Apply   Apply
	Class   Class
	Doc     string

	// KindInt: the limits. Min and Max are both zero for a number with no limits.
	Min, Max   int64
	WriteClamp bool     // a number outside the limits is brought in when written, not refused
	Read       ReadMode // what a reader does with one outside them
	// ReadMin and ReadMax, when ReadBounded, are the limits a reader applies instead of Min..Max (the write side is stricter or looser than what
	// the reader tolerates: the knowledge timeout is written from 1 s but read from 30 s; the launch limit is any number from 0 up).
	ReadMin, ReadMax int64
	ReadBounded      bool
	// KindEnum: the values. An enum is read without regard to case; an unrecognised value reads as the default.
	Values []string
	// OptIn is for a bool that widens exposure (remote access, hub mode): only the word true turns it on, in any case; yes, 1 and on do not, so a
	// stray value fails closed. It is off by default and off for anything else.
	OptIn bool
	// OwnCheck marks a setting whose value PUT /api/settings checks with its own rule (it names the problem better than the type
	// could, or ties the value to another setting); the type check is not applied to it.
	OwnCheck bool
	// MaxLen is the longest value that is written, in bytes; zero is the general limit.
	MaxLen int
}

func (s Spec) hasRange() bool { return s.Min != 0 || s.Max != 0 }

// readBounds is the range a reader holds a number to; ok is false when there is none.
func (s Spec) readBounds() (lo, hi int64, ok bool) {
	switch {
	case s.ReadBounded:
		return s.ReadMin, s.ReadMax, true
	case s.hasRange():
		return s.Min, s.Max, true
	}
	return 0, 0, false
}

// Family is a group of keys that share a prefix (a CLI path or a default model per agent type).
type Family struct {
	Prefix string
	Kind   Kind
	Class  Class
	Apply  Apply
	Doc    string
}

var (
	specs    = map[string]Spec{}
	families []Family
)

func register(s Spec) {
	if _, dup := specs[s.Key]; dup {
		panic("settings: " + s.Key + " is declared twice")
	}
	specs[s.Key] = s
}

// Lookup is key's spec: an exact key, or a member of a family (its Spec then has the family's kind and no default).
func Lookup(key string) (Spec, bool) {
	if s, ok := specs[key]; ok {
		return s, true
	}
	for _, f := range families {
		if strings.HasPrefix(key, f.Prefix) && len(key) > len(f.Prefix) {
			return Spec{Key: key, Kind: f.Kind, Class: f.Class, Apply: f.Apply, Doc: f.Doc}, true
		}
	}
	return Spec{}, false
}

// All is every exact spec, for the docs and the census (a copy).
func All() []Spec {
	out := make([]Spec, 0, len(specs))
	for _, s := range specs {
		out = append(out, s)
	}
	return out
}

func must(key string, want ...Kind) Spec {
	s, ok := Lookup(key)
	if !ok {
		panic("settings: " + key + " is not a declared setting")
	}
	for _, k := range want {
		if s.Kind == k {
			return s
		}
	}
	panic(fmt.Sprintf("settings: %s is not a setting of that kind (it is kind %d)", key, s.Kind))
}

// Bool reads an on/off setting. The words for on are 1, true, yes and on; for off 0, false, no and off; any case, spaces ignored. Anything else, and an unset
// setting, is the default. This is the one place those words are decided. An OptIn setting is on only for the word true.
func Bool(m map[string]string, key string) bool {
	s := must(key, KindBool)
	v := m[key]
	if s.OptIn {
		return strings.EqualFold(strings.TrimSpace(v), "true")
	}
	switch {
	case config.Truthy(v):
		return true
	case config.Falsy(v):
		return false
	}
	return s.Default == "true"
}

// State says how a number setting reads.
type State int

const (
	Unset   State = iota // not set, or empty: the default applies
	Valid                // a whole number inside the setting's limits (or any, for ReadAny)
	Invalid              // set to something that is not a whole number, or outside the limits for a ReadDefault setting
)

// IntState reads a whole-number setting and says what it found, for the few readers that treat an unusable value differently from an unset one
// (the artifact store's cap fails closed on a value it cannot read). The number is the setting's value when Valid, else the default.
func IntState(m map[string]string, key string) (int64, State) {
	s := must(key, KindInt)
	def, _ := strconv.ParseInt(s.Default, 10, 64)
	raw := strings.TrimSpace(m[key])
	if raw == "" {
		return def, Unset
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return def, Invalid
	}
	if lo, hi, bounded := s.readBounds(); bounded && (n < lo || n > hi) {
		switch s.Read {
		case ReadClamp:
			return max(lo, min(hi, n)), Valid
		case ReadDefault:
			return def, Invalid
		}
	}
	return n, Valid
}

// Int reads a whole-number setting: its value, clamped or defaulted by the setting's ReadMode when it is outside the limits, and the default
// when it is unset or not a whole number.
func Int(m map[string]string, key string) int64 {
	n, _ := IntState(m, key)
	return n
}

// IntOr is Int with a default the caller knows better than the table: a setting whose default comes from Coral's configuration (a flag or an
// environment variable), such as the git poll interval. It applies when the setting is unset or unreadable.
func IntOr(m map[string]string, key string, def int64) int64 {
	n, st := IntState(m, key)
	if st == Valid {
		return n
	}
	return def
}

// Enum reads an enum setting: the declared value it matches without regard to case, or the default.
func Enum(m map[string]string, key string) string {
	s := must(key, KindEnum)
	v := strings.TrimSpace(m[key])
	for _, want := range s.Values {
		if strings.EqualFold(v, want) {
			return want
		}
	}
	return s.Default
}

// Text reads a text setting as saved: surrounding spaces are trimmed, an unset one is the default.
func Text(m map[string]string, key string) string {
	s := must(key, KindText)
	if v, ok := m[key]; ok {
		return strings.TrimSpace(v)
	}
	return s.Default
}

// Raw reads a setting exactly as stored, untouched: the value of a list kept as text, of a text setting whose spaces matter, or of one of Coral's own
// bookkeeping keys. It is the only way to read a value without its type; each use says why.
func Raw(m map[string]string, key string) string {
	must(key, KindList, KindText, KindOpaque, KindEnum, KindInt, KindBool)
	return m[key]
}

// Has reports whether the setting is saved at all (an empty value counts), for the one reader that must tell a choice from the default (the
// first-launch question about remote access).
func Has(m map[string]string, key string) bool {
	must(key, KindBool, KindInt, KindEnum, KindText, KindList, KindOpaque)
	_, ok := m[key]
	return ok
}

// TextFamily reads a text setting of a family by its full key (default_model_claude): trimmed, empty when unset. A key outside every
// declared family panics, like an undeclared key; the bare prefix (an empty type) is a member too and reads empty.
func TextFamily(m map[string]string, key string) string {
	for _, f := range families {
		if f.Kind == KindText && strings.HasPrefix(key, f.Prefix) {
			return strings.TrimSpace(m[key])
		}
	}
	panic("settings: " + key + " is not a declared text setting")
}
