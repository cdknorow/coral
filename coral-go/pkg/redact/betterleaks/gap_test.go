package betterleaks

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/pkg/redact"
)

// #3808's gap report: the fixtures Coral's own rules (redact.Rules) were built and tested
// against, run through both rule sources. By the operator's decision the library's misses are
// not failures: they are what the release notes list as no longer redacted. The test fails only
// if the table can't be built; CORAL_GAP_REPORT=<file> writes it as Markdown.
//
//   CORAL_GAP_REPORT=/tmp/gap.md go test ./pkg/redact/betterleaks -run TestGapReport -v

type gapCase struct{ category, label, text, secret string }

func gapCases() []gapCase {
	hi := func(n int, seed uint32) string { return pseudo(n, alnum, seed) }
	b64 := pseudo(24, alnum+"+/", 21) + "=="
	var cs []gapCase
	add := func(cat, label, text, secret string) { cs = append(cs, gapCase{cat, label, text, secret}) }

	// Provider tokens and keys.
	add("Provider tokens", "GitHub classic (ghp_)", "token ghp_"+hi(36, 1), hi(36, 1))
	add("Provider tokens", "GitHub fine-grained (github_pat_, 82)", "github_pat_"+pseudo(82, alnum+"_", 2), pseudo(82, alnum+"_", 2))
	add("Provider tokens", "GitHub fine-grained, short (github_pat_, 30)", "github_pat_"+pseudo(30, alnum+"_", 3), pseudo(30, alnum+"_", 3))
	add("Provider tokens", "Anthropic (sk-ant-api03-, full shape)", "sk-ant-api03-"+pseudo(93, alnum+"-_", 4)+"AA", pseudo(93, alnum+"-_", 4))
	add("Provider tokens", "Anthropic, short (sk-ant-, 40)", "sk-ant-api03-"+pseudo(40, alnum+"-_", 5), pseudo(40, alnum+"-_", 5))
	add("Provider tokens", "OpenAI-style (sk-, 48)", "sk-"+hi(48, 6), hi(48, 6))
	add("Provider tokens", "AWS access key id (AKIA)", "aws_access_key_id = AKIA"+pseudo(16, "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567", 7), pseudo(16, "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567", 7))
	add("Provider tokens", "AWS documentation key", "AKIA"+"IOSFODNN7"+"EXAMPLE", "IOSFODNN7"+"EXAMPLE")
	add("Provider tokens", "Slack bot (real shape)", "xoxb-"+pseudo(11, "0123456789", 8)+"-"+pseudo(12, "0123456789", 9)+"-"+hi(24, 10), hi(24, 10))
	add("Provider tokens", "Slack-like (xoxb-, loose)", "xoxb-"+pseudo(30, alnum+"-", 11), pseudo(30, alnum+"-", 11))
	add("Provider tokens", "Stripe live key", "sk_live_"+hi(24, 12), hi(24, 12))
	add("Provider tokens", "npm token", "npm_"+hi(36, 13), hi(36, 13))
	add("Provider tokens", "JWT", "eyJhbGciOiJIUzI1NiJ9."+pseudo(40, alnum+"-_", 14)+"."+pseudo(43, alnum+"-_", 15), pseudo(43, alnum+"-_", 15))
	key := "-----BEGIN RSA PRIVATE KEY-----\n" + pseudo(64, alnum+"+/", 16) + "\n" + pseudo(64, alnum+"+/", 17) + "\n-----END RSA PRIVATE KEY-----"
	add("Provider tokens", "PEM private key", key, pseudo(64, alnum+"+/", 16))

	// Authorization header values.
	add("Authorization", "Authorization: Basic", "Authorization: Basic "+b64, b64)
	add("Authorization", "Authorization: Bearer", "Authorization: Bearer "+hi(40, 18), hi(40, 18))
	add("Authorization", "Authorization: Digest", `Authorization: Digest username="bob", nonce="`+pseudo(32, "0123456789abcdef", 19)+`"`, pseudo(32, "0123456789abcdef", 19))
	add("Authorization", `JSON {"Authorization": "Basic ..."}`, `{"Authorization": "Basic `+b64+`"}`, b64)
	add("Authorization", `curl -H "Authorization: Bearer ..."`, `curl -H "Authorization: Bearer `+hi(40, 20)+`" https://api.example.com`, hi(40, 20))

	// Values after a secret name, high-entropy value.
	for _, n := range []string{"password", "passwd", "passphrase", "secret", "secret_key", "token", "api_key", "apikey", "access_key", "private_key", "auth", "credential"} {
		v := hi(20, uint32(len(n))*31)
		add("Secret-named values", n+"=VALUE", n+"="+v, v)
		add("Secret-named values", n+`: "VALUE"`, n+`: "`+v+`"`, v)
		add("Secret-named values", strings.ToUpper(n)+`="VALUE" (env)`, "export DB_"+strings.ToUpper(n)+`="`+v+`"`, v)
	}
	// Low entropy, or not a token.
	add("Low-entropy values", "token=abc123", "token=abc123def", "abc123def")
	add("Low-entropy values", `secret: "changeme"`, `secret: "changeme"`, "changeme")
	add("Low-entropy values", "api_key=12345678", "api_key=12345678", "12345678")
	add("Low-entropy values", "--password=hunter2", "mysql --password=hunter2 db", "hunter2")
	add("Low-entropy values", "a passphrase with spaces", `PASSPHRASE="correct horse battery staple"`, "correct horse battery")
	add("Low-entropy values", "letters only", "password: correcthorse", "correcthorse")
	add("Low-entropy values", "--api-key VALUE (space-separated flag)", "tool --api-key "+hi(20, 22), hi(20, 22))

	// URL passwords.
	for i, scheme := range []string{"postgres", "mysql", "redis", "mongodb", "amqp", "https", "ftp"} {
		pw := hi(16, uint32(40+i))
		add("URL passwords", scheme+"://user:PW@host", scheme+"://app:"+pw+"@db.internal/x", pw)
	}
	add("URL passwords", "a / in the password", "postgres://app:ab/"+hi(12, 50)+"@db/x", hi(12, 50))
	add("URL passwords", "redis://:PW@host (no user)", "redis://:"+hi(16, 51)+"@cache:6379", hi(16, 51))

	// Adjacency and JSON structure.
	add("Adjacent keywords", "token: Authorization: Basic ...", "token: Authorization: Basic "+b64, b64)
	add("Adjacent keywords", `token:"<500 bytes> DB_PASSWORD="..."`, `token:"`+strings.Repeat("0", 508)+` DB_PASSWORD="`+hi(20, 52)+`"`, hi(20, 52))
	add("JSON structure", `{"secrets":{"db":"..."}}`, `{"secrets":{"db":"`+hi(20, 53)+`"}}`, hi(20, 53))
	add("JSON structure", `{"password": 12345678}`, `{"password": 12345678}`, "12345678")
	add("JSON structure", `{"name":"API_KEY","value":"..."}`, `{"env":[{"name":"API_KEY","value":"`+hi(20, 54)+`"}]}`, hi(20, 54))
	return cs
}

