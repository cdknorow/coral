package routes

import (
	"bytes"
	"context"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/pkg/redact"
)

// The sender-side scan against Security's #4438 matrix: each re-encoding of a token is either caught
// or listed as not caught (cross-team.md says the same list).
func TestScanAttachmentEncodingMatrix(t *testing.T) {
	token := "ghp_" + "1a2B3c4D5e6F7g8H9i0J1k2L3m4N5o6P7q8R"
	awsKey := "AKIA" + "ZQ3XK7JD2MNB5RTP"
	wrap := func(s string) string { return "# notes\n" + s + "\nend\n" }
	chunks := func(s string, n int) string {
		var parts []string
		for len(s) > n {
			parts, s = append(parts, s[:n]), s[n:]
		}
		return strings.Join(append(parts, s), "\n")
	}
	rev := func(s string) string { return string(reverseBytes([]byte(s))) }
	b64 := base64.StdEncoding.EncodeToString([]byte("token=" + token))

	caught := map[string]string{
		"plain":                      "deploy with token=" + token,
		"in a line of prose":         "first we log in, then " + token + " is used for the push",
		"base64":                     "data: " + b64,
		"base64 of a bare token":     base64.StdEncoding.EncodeToString([]byte(token)),
		"base64 unpadded":            strings.TrimRight(base64.StdEncoding.EncodeToString([]byte("x="+token)), "="),
		"base64 url alphabet":        base64.RawURLEncoding.EncodeToString([]byte("??>>?>token=" + token)),
		"base64 wrapped at 20":       chunks(b64, 20),
		"base64 of base64":           base64.StdEncoding.EncodeToString([]byte(b64)),
		"hex":                        hex.EncodeToString([]byte(token)),
		"url encoded underscore":     strings.Replace(token, "_", "%5F", 1),
		"url encoded all":            url.QueryEscape(token) + "&x=" + strings.ReplaceAll(token[:6], "g", "%67"),
		"split by a newline":         token[:12] + "\n" + token[12:],
		"split by a space":           token[:10] + " " + token[10:],
		"split by a backtick":        token[:8] + "`" + token[8:],
		"split by quotes and a plus": `"` + token[:9] + `" + "` + token[9:] + `"`,
		"reversed":                   rev(token),
		"html entity":                strings.Replace(token, "h", "&#104;", 1),
		"html entity hex":            strings.Replace(token, "g", "&#x67;", 1),
		"aws access key id":          "key " + awsKey,
		"aws key id as hex":          hex.EncodeToString([]byte(awsKey)),
	}
	for name, body := range caught {
		a := attachedArtifact{ID: "ab"}
		r := scanAttachment(context.Background(), a, []byte(wrap(body)))
		if assert.NotNil(t, r, "%s should be caught", name) {
			assert.Equal(t, "attach_secret", r.code, name)
		}
	}

	// What is not caught, and says so in cross-team.md: a clean file and these pass.
	notCaught := map[string]string{
		"rot13":                      rot13(token),
		"base32":                     base32.StdEncoding.EncodeToString([]byte(token)),
		"split by a comma":           token[:10] + "," + token[10:],
		"fragments out of order":     token[10:] + "\n" + token[:10],
		"base64 three layers deep":   base64.StdEncoding.EncodeToString([]byte(base64.StdEncoding.EncodeToString([]byte(b64)))),
		"the documentation key":      "AKIA" + "IOSFODNN7" + "EXAMPLE",
		"ordinary prose":             "The release is tagged and the team checked everything twice.",
		"a long word":                "internationalizationandlocalizationofthesoftware",
		"a uuid and a sha":           "id 3f2b8c1e-9d4a-4b7e-8a61-5c2d9e0f1a3b sha 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
		"a base64 image reference":   "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte(strings.Repeat("\x89PNG\r\n\x1a\n\x00\x00", 12))),
		"a percent-encoded sentence": url.QueryEscape("the quick brown fox jumps over the lazy dog"),
	}
	for name, body := range notCaught {
		assert.Nil(t, scanAttachment(context.Background(), attachedArtifact{ID: "ab"}, []byte(wrap(body))), "%s is listed as not caught or clean", name)
	}
}

func rot13(s string) string {
	b := []byte(s)
	for i, c := range b {
		switch {
		case c >= 'a' && c <= 'z':
			b[i] = 'a' + (c-'a'+13)%26
		case c >= 'A' && c <= 'Z':
			b[i] = 'A' + (c-'A'+13)%26
		}
	}
	return string(b)
}

