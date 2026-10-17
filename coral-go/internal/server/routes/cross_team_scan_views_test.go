package routes

import (
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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
		r := scanAttachment(a, []byte(wrap(body)))
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
		assert.Nil(t, scanAttachment(attachedArtifact{ID: "ab"}, []byte(wrap(body))), "%s is listed as not caught or clean", name)
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
	assert.Nil(t, scanAttachment(attachedArtifact{ID: "ab"}, []byte(few.String())), "under the bound passes")
	for i := 0; i < scanMaxRuns+8; i++ {
		fmt.Fprintln(&many, "note", run(i))
	}
	r := scanAttachment(attachedArtifact{ID: "ab"}, []byte(many.String()))
	require.NotNil(t, r)
	assert.Equal(t, "attach_unchecked", r.code)
	assert.Equal(t, http.StatusUnprocessableEntity, r.status)

	// A secret behind the runs the budget did not reach is not passed either: it fails closed.
	r = scanAttachment(attachedArtifact{ID: "ab"}, []byte(many.String()+"token=ghp_1a2B3c4D5e6F7g8H9i0J1k2L3m4N5o6P7q8R\n"))
	require.NotNil(t, r)

	// The decoded-bytes bound: a few huge runs.
	big := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("a plain line of text\n", 60000)))
	require.Greater(t, len(big), scanMaxDecoded/2)
	r = scanAttachment(attachedArtifact{ID: "ab"}, []byte(big+"\n\n"+big+"\n\n"+big))
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
		assert.Nil(t, scanAttachment(attachedArtifact{ID: "ab"}, b), f)
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
	assert.Nil(t, scanAttachment(attachedArtifact{ID: "ab"}, []byte(b.String())))
	views, ok := scanViews([]byte(b.String()))
	assert.True(t, ok)
	_ = views

	// And a secret in such a log is still found.
	r := scanAttachment(attachedArtifact{ID: "ab"}, []byte(b.String()+"token="+base64.StdEncoding.EncodeToString([]byte("ghp_1a2B3c4D5e6F7g8H9i0J1k2L3m4N5o6P7q8R"))+"\n"))
	require.NotNil(t, r)
	assert.Equal(t, "attach_secret", r.code)
}
