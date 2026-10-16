package betterleaks

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	cryptorand "crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/cdknorow/coral/pkg/redact"
)

// #3884: coral-hub's redaction comparison, folded into the gap report. Each shape is run with
// runtimeRandomRuns runtime-random values (not the fixed seeds gapCases uses), and the Markdown
// table shows how many of them each rule source redacts. It records; by the operator's decision
// (library only) nothing here fails. Realistic hosts (acme-corp.io), never example.com, except
// the one example-host shape that documents how a placeholder host is treated. The EC and
// ed25519 keys are generated in process (never openssl/ssh-keygen).

const runtimeRandomRuns = 20

// gapShape generates one fixture (text, and the secret substring that must not survive) from r.
type gapShape struct {
	category string
	label    string
	gen      func(r *rand.Rand) (text, secret string)
}

func randToken(r *rand.Rand, n int, charset string) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = charset[r.Intn(len(charset))]
	}
	return string(b)
}

// a realistic private host (never example.com, which a detector may treat as a placeholder).
func randHost(r *rand.Rand) string {
	names := []string{"acme-corp.io", "db.acme-corp.io", "cache.acme-corp.io", "api.acme-corp.io", "internal.acme-corp.io"}
	return names[r.Intn(len(names))]
}

func ecKeyPEM(r *rand.Rand) string {
	k, err := ecdsa.GenerateKey(elliptic.P256(), cryptorand.Reader)
	if err != nil {
		return ""
	}
	der, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		return ""
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))
}

