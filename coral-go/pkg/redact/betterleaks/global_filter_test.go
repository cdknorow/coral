package betterleaks

import (
	"context"
	"math/rand"
	"strings"
	"testing"

	"github.com/betterleaks/betterleaks/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const b64 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
const alnumSet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

func randFrom(r *rand.Rand, set string, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = set[r.Intn(len(set))]
	}
	return string(b)
}

// #5179: betterleaks' global filter read `(?i)^true|false|null$` as "the secret is true, false or null", but it means: starts with "true", contains
// "false", or ends with "null", and so threw away any finding that held "false" in any case (one random 1,600-character private key body in 3,600 does).
// The secrets here are built at run time and each holds the word, in several cases and places.
func TestASecretHoldingFalseTrueOrNullIsStillFound(t *testing.T) {
	d, err := New()
	require.NoError(t, err)
	r := rand.New(rand.NewSource(5179))
	find := func(text string) bool {
		sp, err := d.Find(context.Background(), text)
		require.NoError(t, err)
		return len(sp) > 0
	}
	for i := 0; i < 200; i++ {
		word := []string{"false", "FALSE", "FaLsE", "fAlSe", "False"}[i%5]
		// A private key body (the rule has no entropy gate, so only the global filter can discard it).
		body := randFrom(r, b64, 300)
		at := r.Intn(len(body) - 10)
		key := "-----BEGIN OPENSSH PRIVATE KEY-----\n" + body[:at] + word + body[at+len(word):] + "\n-----END OPENSSH PRIVATE KEY-----"
		assert.True(t, find(key), "a private key whose body holds %q is found", word)
		// A GitHub token ending in null, one starting with true: also thrown away before.
		assert.True(t, find("token ghp_"+randFrom(r, alnumSet, 32)+[]string{"null", "NULL", "Null"}[i%3]+" end"), "a token ending in null is found")
	}
	// What the filter meant to drop still goes: the bare words.
	assert.False(t, find("-----BEGIN PRIVATE KEY-----\nfalse\n-----END PRIVATE KEY-----"), "too short to be a key (the rule wants 64 characters)")
}

// The fix is in the filter itself: the unanchored alternation is gone after New, and an unfixed one is refused.
func TestGlobalFilterIsAnchored(t *testing.T) {
	cfg, err := config.Default()
	require.NoError(t, err)
	require.NoError(t, fixGlobalFilter(cfg))
	assert.NotContains(t, cfg.Filter, "^true|false|null$")
	assert.Contains(t, cfg.Filter, anchoredBoolWords)

	// A filter that keeps the fault after the replacement (a different quoting, say) is refused.
	bad := &config.Config{Filter: "matchesAny(finding[\"secret\"], [\n  '(?i)^true|false|null$',\n])"}
	assert.Error(t, fixGlobalFilter(bad))
	// One that never had it (fixed upstream) passes untouched.
	ok := &config.Config{Filter: strings.Repeat("x", 3)}
	require.NoError(t, fixGlobalFilter(ok))
	assert.Equal(t, "xxx", ok.Filter)
}
