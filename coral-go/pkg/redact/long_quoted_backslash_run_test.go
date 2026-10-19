package redact

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// #5018 review: a run of backslashes at the end of a write inside an over-long quoted value is held until the byte after it is known; a
// hostile stream of nothing but backslashes then keeps all of it pending and the run is scanned again from its start on every write, so the
// work is quadratic in the run's length (16 MiB in 4 KiB writes took 15 s and 56 MiB of heap; 4 MiB took 0.9 s, 1 MiB 57 ms) where the class-only
// drop before the change took 10 ms and nothing pending. The output of an agent is the attacker's: redaction must stay linear and bounded
// on it, with the tail of the value hidden and the text after the closing quote kept.
func TestBackslashRunInAnOverlongQuotedValueIsLinear(t *testing.T) {
	if raceEnabled {
		t.Skip("a time bound")
	}
	const total = 16 << 20
	var out bytes.Buffer
	w := NewWriter(&out)
	_, _ = w.Write([]byte(`x password="` + strings.Repeat("a", 700)))
	chunk := bytes.Repeat([]byte{'\\'}, 4096)
	start := time.Now()
	for n := 0; n < total; n += len(chunk) {
		_, _ = w.Write(chunk)
	}
	_, _ = w.Write([]byte(` TAIL" after`))
	_ = w.Close()
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("%d MiB of backslashes in 4 KiB writes took %v: the held run is scanned again on every write", total>>20, took)
	}
	if bytes.Contains(out.Bytes(), []byte("TAIL")) || !bytes.HasSuffix(out.Bytes(), []byte(`" after`)) {
		t.Fatalf("the value's tail shows or the text after its quote was lost: %.60q", out.Bytes())
	}
	if out.Len() > 1024 {
		t.Fatalf("%d bytes out for a dropped value", out.Len())
	}
}