func ed25519KeyPEM(r *rand.Rand) string {
	_, priv, err := ed25519.GenerateKey(cryptorand.Reader)
	if err != nil {
		return ""
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return ""
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func gapShapes() []gapShape {
	hi := func(r *rand.Rand, n int) string { return randToken(r, n, alnum) }
	secretNames := []string{"password", "passwd", "passphrase", "secret", "secret_key", "token", "api_key", "apikey", "access_key", "auth", "credential"}
	var s []gapShape
	add := func(cat, label string, gen func(r *rand.Rand) (string, string)) {
		s = append(s, gapShape{cat, label, gen})
	}

	// Provider tokens (random bodies).
	add("Provider tokens", "GitHub classic ghp_", func(r *rand.Rand) (string, string) { v := hi(r, 36); return "token ghp_" + v, v })
	add("Provider tokens", "Anthropic sk-ant-api03-", func(r *rand.Rand) (string, string) {
		v := randToken(r, 93, alnum+"-_")
		return "sk-ant-api03-" + v + "AA", v
	})
	add("Provider tokens", "OpenAI sk-", func(r *rand.Rand) (string, string) { v := hi(r, 48); return "sk-" + v, v })
	add("Provider tokens", "AWS AKIA", func(r *rand.Rand) (string, string) {
		v := randToken(r, 16, "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567")
		return "aws_access_key_id = AKIA" + v, v
	})
	add("Provider tokens", "Stripe sk_live_", func(r *rand.Rand) (string, string) { v := hi(r, 24); return "sk_live_" + v, v })
	add("Provider tokens", "npm_", func(r *rand.Rand) (string, string) { v := hi(r, 36); return "npm_" + v, v })
	add("Provider tokens", "JWT", func(r *rand.Rand) (string, string) {
		v := randToken(r, 43, alnum+"-_")
		return "eyJhbGciOiJIUzI1NiJ9." + randToken(r, 40, alnum+"-_") + "." + v, v
	})

	// Authorization header values.
	add("Authorization", "Authorization: Bearer", func(r *rand.Rand) (string, string) { v := hi(r, 40); return "Authorization: Bearer " + v, v })
	add("Authorization", "Authorization: Basic", func(r *rand.Rand) (string, string) {
		v := randToken(r, 24, alnum+"+/") + "=="
		return "Authorization: Basic " + v, v
	})

	// Secret-named values (high entropy).
	add("Secret-named values", "<name>=VALUE", func(r *rand.Rand) (string, string) {
		n := secretNames[r.Intn(len(secretNames))]
		v := hi(r, 20)
		return n + "=" + v, v
	})
	add("Secret-named values", `<name>: "VALUE"`, func(r *rand.Rand) (string, string) {
		n := secretNames[r.Intn(len(secretNames))]
		v := hi(r, 20)
		return n + `: "` + v + `"`, v
	})

	// URL passwords on a realistic host.
	add("URL passwords", "scheme://user:PW@host", func(r *rand.Rand) (string, string) {
		schemes := []string{"postgres", "mysql", "redis", "mongodb", "amqp", "https"}
		pw := hi(r, 16)
		return schemes[r.Intn(len(schemes))] + "://app:" + pw + "@" + randHost(r) + "/db", pw
	})

	// Query parameters: a secret-named parameter followed by another (Security's shape).
	add("Query parameters", "?token=SECRET&next=... (followed by another)", func(r *rand.Rand) (string, string) {
		v := hi(r, 32)
		return "https://" + randHost(r) + "/callback?token=" + v + "&next=%2Fhome&lang=en", v
	})
	add("Query parameters", "?api_key=SECRET&format=json", func(r *rand.Rand) (string, string) {
		v := hi(r, 32)
		return "GET https://" + randHost(r) + "/v1/data?api_key=" + v + "&format=json HTTP/1.1", v
	})
	add("Query parameters", "?x=1&access_token=SECRET&y=2 (mid-string)", func(r *rand.Rand) (string, string) {
		v := hi(r, 40)
		return "https://" + randHost(r) + "/oauth?x=1&access_token=" + v + "&y=2", v
	})

	// Private keys generated in process.
	add("Private keys", "EC private key (P-256, in-process)", func(r *rand.Rand) (string, string) {
		p := ecKeyPEM(r)
		body := strings.TrimSpace(strings.SplitN(strings.SplitN(p, "-----\n", 2)[1], "\n-----", 2)[0])
		return p, body
	})
	add("Private keys", "ed25519 private key (PKCS#8, in-process)", func(r *rand.Rand) (string, string) {
		p := ed25519KeyPEM(r)
		body := strings.TrimSpace(strings.SplitN(strings.SplitN(p, "-----\n", 2)[1], "\n-----", 2)[0])
		return p, body
	})

	// The example-host case: a URL password on example.com, to document whether a placeholder
	// host is treated differently from the realistic-host URL-password shape above (why fixtures
	// use acme-corp.io, never example.com).
	add("Example host", "URL password on example.com", func(r *rand.Rand) (string, string) {
		pw := hi(r, 16)
		return "postgres://app:" + pw + "@db.example.com/x", pw
	})

	return s
}

// writeRandomGapTable runs every shape runtimeRandomRuns times and appends a table of how many
// values each rule source redacted. Folded into TestGapReport's report.
func writeRandomGapTable(t *testing.T, b *strings.Builder, lib redact.Redactor) {
	t.Helper()
	seed := time.Now().UnixNano()
	r := rand.New(rand.NewSource(seed))
	fmt.Fprintf(b, "\n## Comparison over %d runtime-random values per shape (seed %d)\n\n", runtimeRandomRuns, seed)
	fmt.Fprintf(b, "| Category | Shape | Old rules redact | betterleaks %s redacts |\n|---|---|---|---|\n", Version)
	type key struct{ cat, label string }
	type tally struct{ old, lib int }
	counts := map[key]*tally{}
	var order []key
	shown := func(red redact.Redactor, text, secret string) bool {
		out, err := red.Text([]byte(text))
		if err != nil {
			return true // could not check: treat as shown (fail-closed view), recorded not failed
		}
		return strings.Contains(string(out), secret)
	}
	for _, sh := range gapShapes() {
		k := key{sh.category, sh.label}
		if counts[k] == nil {
			counts[k] = &tally{}
			order = append(order, k)
		}
		for i := 0; i < runtimeRandomRuns; i++ {
			text, secret := sh.gen(r)
			if secret == "" || text == "" {
				continue
			}
			if !shown(redact.Rules, text, secret) {
				counts[k].old++
			}
			if !shown(lib, text, secret) {
				counts[k].lib++
			}
		}
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i].cat != order[j].cat {
			return order[i].cat < order[j].cat
		}
		return order[i].label < order[j].label
	})
	for _, k := range order {
		tl := counts[k]
		fmt.Fprintf(b, "| %s | %s | %d/%d | %d/%d |\n",
			k.cat, strings.ReplaceAll(k.label, "|", "\\|"), tl.old, runtimeRandomRuns, tl.lib, runtimeRandomRuns)
	}
}
