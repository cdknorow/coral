package secrets

import (
	"strings"
	"testing"
	"unicode/utf8"

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

// #4265 commit 2: header values, JWTs, more provider tokens, and a key with an invisible character in it.
func TestCoralMasksHeadersJWTsAndMoreKeys(t *testing.T) {
	require.NoError(t, Init())
	jwt := "eyJ" + fake(20, urlSafe, 21) + ".eyJ" + fake(30, urlSafe, 22) + "." + fake(43, urlSafe, 23)
	cases := []struct{ name, text, secret string }{
		{"bearer header", "Authorization: Bearer " + fake(40, alnumAZ, 24), ""},
		{"bearer in a curl trace", "> Authorization: Bearer " + fake(40, alnumAZ, 25) + "\r", ""},
		{"bearer in JSON", `{"Authorization": "Bearer ` + fake(40, alnumAZ, 26) + `"}`, fake(40, alnumAZ, 26)},
		{"basic header", "Authorization: Basic " + fake(24, alnumAZ+"+/", 27) + "==", fake(24, alnumAZ+"+/", 27)},
		{"jwt", "token " + jwt + " ok", jwt},
		{"hugging face", "HF_TOKEN=hf_" + fake(34, alnumAZ, 28), ""},
		{"google oauth", "ya29." + fake(60, urlSafe, 29), ""},
		{"aws secret, spaced", "aws_secret_access_key = " + fake(40, alnumAZ+"+/", 30), ""},
	}
	for _, c := range cases {
		out, err := Redactor().Text([]byte(c.text))
		require.NoError(t, err, c.name)
		secret := c.secret
		if secret == "" { // the value is what follows the last space or equals sign
			secret = c.text[strings.LastIndexAny(c.text, " =\"")+1:]
			secret = strings.TrimRight(secret, "\"}\r")
		}
		assert.NotContains(t, string(out), secret[len(secret)/2:], c.name)
		assert.NotEqual(t, c.text, string(out), c.name)
	}
}

func TestCoralKeyRulesLeaveProseAndNamesAlone(t *testing.T) {
	require.NoError(t, Init())
	for _, s := range []string{
		"the bearer of the news arrived yesterday afternoon",
		"bearer token support is planned for the next release of the tool",
		"Authorization: Basic is described in RFC 7617",
		"a basic example of the configuration file format",
		"eyJ is how a base64 encoded JSON object starts",
		"hf_hub_download is the function name to call here",
		"the ya29 prefix marks a Google token",
	} {
		out, err := Redactor().Text([]byte(s))
		require.NoError(t, err)
		assert.Equal(t, s, string(out))
	}
}

// An invisible character inside a key ends a rule's match and used to leave the rest showing.
func TestAnInvisibleCharacterInsideAKeyDoesNotHideIt(t *testing.T) {
	require.NoError(t, Init())
	for _, mark := range []rune{0x200B, 0x200C, 0x200D, 0x2060, 0x2062, 0xFEFF, 0x00AD, 0x202E, 0x061C, 0xFFF9, 0xE0041, 0xFE0F, 0x0301, 0x034F, 0x0000, 0x001B, 0x0085} {
		id := "AKIA" + fake(8, upperNum, 31) + string(mark) + fake(8, upperNum, 32)
		out, err := Redactor().Text([]byte("id: " + id + " end"))
		require.NoError(t, err)
		assert.NotContains(t, string(out), "AKIA", "mark %U", mark)
		assert.Contains(t, string(out), "id: ", "the rest of the line is kept")
		assert.Contains(t, string(out), " end", "the rest of the line is kept")
		ant := "sk-ant-api03-" + fake(20, urlSafe, 33) + string(mark) + fake(20, urlSafe, 34)
		out, err = Redactor().Text([]byte(ant))
		require.NoError(t, err)
		assert.NotContains(t, string(out), fake(20, urlSafe, 34), "mark %U", mark)
	}
	// A genuine U+FFFD beside a mark, around a key: valid output, the key masked, the rest kept.
	mixed := "x" + string(rune(0xFFFD)) + string(rune(0x200B)) + " AKIA" + fake(16, upperNum, 35) + " y"
	out, err := Redactor().Text([]byte(mixed))
	require.NoError(t, err)
	assert.True(t, utf8.ValidString(string(out)))
	assert.NotContains(t, string(out), "AKIA")
	assert.True(t, strings.HasPrefix(string(out), "x"+string(rune(0xFFFD))))
	// Text with an invisible character but no key is unchanged.
	plain := "cafe notes" + string(rune(0x200B)) + " and a line"
	out, err = Redactor().Text([]byte(plain))
	require.NoError(t, err)
	assert.Equal(t, plain, string(out))
}

// #5943: betterleaks' gcp-api-key drops a finding whose entropy is 4.0 or less (a placeholder filter), and about one random Google key in 2000 is that low, so a real key was shown whole
// (AIza_99wwxuvvooOGwerO9eA2evoewoN2IQNrx9, 3.98). Coral's own rule has no entropy filter and keeps the library's other protections.
func TestAGoogleKeyOfLowEntropyIsMasked(t *testing.T) {
	require.NoError(t, Init())
	const low = "AIza_99wwxuvvooOGwerO9eA2evoewoN2IQNrx9" // the key the miss-rate harness missed
	keys := []string{
		low,
		"AIza" + fake(35, "ab", 41),     // two symbols: entropy 1
		"AIza" + fake(35, "abcd01", 42), // six: about 2.6
		"AIza" + fake(35, "-_ab9", 43),  // hyphens and underscores in the body
		"AIza" + fake(35, alnumAZ, 44),
	}
	frames := []string{"%s", "key %s", `apiKey: "%s"`, "the key is %s, do not share it", "?key=%s&alt=json", "(%s)", "KEY=%s;", "line one\n%s\nline three", "a=%s."}
	for _, k := range keys {
		for _, f := range frames {
			text := strings.Replace(f, "%s", k, 1)
			out, err := Redactor().Text([]byte(text))
			require.NoError(t, err, k)
			assert.NotContains(t, string(out), k[4:], "%q in %q", k, f)
		}
	}
}

// What the library's rule leaves alone, Coral's rule leaves alone: its documented example keys, a run of one character, a token that is longer or shorter than a key, another case.
func TestAGoogleKeyRuleLeavesPlaceholdersAndExamplesAlone(t *testing.T) {
	require.NoError(t, Init())
	for _, s := range []string{
		"AIzaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",               // 35 of one character
		"see AIzaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa here",      // in a sentence (after a key-like name, the header rule masks it by name, which is not this rule)
		"AIza" + strings.Repeat("0", 8) + fake(27, alnumAZ, 51), // a run of eight zeros
		"AIzaSyabcdefghijklmnopqrstuvwxyz1234567",               // the Firebase SDK's example keys
		"AIzaSyAnLA7NfeLquW1tJFpx_eQCxoX-oo6YyIs",
		"AIzaSyCkEhVjf3pduRDt6d1yKOMitrUEke8agEM",
		"AIzaSyDMAScliyLx7F0NPDEJi1QmyCgHIAODrlU",
		"AIzaSyD3asb-2pEZVqMkmL6M9N6nHZRR_znhrh0",
		"AIzayDNSXIbFmlXbIE6mCzDLQAqITYefhixbX4A",
		"AIzaSyBcE-OOIbhjyR83gm4r2MFCu4MJmprNXsw",
		"AIzaSyA8vmApnrHNFE0bApF4hoZ11srVL_n0nvY",
		"AIzaTesb6Tscfcni8vIpWZqNCXFDFslJtVSvFDqabcd123", // longer than a key: the library's text-boundary sample
		"GWw4hjABFzZCGiRpmlDyDdo87Jn9BN9THUA47muVRNunLxsa82tMAdvmrhOqNkRKiYMEAFbTJAIzaTesb6Tscfcni8vIpWZqNCXFDFslJtVSvFDq", // a key shape inside a longer token
		"AIZASYCO2CXRMC9ELSKLHLHRMBSWDEVEDZTLO2O", // another case
		"AIza is the prefix of a Google API key",
		"AIza" + fake(34, alnumAZ, 52), // one short
	} {
		// Coral's own rule is what is asked (the library's other rules, and the keyword rules after "apiKey:", may mask what they judge a secret): it finds no span.
		assert.Empty(t, extraSpans(s), s)
	}
	assert.True(t, googleKeyOK("AIza"+fake(35, alnumAZ, 53)))
	for k := range googleExampleKeys {
		assert.False(t, googleKeyOK(k), k)
	}
}
