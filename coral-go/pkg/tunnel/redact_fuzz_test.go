package tunnel

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// #3761: fuzz targets for the Hub redactor, so the bug class found by hand in #3723, #3731,
// #3732, #3733 and #3756 (a split, an escape, a cut or a JSON shape that lets a secret through,
// or redaction that breaks a reply) is searched for automatically.
//
//   go test ./pkg/tunnel -run '^$' -fuzz FuzzRedactStream -fuzztime 10m
//
// A secret is planted between spaces in text the fuzzer chooses. It counts as shown only by a
// run of 4 or more of its characters that the chosen text doesn't itself contain (the fuzzer may
// copy parts of it there).

// fuzzSecret is one planted secret: the text that carries it and the part that must not show.
type fuzzSecret struct{ text, secret string }

func fuzzSecrets() []fuzzSecret {
	tok := fake("", 36, alnum)
	b64 := fake("", 24, alnum+"=")
	hex := fake("", 32, "0123456789abcdef")
	return []fuzzSecret{
		{"ghp_" + tok, tok},
		{"github_pat_" + fake("", 30, alnum+"_"), fake("", 30, alnum+"_")},
		{"sk-ant-api03-" + fake("", 40, alnum+"-_"), fake("", 40, alnum+"-_")},
		{"AKIA" + fake("", 16, upper), fake("", 16, upper)},
		{"xoxb-" + fake("", 30, alnum+"-"), fake("", 30, alnum+"-")},
		{"Authorization: Bearer " + fake("", 40, alnum+"._-"), fake("", 40, alnum+"._-")},
		{"Authorization: Basic " + b64, b64},
		{`Authorization: Digest username="bob", nonce="` + hex + `"`, hex},
		{"password=" + tok, tok},
		{`DB_PASSWORD="` + tok + `"`, tok},
		{"postgres://app:" + tok + "@db.internal/main", tok},
		{"redis://:" + tok + "@cache:6379", tok},
	}
}

// fuzzShown is the first run of 4 or more characters of secret in out that noise doesn't
// contain, or "".
func fuzzShown(out, secret, noise string) string {
	for i := 0; i+4 <= len(secret); i++ {
		w := secret[i : i+4]
		if strings.Contains(out, w) && !strings.Contains(noise, w) {
			return w
		}
	}
	return ""
}

// fuzzNoise is the fuzzer's own text as it can appear in the output: as written and escaped
// once or twice in JSON (a key "\u0007el" shows "07el" in a reply without any secret in it).
func fuzzNoise(parts ...string) string {
	var sb strings.Builder
	for _, p := range parts {
		for enc := uint8(0); enc < 3; enc++ {
			sb.Write(fuzzEncode(p, enc))
			sb.WriteByte(0)
		}
	}
	return sb.String()
}

func fuzzPlant(prefix string, kind uint8, suffix string) (string, fuzzSecret) {
	all := fuzzSecrets()
	sec := all[int(kind)%len(all)]
	return prefix + " " + sec.text + " " + suffix, sec
}

// fuzzEncode returns s raw (0), as a JSON string (1) or as JSON inside a JSON string (2).
func fuzzEncode(s string, enc uint8) []byte {
	switch enc % 3 {
	case 1:
		b, _ := json.Marshal(map[string]string{"content": s})
		return b
	case 2:
		in, _ := json.Marshal(map[string]string{"content": s})
		b, _ := json.Marshal(map[string]string{"content": string(in)})
		return b
	}
	return []byte(s)
}

func fuzzRedactWrites(b []byte, cuts ...int) []byte {
	var out bytes.Buffer
	r := NewRedactor(&out)
	at := 0
	for _, c := range cuts {
		c = min(max(c, at), len(b))
		_, _ = r.Write(b[at:c])
		at = c
	}
	_, _ = r.Write(b[at:])
	_ = r.Close()
	return out.Bytes()
}

func fuzzSeedText(f *testing.F, add func(prefix, suffix string)) {
	for _, s := range []string{"", "x", "see", "\"", "\\", "'", "{\"a\": \"", "Authorization:", "Bearer", "-----BEGIN ",
		"https://", "password", "=", ":", "@", "é", "\x1b[1;32m", "\\u001b[", "\n", "a,b;c", "token: ", "<b>"} {
		add(s, s)
	}
}

