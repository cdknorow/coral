package logparse

import (
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #3711: StripANSI is a one-pass byte scanner now; its output must be the old two-regexp
// version's, byte for byte, on real captures, on every kind of sequence (complete, malformed,
// cut short at every byte), on invalid UTF-8, and on random input.

var stripCases = []string{
	"plain text, no escapes",
	"\x1b[32mINFO\x1b[0m: ok",
	"\x1b[38;2;255;100;0mtruecolor\x1b[m",
	"\x1b[?2004h\x1b[?25l\x1b[2J\x1b[H",
	"\x1b[1;31;4m\x1b[K\x1b[3 q",
	"\x1b]0;window title\x07after",
	"\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\",
	"\x1b]11;?\x1b\\",
	"\x1b]2;unterminated title then text",
	"\x1b]2;ends at another ESC\x1b[31mred",
	"\x1b]2;ESC without backslash\x1bXafter",
	"\x1bPq#0;2;0;0;0#1~~@@\x1b\\", // DCS (sixel): ESC P is an Fe, the rest is text
	"\x1bP$q m\x1b\\",
	"\x1b[",
	"\x1b",
	"\x1b\x1b\x1b[0m",
	"\x1b[12;34",
	"\x1b[12;34\x01m",
	"\x1b[?1049h\x1b[\xe2\x80\x99",
	"\x1b(B\x1b)0\x1b=\x1b>",
	"\x1b7\x1b8\x1bc\x1bM\x1bD\x1bE",
	"\x1b@\x1bZ\x1b\\\x1b]\x1b^\x1b_\x1b[\x1b`",
	"tab\tnew\nline\r\nbell\x07nul\x00del\x7fvt\x0bff\x0c",
	"caf\xc3\xa9 \xe2\x9c\x93 \xf0\x9f\x98\x80 and bad \xff\xfe bytes",
	"\x1b]0;caf\xc3\xa9 \xff title\x07",
	"\x9b31m C1 CSI is just a byte here",
	"\x1b[\xff31m",
	"\r\x1b[2K\r\x1b[1A\x1b[2K\x1b[G⏺ Working… (12s · ↓ 300 tokens)",
}

func stripCorpus(t testing.TB) []string {
	t.Helper()
	var out []string
	for _, dir := range []string{"../ptymanager/testdata", "../server/routes/testdata"} {
		_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !(strings.HasSuffix(p, ".ansi") || strings.HasSuffix(p, ".txt")) {
				return nil
			}
			b, rerr := os.ReadFile(p)
			if rerr == nil {
				out = append(out, string(b))
			}
			return nil
		})
	}
	if len(out) < 10 {
		t.Fatalf("expected the real captures in testdata, found %d", len(out))
	}
	return out
}

func TestStripANSIMatchesRegexp(t *testing.T) {
	check := func(name, in string) {
		t.Helper()
		if got, want := StripANSI(in), stripANSIRegexp(in); got != want {
			t.Fatalf("%s: %q\n got %q\nwant %q", name, in, got, want)
		}
	}
	for i, in := range stripCorpus(t) {
		check("capture", in)
		for _, line := range strings.Split(in, "\n") {
			check("capture line", line)
		}
		_ = i
	}
	for _, c := range stripCases {
		check("case", c)
		for k := 0; k <= len(c); k++ { // every sequence cut short
			check("prefix", c[:k])
			check("suffix", c[k:])
		}
	}
	rng := rand.New(rand.NewSource(3711))
	alphabet := []byte("\x1b\x1b\x1b[]\\;?0123456789 !/@AZ^_`az~mPH\x07\x00\x7f\t\n\r\xc3\xa9\xff\x9b")
	for n := 0; n < 20000; n++ {
		b := make([]byte, rng.Intn(40))
		for i := range b {
			b[i] = alphabet[rng.Intn(len(alphabet))]
		}
		check("random", string(b))
	}
}

func FuzzStripANSI(f *testing.F) {
	for _, c := range stripCases {
		f.Add(c)
	}
	f.Fuzz(func(t *testing.T, in string) {
		if got, want := StripANSI(in), stripANSIRegexp(in); got != want {
			t.Fatalf("%q: got %q want %q", in, got, want)
		}
	})
}

func benchInput() string {
	var sb strings.Builder
	for i := 0; i < 10000; i++ {
		sb.WriteString("\x1b[32mINFO\x1b[0m: Processing file src/coral/static/app.js")
		sb.WriteString(" with some regular text and \x1b]2;Window Title\x07 embedded OSC\x1b[38;2;10;20;30m⏺\x1b[0m\r\n")
	}
	return sb.String()
}

// BenchmarkStripANSIRegexp is the old implementation, for comparison with BenchmarkStripANSI.
func BenchmarkStripANSIRegexp(b *testing.B) {
	input := benchInput()
	b.SetBytes(int64(len(input)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stripANSIRegexp(input)
	}
}

// BenchmarkStripANSIOnePass is StripANSI on BenchmarkStripANSIRegexp's input.
func BenchmarkStripANSIOnePass(b *testing.B) {
	input := benchInput()
	b.SetBytes(int64(len(input)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		StripANSI(input)
	}
}
