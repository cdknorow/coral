package routes

import (
	"encoding/json"
	"testing"
	"unicode/utf8"
)

// FuzzParseReceivedMeta: whatever bytes arrive as the frame's metadata, the parser never panics, and what
// it accepts has every field inside its rules (so the rest of the receiver can rely on them).
func FuzzParseReceivedMeta(f *testing.F) {
	good, _ := json.Marshal(meta([]byte("hello\n"), "h.txt"))
	f.Add(good)
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"size":1e3}`))
	f.Add([]byte(`{"size":99999999999999999999,"size":1}`))
	f.Add(append(append([]byte{}, good...), ` {}`...))
	f.Fuzz(func(t *testing.T, raw []byte) {
		m, err := parseReceivedMeta(raw)
		if err != nil {
			return
		}
		if !utf8.Valid(raw) || m.Size < 1 || m.Size > maxReceivedArtifact || !receivedDigestRe.MatchString(m.Digest) ||
			!relayIDRe.MatchString(m.MessageID) || !receivedMediaTypeRe.MatchString(m.MediaType) ||
			!plainLabel(m.Name) || !plainLabel(m.FromBoard) || !plainLabel(m.ToAddress) {
			t.Fatalf("accepted out-of-rule metadata: %+v", m)
		}
	})
}
