package cloud

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// Fuzz targets for what the Hub answers to a sign-in operation (#4147). A plain go test runs the
// seeds; go test -fuzz FuzzStrictJSON -fuzztime 10m ./internal/cloud/ fuzzes.

// FuzzStrictJSON: the redeem and check replies are decoded by strictJSON. It never panics, and what
// it accepts is exactly one JSON object with unique keys, only the fields the struct names, and
// nothing after it: a Hub reply that smuggles a second value, a repeated key or an unknown field
// is refused.
func FuzzStrictJSON(f *testing.F) {
	for _, s := range []string{
		`{"ok":true,"status":"ok","account":"11111111-2222-4333-8444-555555555555"}`,
		`{"ok":true,"grant":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee","account":"11111111-2222-4333-8444-555555555555","display":"Ann","ttl":3600}`,
		`{"ok":true,"ok":false}`, `{"ok":false,"OK":true}`, `{"Ok":true,"status":"ok"}`,
		`{"ok":true} {"ok":false}`,
		`{"ok":true,"extra":1}`,
		`{"ok":true}x`,
		`{"ok":"true"}`,
		`[]`, `null`, `"x"`, `1`, ``, `{`, `{"ok":true,"ttl":99999999999999999999}`,
		`{"ok":true,"display":"\u202e\u0000"}`,
		`{"OK":true}`, `{"ok":true,"status":null}`,
		`{"a":{"b":{"c":[1,2,{"d":null}]}}}`,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		var r struct {
			OK      bool   `json:"ok"`
			Status  string `json:"status"`
			Grant   string `json:"grant"`
			Account string `json:"account"`
			Display string `json:"display"`
			Tier    string `json:"tier"`
			TTL     int    `json:"ttl"`
		}
		if strictJSON(b, &r) != nil {
			return
		}
		if !json.Valid(b) {
			t.Fatalf("accepted invalid JSON %q", b)
		}
		dec := json.NewDecoder(bytes.NewReader(b))
		tok, err := dec.Token()
		if err != nil || tok != json.Delim('{') {
			t.Fatalf("accepted a non-object %q", b)
		}
		seen := map[string]bool{}
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				t.Fatalf("accepted %q but its keys do not read: %v", b, err)
			}
			k, _ := kt.(string)
			if seen[k] {
				t.Fatalf("accepted a repeated key %q in %q", k, b)
			}
			seen[k] = true
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				t.Fatalf("accepted %q but a value does not read: %v", b, err)
			}
			switch k {
			case "ok", "status", "grant", "account", "display", "tier", "ttl":
			default:
				t.Fatalf("accepted the unknown key %q in %q", k, b)
			}
		}
		if _, err := dec.Token(); err != nil { // the closing brace
			t.Fatalf("accepted %q with no closing brace", b)
		}
		if _, err := dec.Token(); err != io.EOF {
			t.Fatalf("accepted trailing data after the object in %q", b)
		}
	})
}

// FuzzCleanDisplay: a Hub's display name, stored and shown. Whatever it holds, the result is valid
// UTF-8, at most 64 runes, free of control, format, line and paragraph separator characters,
// trimmed, and cleaning it again changes nothing.
func FuzzCleanDisplay(f *testing.F) {
	for _, s := range []string{"Ann", "  Ann  ", "A\x00nn", "A\u202enn", "a\u2028b\u2029c", "\x1b[31mred", strings.Repeat("é", 200), "\xff\xfe", "", "\ufeffzero width", "😀👨\u200d👩\u200d👧", "a\tb\nc"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out := CleanDisplay(s)
		if !utf8.ValidString(out) {
			t.Fatalf("invalid UTF-8 in %q", out)
		}
		if n := utf8.RuneCountInString(out); n > 64 {
			t.Fatalf("%d runes", n)
		}
		for _, r := range out {
			if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
				t.Fatalf("%U in %q (from %q)", r, out, s)
			}
		}
		if out != strings.TrimSpace(out) {
			t.Fatalf("not trimmed: %q", out)
		}
		if again := CleanDisplay(out); again != out {
			t.Fatalf("not idempotent: %q then %q", out, again)
		}
	})
}

// FuzzValidHubAccount: an account or grant id from the Hub is a plain id; whatever is accepted
// holds no separator, control or space character and is bounded.
func FuzzValidHubAccount(f *testing.F) {
	for _, s := range []string{"11111111-2222-4333-8444-555555555555", "", "x", "11111111-2222-4333-8444-55555555555", "11111111-2222-4333-8444-5555555555555", "../x", "a b", "\n", "11111111-2222-4333-8444-55555555555G"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if !ValidHubAccount(s) {
			return
		}
		if len(s) == 0 || len(s) > 128 {
			t.Fatalf("accepted %d bytes", len(s))
		}
		for i := 0; i < len(s); i++ {
			c := s[i]
			if c <= 0x20 || c >= 0x7f || strings.IndexByte("/\\:?#%&=\"'<>", c) >= 0 {
				t.Fatalf("accepted %q with the byte %q", s, c)
			}
		}
	})
}

// #4147: found by FuzzStrictJSON. encoding/json matches a key to a field ignoring case, so a reply
// with the same field in two cases passed the repeated-key rule and the last one won; a key in another
// case of a known field was taken as known. Both are refused now.
func TestStrictJSONComparesKeysExactly(t *testing.T) {
	type reply struct {
		OK     bool   `json:"ok"`
		Status string `json:"status"`
		Skip   string `json:"-"`
		Plain  string
	}
	for _, c := range []struct {
		in string
		ok bool
	}{
		{`{"ok":true,"status":"ok"}`, true},
		{`{"ok":true,"Plain":"x"}`, true},
		{`{"OK":true}`, false},
		{`{"Ok":true}`, false},
		{`{"ok":false,"OK":true}`, false},
		{`{"ok":true,"STATUS":"ok"}`, false},
		{`{"ok":true,"plain":"x"}`, false},
		{`{"ok":true,"Skip":"x"}`, false},
		{`{"ok":true,"-":"x"}`, false},
	} {
		var r reply
		if got := strictJSON([]byte(c.in), &r) == nil; got != c.ok {
			t.Errorf("%s: accepted %v, want %v", c.in, got, c.ok)
		}
	}
}