// More encoded runs or decoded bytes than the bounds take fails closed (attach_unchecked), never passes.
func TestScanAttachmentBudgetFailsClosed(t *testing.T) {
	run := func(i int) string {
		return base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("plain note number %06d", i)))
	}

	var few, many strings.Builder
	for i := 0; i < scanMaxRuns-8; i++ {
		fmt.Fprintln(&few, "note", run(i))
	}
	assert.Nil(t, scanAttachment(context.Background(), attachedArtifact{ID: "ab"}, []byte(few.String())), "under the bound passes")
	for i := 0; i < scanMaxRuns+8; i++ {
		fmt.Fprintln(&many, "note", run(i))
	}
	r := scanAttachment(context.Background(), attachedArtifact{ID: "ab"}, []byte(many.String()))
	require.NotNil(t, r)
	assert.Equal(t, "attach_unchecked", r.code)
	assert.Equal(t, http.StatusUnprocessableEntity, r.status)

	// A secret behind the runs the budget did not reach is not passed either: it fails closed.
	r = scanAttachment(context.Background(), attachedArtifact{ID: "ab"}, []byte(many.String()+"token=ghp_1a2B3c4D5e6F7g8H9i0J1k2L3m4N5o6P7q8R\n"))
	require.NotNil(t, r)

	// The decoded-bytes bound: a few huge runs.
	big := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("a plain line of text\n", 60000)))
	require.Greater(t, len(big), scanMaxDecoded/2)
	r = scanAttachment(context.Background(), attachedArtifact{ID: "ab"}, []byte(big+"\n\n"+big+"\n\n"+big))
	require.NotNil(t, r)
	assert.Equal(t, "attach_unchecked", r.code)
}

// The views must not turn ordinary documents into refusals: Coral's own agent docs (prose, tables, code
// fences, keyword words such as "password:" and "token") all pass the scan.
func TestScanAttachmentLeavesTheAgentDocsAlone(t *testing.T) {
	files, err := filepath.Glob("../../../agent_docs/*.md")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	for _, f := range files {
		b, err := os.ReadFile(f)
		require.NoError(t, err)
		assert.Nil(t, scanAttachment(context.Background(), attachedArtifact{ID: "ab"}, b), f)
	}
}

// Joining split fragments needs a digit or an underscore in one of them, and stops at ":" and "=".
func TestDropSplittersKeepsProse(t *testing.T) {
	assert.Equal(t, "password: value does not matter", string(dropSplitters([]byte("password: value does not matter"))))
	assert.Equal(t, "ghp_abcd1234", string(dropSplitters([]byte("ghp_ab `cd` 1234"))))
	assert.Equal(t, "token = x", string(dropSplitters([]byte("token = x"))))
}

// A log with a path on every line, or long URLs and identifiers, holds many candidate runs and none that
// decodes to text: it is not refused (Security's #4439 round 1).
func TestScanAttachmentPathLogIsNotRefused(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 600; i++ {
		fmt.Fprintf(&b, "2026-10-17T11:%02d:00 INFO opened /Users/someone/Software/coral/coral-go/internal/server/routes/file_%d.go in 12ms\n", i%60, i)
		fmt.Fprintf(&b, "GET https://example.com/api/board/some-long-project-name/artifacts/%d?x=longIdentifierWithoutAnySpacesAtAll%d\n", i, i)
	}
	assert.Nil(t, scanAttachment(context.Background(), attachedArtifact{ID: "ab"}, []byte(b.String())))
	views, ok := scanViews([]byte(b.String()))
	assert.True(t, ok)
	_ = views

	// And a secret in such a log is still found.
	r := scanAttachment(context.Background(), attachedArtifact{ID: "ab"}, []byte(b.String()+"token="+base64.StdEncoding.EncodeToString([]byte("ghp_1a2B3c4D5e6F7g8H9i0J1k2L3m4N5o6P7q8R"))+"\n"))
	require.NotNil(t, r)
	assert.Equal(t, "attach_secret", r.code)
}

// fakeScan is a scan environment on a fake clock: each pass takes what take says, answers ErrUnchecked when
// that is more than the budget it was given (as the real scanner does at its budget), and records the budgets.
type fakeScan struct {
	clock   time.Time
	take    func(n int) time.Duration
	budgets []time.Duration
	spent   time.Duration
}

func (f *fakeScan) env() scanEnv {
	return scanEnv{
		now: func() time.Time { return f.clock },
		text: func(budget time.Duration, v []byte) ([]byte, error) {
			f.budgets = append(f.budgets, budget)
			d := f.take(len(v))
			if d > budget {
				f.clock, f.spent = f.clock.Add(budget), f.spent+budget
				return nil, redact.ErrUnchecked
			}
			f.clock, f.spent = f.clock.Add(d), f.spent+d
			return v, nil
		},
	}
}

