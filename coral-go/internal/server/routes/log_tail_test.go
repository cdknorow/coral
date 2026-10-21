package routes

import (
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/logparse"
)

// oldLogStatusLines is getLogStatus's recent lines as it was before #3711: the log's last
// logTailBytes read afresh, its first (partial) line dropped when the log is longer, every line
// cleaned, the last 20 kept.
func oldLogStatusLines(t *testing.T, path string) []string {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	start := int64(0)
	if info.Size() > logTailBytes {
		start = info.Size() - logTailBytes
	}
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	if start > 0 {
		_, _ = f.Seek(start, 0)
	}
	raw, err := io.ReadAll(f)
	require.NoError(t, err)
	rawLines := strings.Split(string(raw), "\n")
	if start > 0 && len(rawLines) > 0 {
		rawLines = rawLines[1:]
	}
	clean := make([]string, 0, len(rawLines))
	for _, l := range rawLines {
		clean = append(clean, logparse.StripANSI(l))
	}
	return logparse.RecentLines(clean)
}

// #3711: the log status read incrementally is, after every append, what a fresh read gives:
// lines, escapes, CRLF, invalid UTF-8, lines longer than the window (shrunk to 300 bytes so
// they cross it often), a log truncated or replaced, and appends bigger than the window.
func TestLogTailMatchesAFreshRead(t *testing.T) {
	prev := logTailBytes
	logTailBytes = 300
	t.Cleanup(func() { logTailBytes = prev })
	path := filepath.Join(t.TempDir(), "agent.log")
	require.NoError(t, os.WriteFile(path, nil, 0o644))
	rng := rand.New(rand.NewSource(3711))
	pieces := []string{"plain words ", "\n", "\r\n", "\x1b[32m", "\x1b[0m", "\x1b]0;title\x07", "\x1b[", "\x1b", "\xff\xfe",
		"⏺ Working… ", "\x07", "\t", "\x1b[2K\r"}
	var tail *logTail
	appends := 0
	for step := 0; step < oracleN(3000); step++ {
		switch r := rng.Intn(100); {
		case r < 2: // truncated (a new log in the same place)
			require.NoError(t, os.Truncate(path, 0))
		case r < 3: // replaced by a new file
			require.NoError(t, os.Remove(path))
			require.NoError(t, os.WriteFile(path, []byte("fresh log\n"), 0o644))
		case r < 5: // rewritten in place, as long or longer (not an append)
			cur, err := os.ReadFile(path)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(path, []byte(strings.Repeat("r", len(cur))+"ewritten\n"), 0o644))
		default:
			var b strings.Builder
			switch k := rng.Intn(20); {
			case k == 0:
				b.WriteString(strings.Repeat("x", 250+rng.Intn(400))) // longer than the window, no newline
			case k == 1:
				for b.Len() < 700 { // a burst bigger than the window
					b.WriteString(pieces[rng.Intn(len(pieces))])
				}
			default:
				for n := rng.Intn(8); n >= 0; n-- {
					b.WriteString(pieces[rng.Intn(len(pieces))])
				}
			}
			f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
			require.NoError(t, err)
			_, err = f.WriteString(b.String())
			require.NoError(t, err)
			require.NoError(t, f.Close())
			appends++
		}
		var status map[string]any
		status, tail = readLogTail(path, tail)
		got, _ := status["recent_lines"].([]string)
		want := oldLogStatusLines(t, path)
		require.Equal(t, want, got, "step %d", step)
		require.NotNil(t, tail)
		require.LessOrEqual(t, len(tail.lines), recentLines, "only the last lines are kept")
		require.LessOrEqual(t, int64(len(tail.part)), logTailBytes, "a long partial line isn't kept whole")
	}
	require.Greater(t, appends, oracleN(3000)*5/6) // the steps that append: most of them
}

// A log that can't be read gives the empty status, and no tail to keep.
func TestLogTailMissingLog(t *testing.T) {
	status, tail := readLogTail(filepath.Join(t.TempDir(), "none.log"), nil)
	require.Nil(t, tail)
	require.Equal(t, []string{}, status["recent_lines"])
	require.Nil(t, status["staleness_seconds"])
}

// The log status as it was before #3711, regexps included, for the benchmarks.
var (
	oldANSIRE = regexp.MustCompile(`\x1B(?:\][^\x07\x1B]*(?:\x07|\x1B\\)?|\[[0-?]*[ -/]*[@-~]|[@-Z\\-_])`)
	oldCtrlRE = regexp.MustCompile(`[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]`)
)

func oldLogStatusRegexp(path string) []string {
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	start := int64(0)
	if info.Size() > logTailBytes {
		start = info.Size() - logTailBytes
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	if start > 0 {
		_, _ = f.Seek(start, 0)
	}
	raw, _ := io.ReadAll(f)
	rawLines := strings.Split(string(raw), "\n")
	if start > 0 && len(rawLines) > 0 {
		rawLines = rawLines[1:]
	}
	clean := make([]string, 0, len(rawLines))
	for _, l := range rawLines {
		clean = append(clean, oldCtrlRE.ReplaceAllString(oldANSIRE.ReplaceAllString(l, " "), ""))
	}
	return logparse.RecentLines(clean)
}

// benchLog writes a 4 MB log of a busy TUI agent (colours, cursor moves, titles).
func benchLog(b *testing.B) string {
	b.Helper()
	path := filepath.Join(b.TempDir(), "agent.log")
	var sb strings.Builder
	for sb.Len() < 4<<20 {
		sb.WriteString("\x1b[2K\r\x1b[38;2;215;119;87m⏺\x1b[0m \x1b[1mWorking\x1b[22m… (12s · ↓ 300 tokens) \x1b]0;claude\x07\x1b[?25l some tool output text\r\n")
	}
	require.NoError(b, os.WriteFile(path, []byte(sb.String()), 0o644))
	return path
}

func appendLog(b *testing.B, path, s string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(b, err)
	_, _ = f.WriteString(s)
	_ = f.Close()
}

const benchAppend = "\x1b[2K\r\x1b[38;2;215;119;87m⏺\x1b[0m streamed output line from the agent\r\n"

// BenchmarkLogStatusOld is a poll of a growing log before #3711 (the window read afresh,
// every line cleaned with the regexps); BenchmarkLogStatusGrown is the same poll now.
func BenchmarkLogStatusOld(b *testing.B) {
	path := benchLog(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		appendLog(b, path, benchAppend)
		oldLogStatusRegexp(path)
	}
}

func BenchmarkLogStatusGrown(b *testing.B) {
	path := benchLog(b)
	_, tail := readLogTail(path, nil)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		appendLog(b, path, benchAppend)
		_, tail = readLogTail(path, tail)
	}
}

// BenchmarkLogStatusFresh is a first read (or one after a big jump): the window, once.
func BenchmarkLogStatusFresh(b *testing.B) {
	path := benchLog(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		readLogTail(path, nil)
	}
}
