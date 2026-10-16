package tunnel

import "strings"

// Fake tokens are built at run time (prefix + filler), so the repository holds nothing that
// looks like a real secret (the same helper as pkg/redact's tests).
func fake(prefix string, n int, alphabet string) string {
	var b strings.Builder
	b.WriteString(prefix)
	for i := 0; i < n; i++ {
		b.WriteByte(alphabet[(i*7+3)%len(alphabet)])
	}
	return b.String()
}

const (
	alnum = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	upper = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
)

func pemKey(label string, lines int, nl string) string {
	var b strings.Builder
	b.WriteString("-----BEGIN " + label + "PRIVATE KEY-----" + nl)
	for i := 0; i < lines; i++ {
		b.WriteString(fake("", 64, alnum+"+/") + nl)
	}
	b.WriteString("-----END " + label + "PRIVATE KEY-----")
	return b.String()
}