// FuzzRedactStream: any text around a planted secret, raw or JSON-encoded, written in two or
// three pieces, redacts the same as written whole, and the secret doesn't show.
func FuzzRedactStream(f *testing.F) {
	fuzzSeedText(f, func(p, s string) {
		for k := range fuzzSecrets() {
			f.Add(p, uint8(k), s, uint8(0), uint16(7), uint16(19))
			f.Add(p, uint8(k), s, uint8(1), uint16(30), uint16(31))
			f.Add(p, uint8(k), s, uint8(2), uint16(1), uint16(200))
		}
	})
	f.Fuzz(func(t *testing.T, prefix string, kind uint8, suffix string, enc uint8, c1, c2 uint16) {
		text, sec := fuzzPlant(prefix, kind, suffix)
		body := fuzzEncode(text, enc)
		whole := RedactTokens(body)
		a, b := int(c1)%(len(body)+1), int(c2)%(len(body)+1)
		if a > b {
			a, b = b, a
		}
		if got := fuzzRedactWrites(body, a); !bytes.Equal(got, whole) {
			t.Fatalf("split at %d differs:\n in %q\n whole %q\n split %q", a, body, whole, got)
		}
		if got := fuzzRedactWrites(body, a, b); !bytes.Equal(got, whole) {
			t.Fatalf("split at %d, %d differs:\n in %q\n whole %q\n split %q", a, b, body, whole, got)
		}
		if w := fuzzShown(string(whole), sec.secret, fuzzNoise(prefix, suffix)); w != "" {
			t.Fatalf("%q of the secret shows:\n in %q\n out %q", w, body, whole)
		}
		if enc%3 != 0 && json.Valid(body) && !json.Valid(whole) {
			t.Fatalf("redaction broke the JSON:\n in %s\n out %s", body, whole)
		}
		// A time bound per input: the input repeated to about 64 KiB redacts in well under a
		// second, so a pattern that makes the redactor superlinear (#3756 v2's look-ahead) fails.
		if !raceEnabled {
			big := bytes.Repeat(body, max(1, (64<<10)/max(len(body), 1)))
			st := time.Now()
			RedactTokens(big)
			if took := time.Since(st); took > time.Second {
				t.Fatalf("%d bytes took %v:\n unit %q", len(big), took, body)
			}
		}
	})
}

// FuzzRedactJSON: a JSON reply with planted secrets in the shapes headers and tools use, and
// fuzzed strings around them, stays valid after the structural path, and the secrets are gone.
func FuzzRedactJSON(f *testing.F) {
	fuzzSeedText(f, func(p, s string) { f.Add(p, s, "note", uint8(0)) })
	f.Add("Authorization", "Basic", "name", uint8(3))
	f.Add("password", "x", "value", uint8(5))
	f.Fuzz(func(t *testing.T, a, b, key string, kind uint8) {
		all := fuzzSecrets()
		sec := all[int(kind)%len(all)]
		tok := fake("", 36, alnum)
		b64 := fake("", 24, alnum+"=")
		v := map[string]any{
			"a":       a,
			key:       b,
			"planted": " " + sec.text + " ",
			"headers": map[string]any{"Authorization": "Basic " + b64, "x": []any{a, b}},
			"env":     []any{map[string]any{"name": "Authorization", "value": "Bearer " + tok}},
			"dsn":     "postgres://app:" + tok + "@db/main",
			"auth":    map[string]any{"password": tok, key: a},
			"nested":  map[string]any{"content": a + " " + sec.text + " " + b},
		}
		body, err := json.Marshal(v)
		if err != nil {
			return
		}
		out, ok := redactReply(jsonHdr, body)
		if !ok {
			t.Fatalf("not redacted: %s", body)
		}
		if !json.Valid(out) {
			t.Fatalf("redaction broke the JSON:\n in %s\n out %s", body, out)
		}
		noise := fuzzNoise(a, b, key)
		for _, secret := range []string{sec.secret, tok, b64} {
			if w := fuzzShown(string(out), secret, noise); w != "" {
				t.Fatalf("%q shows:\n in %s\n out %s", w, body, out)
			}
		}
	})
}

// FuzzTrimCut: text with a planted secret, cut at any offset the way the chat page's trim (#3723)
// and the transcript reader's fixed caps cut it, then sent through the Hub's redactor as JSON:
// the secret doesn't show.
func FuzzTrimCut(f *testing.F) {
	fuzzSeedText(f, func(p, s string) {
		for k := range fuzzSecrets() {
			f.Add(p, uint8(k), s, uint16(len(p)+5))
		}
	})
	f.Fuzz(func(t *testing.T, prefix string, kind uint8, suffix string, n uint16) {
		text, sec := fuzzPlant(prefix, kind, suffix)
		cutAt := int(n) % (len(text) + 1)
		noise := fuzzNoise(prefix, suffix)
		// The reader's caps: cut, no redaction before.
		cut := text[:CutOutsideToken(text, cutAt)]
		// The chat trim: redacted, then cut.
		red := string(RedactTokens([]byte(text)))
		trimmed := red[:CutOutsideToken(red, cutAt)]
		for name, kept := range map[string]string{"cut": cut, "trim": trimmed} {
			js, _ := json.Marshal(map[string]string{"content": kept})
			out := string(RedactTokens(js))
			if w := fuzzShown(out, sec.secret, noise); w != "" {
				t.Fatalf("%s at %d: %q of the secret shows:\n text %q\n kept %q\n out %q", name, cutAt, w, text, kept, out)
			}
		}
	})
}
