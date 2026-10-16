package redact

import (
	"fmt"
	"io"
	"strings"
	"testing"
)

// The design's budget (#3560): under 2 ms per MB with no token, under 20 ms per MB with
// tokens, memory flat. Bodies are written in 32 KiB chunks, as the connector does.
func benchBody(size int, hits bool) []byte {
	line := `{"role":"assistant","text":"ran the tests; 42 passed in 3.1s, see task-runner output and risk-free sk-learn notes"},` + "\n"
	hit := `{"text":"export GH=` + fake("ghp_", 36, alnum) + ` and Authorization: Bearer ` + fake("", 40, alnum) + `"},` + "\n"
	var b strings.Builder
	for n := 0; b.Len() < size; n++ {
		b.WriteString(line)
		if hits && n%8 == 7 { // two tokens about every 1 KiB
			b.WriteString(hit)
		}
	}
	return []byte(b.String()[:size])
}

func BenchmarkRedactStream(b *testing.B) {
	for _, size := range []int{1 << 10, 64 << 10, 1 << 20, 16 << 20} {
		for _, hits := range []bool{false, true} {
			body := benchBody(size, hits)
			b.Run(fmt.Sprintf("%dKiB/hits=%v", size>>10, hits), func(b *testing.B) {
				b.SetBytes(int64(len(body)))
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					r := NewWriter(io.Discard)
					for rest := body; len(rest) > 0; {
						n := min(len(rest), 32<<10)
						_, _ = r.Write(rest[:n])
						rest = rest[n:]
					}
					_ = r.Close()
				}
			})
		}
	}
}