// A pass over a file gets 3 s plus 4 s a MiB, so a near-8 MiB file passes on a slow machine; a small file
// gets the 3 s it always had (#4453, the Orchestrator's budget).
func TestScanBudgetGrowsWithTheFile(t *testing.T) {
	assert.Equal(t, 3*time.Second, scanPassBudget(0))
	assert.InDelta(t, float64(3*time.Second), float64(scanPassBudget(1<<10)), float64(10*time.Millisecond), "a small file: about 3 s")
	assert.Equal(t, 7*time.Second, scanPassBudget(1<<20))
	assert.Equal(t, 33*time.Second, scanPassBudget(7<<20+512<<10), "7.5 MiB: 33 s, about 15 times an idle machine's pass")
	assert.Equal(t, 35*time.Second, scanPassBudget(8<<20))
	assert.Less(t, scanFileCap, 125*time.Second, "under the 125 s the command line waits for one attachment")

	// A file whose passes take 20 s each (a loaded machine, 7.5 MiB) is checked, where 3 s would have refused it.
	data := bytes.Repeat([]byte("ordinary line of a big log file\n"), (7<<20+512<<10)/32)
	f := &fakeScan{clock: time.Unix(0, 0), take: func(int) time.Duration { return 20 * time.Second }}
	assert.Equal(t, scanClean, scanFile(f.env(), data))
	require.NotEmpty(t, f.budgets)
	assert.Equal(t, scanPassBudget(len(data)), f.budgets[0], "the first pass gets the size's budget")
	// The same file under the flat 3 s is refused: what the scaling is for.
	f2 := &fakeScan{clock: time.Unix(0, 0), take: func(int) time.Duration { return 20 * time.Second }}
	assert.Equal(t, scanUnchecked, scanWith(f2.env(), data, func(int) time.Duration { return textBudget }))

	// A small file still gets about 3 s and is refused when a pass takes longer.
	small := []byte("a small note\n")
	f3 := &fakeScan{clock: time.Unix(0, 0), take: func(int) time.Duration { return 4 * time.Second }}
	assert.Equal(t, scanUnchecked, scanFile(f3.env(), small))
	assert.InDelta(t, float64(3*time.Second), float64(f3.budgets[0]), float64(10*time.Millisecond))
}

// The whole file's passes together never take more than scanFileCap, however dense: a pass is given only what
// is left, and no time left is unchecked.
func TestScanFileCapHoldsAgainstADenseFile(t *testing.T) {
	// Views that are each as big as the file: reversed text and joined fragments, as a hostile file makes them.
	data := bytes.Repeat([]byte("ghp_ab cd1234 "), 8<<20/14)
	views, ok := scanViews(data)
	require.True(t, ok)
	require.GreaterOrEqual(t, len(views), 2, "a dense file has several full-size views")

	// Each pass takes 30 s: three fit under 100 s, the fourth is given the 10 s left and runs out.
	f := &fakeScan{clock: time.Unix(0, 0), take: func(int) time.Duration { return 30 * time.Second }}
	passes := 1 + len(views)
	out := scanFile(f.env(), data)
	assert.LessOrEqual(t, f.spent, scanFileCap, "the file's scan never takes more than the cap")
	if passes >= 4 {
		assert.Equal(t, scanUnchecked, out, "out of time: refused, not passed")
		require.GreaterOrEqual(t, len(f.budgets), 4)
		assert.Equal(t, 10*time.Second, f.budgets[3], "the fourth pass gets only what is left")
	}

	// A hostile pass that always takes its whole budget ends the scan at once.
	g := &fakeScan{clock: time.Unix(0, 0), take: func(int) time.Duration { return time.Hour }}
	assert.Equal(t, scanUnchecked, scanFile(g.env(), data))
	assert.Len(t, g.budgets, 1)
	assert.LessOrEqual(t, g.spent, scanFileCap)

	// Nothing left is refused without a pass.
	h := &fakeScan{clock: time.Unix(0, 0), take: func(int) time.Duration { return 0 }}
	env := h.env()
	env.now = func() time.Time { h.clock = h.clock.Add(scanFileCap); return h.clock }
	assert.Equal(t, scanUnchecked, scanFile(env, data))
	assert.Empty(t, h.budgets)
}

// The two entry points use the two budgets: an attachment's scan the file's, a message's text the 3 s of a reply.
func TestEntryPointsUseTheirBudgets(t *testing.T) {
	old := liveScan
	t.Cleanup(func() { liveScan = old })
	data := bytes.Repeat([]byte("a line of a big text file\n"), (6<<20)/26)

	f := &fakeScan{clock: time.Unix(0, 0), take: func(int) time.Duration { return time.Second }}
	liveScan = f.env()
	assert.Equal(t, scanClean, scanSecrets(data))
	require.NotEmpty(t, f.budgets)
	assert.Equal(t, scanPassBudget(len(data)), f.budgets[0], "an attachment: the budget of its size")
	assert.Greater(t, f.budgets[0], 20*time.Second)

	m := &fakeScan{clock: time.Unix(0, 0), take: func(int) time.Duration { return time.Second }}
	liveScan = m.env()
	assert.Equal(t, scanClean, scanMessageText(data))
	require.NotEmpty(t, m.budgets)
	for _, b := range m.budgets {
		assert.Equal(t, 3*time.Second, b, "a message's text keeps the 3 s, whatever its size")
	}
}