func gapShown(t *testing.T, r redact.Redactor, c gapCase) bool {
	t.Helper()
	b := []byte(c.text)
	var out []byte
	var err error
	if json.Valid(b) {
		out, err = r.Reply(b, true)
	} else {
		out, err = r.Text(b)
	}
	require.NoError(t, err, c.label)
	return strings.Contains(string(out), c.secret)
}

func TestGapReport(t *testing.T) {
	lib := scanner(t)
	var b strings.Builder
	fmt.Fprintf(&b, "| Category | Fixture | Coral's old rules | betterleaks %s |\n|---|---|---|---|\n", Version)
	type tally struct{ n, old, lib int }
	totals := map[string]*tally{}
	var order []string
	var lost, gained []string
	for _, c := range gapCases() {
		oldCaught := !gapShown(t, redact.Rules, c)
		libCaught := !gapShown(t, lib, c)
		if totals[c.category] == nil {
			totals[c.category] = &tally{}
			order = append(order, c.category)
		}
		tl := totals[c.category]
		tl.n++
		if oldCaught {
			tl.old++
		}
		if libCaught {
			tl.lib++
		}
		mark := func(v bool) string {
			if v {
				return "redacted"
			}
			return "**shown**"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", c.category, strings.ReplaceAll(c.label, "|", "\\|"), mark(oldCaught), mark(libCaught))
		switch {
		case oldCaught && !libCaught:
			lost = append(lost, c.category+": "+c.label)
		case !oldCaught && libCaught:
			gained = append(gained, c.category+": "+c.label)
		}
	}
	fmt.Fprintf(&b, "\n| Category | Fixtures | Old rules redact | betterleaks redacts |\n|---|---|---|---|\n")
	for _, cat := range order {
		tl := totals[cat]
		fmt.Fprintf(&b, "| %s | %d | %d | %d |\n", cat, tl.n, tl.old, tl.lib)
	}
	fmt.Fprintf(&b, "\nNo longer redacted (%d):\n", len(lost))
	for _, l := range lost {
		fmt.Fprintf(&b, "- %s\n", l)
	}
	fmt.Fprintf(&b, "\nNewly redacted (%d):\n", len(gained))
	for _, g := range gained {
		fmt.Fprintf(&b, "- %s\n", g)
	}
	// #3884: coral-hub's comparison, folded in — each shape over runtime-random values.
	writeRandomGapTable(t, &b, lib)
	t.Log("\n" + b.String())
	if p := os.Getenv("CORAL_GAP_REPORT"); p != "" {
		require.NoError(t, os.WriteFile(p, []byte(b.String()), 0o644))
	}
}
