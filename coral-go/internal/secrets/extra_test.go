package secrets

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Fake keys are built at run time, so the repository holds nothing that looks like a real one.
func fake(n int, alphabet string, seed uint32) string {
	var b strings.Builder
	x := seed
	for i := 0; i < n; i++ {
		x = x*1664525 + 1013904223
		b.WriteByte(alphabet[int(x>>24)%len(alphabet)])
	}
	return b.String()
}

const (
	alnumAZ  = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	upperNum = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	urlSafe  = alnumAZ + "-_"
)

// #4265: the provider keys betterleaks does not mask, in the shapes an agent's terminal shows.
func TestCoralMasksTheKeysTheLibraryMisses(t *testing.T) {
	require.NoError(t, Init())
	keys := []struct{ name, key string }{
		{"anthropic, full", "sk-ant-api03-" + fake(93, urlSafe, 1) + "AA"},
		{"anthropic, short", "sk-ant-api03-" + fake(40, urlSafe, 2)},
		{"anthropic, admin", "sk-ant-admin01-" + fake(60, urlSafe, 3)},
		{"openai, sk-", "sk-" + fake(48, alnumAZ, 4)},
		{"openai, sk-proj-", "sk-proj-" + fake(70, urlSafe, 5)},
		{"openai, sk-svcacct-", "sk-svcacct-" + fake(60, urlSafe, 6)},
		{"aws access key id", "AKIA" + fake(16, upperNum, 7)},
		{"aws temporary key id", "ASIA" + fake(16, upperNum, 8)},
	}
	frames := []struct{ name, format string }{
		{"bare", "%s"},
		{"in a sentence", "the key is %s, do not share it"},
		{"after a name", "KEY=%s"},
		{"quoted", `key: "%s"`},
		{"at a line end", "line one\nexport X=%s\nline three"},
	}
	for _, k := range keys {
		for _, f := range frames {
			text := strings.Replace(f.format, "%s", k.key, 1)
			out, err := Redactor().Text([]byte(text))
			require.NoError(t, err, k.name)
			assert.NotContains(t, string(out), k.key, k.name+" "+f.name)
			assert.NotContains(t, string(out), k.key[8:], k.name+" "+f.name+" (the tail)")
		}
	}
	// AWS_ACCESS_KEY_ID=... and the same key in a JSON reply.
	id := "AKIA" + fake(16, upperNum, 9)
	out, err := Redactor().Text([]byte("AWS_ACCESS_KEY_ID=" + id))
	require.NoError(t, err)
	assert.NotContains(t, string(out), id)
	reply, ok := mustReply(t, `{"env":{"AWS_ACCESS_KEY_ID":"`+id+`"},"note":"sk-proj-`+fake(70, urlSafe, 10)+`"}`)
	assert.True(t, ok)
	assert.NotContains(t, reply, id)
	assert.NotContains(t, reply, "sk-proj-")
}

func mustReply(t *testing.T, raw string) (string, bool) {
	t.Helper()
	out, err := Redactor().Reply([]byte(raw), true)
	require.NoError(t, err)
	return string(out), true
}

// Ordinary text that starts or contains the same letters is left alone.
func TestCoralsKeyRulesLeaveOrdinaryTextAlone(t *testing.T) {
	require.NoError(t, Init())
	for _, s := range []string{
		"the task-force-assessment-and-planning-document is ready",
		"risk-assessment-for-the-new-release-process",
		"see sk-learn-compatible-estimator-documentation for details",
		"sk-ant is the prefix",
		"sk-learn-release-notes-2024-final",
		"see sk-2024-planning-notes-final-revision-v3 for details",
		"AKIA is the prefix of an AWS key ID",
		"AKIA" + "IOSFODNN7" + "EXAMPLE", // Amazon's documentation key
		"AKIAlowercase0123456789",
		"BAKIA" + fake(16, upperNum, 11),
		"ASIA" + fake(17, upperNum, 12), // too long for a key ID
		"ASIA" + fake(15, upperNum, 13), // too short
		"/Users/someone/projects/sk-" + "pure-letters-only-no-digits-here",
	} {
		out, err := Redactor().Text([]byte(s))
		require.NoError(t, err)
		assert.Equal(t, s, string(out))
	}
}

// A key split across two lines of a stream is the Scanner's job (it frames units); the rules here
// see each unit whole, so a key in a line of its own is masked wherever it falls.
func TestCoralsKeyRulesMaskInAStream(t *testing.T) {
	require.NoError(t, Init())
	id := "AKIA" + fake(16, upperNum, 14)
	ant := "sk-ant-api03-" + fake(40, urlSafe, 15)
	text := strings.Repeat("filler line\n", 200) + "id " + id + "\n" + ant + "\n" + strings.Repeat("tail\n", 200)
	out, err := Redactor().Text([]byte(text))
	require.NoError(t, err)
	assert.NotContains(t, string(out), id)
	assert.NotContains(t, string(out), ant[8:])
}
