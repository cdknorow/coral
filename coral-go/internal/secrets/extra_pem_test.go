package secrets

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// betterleaks' global filter drops a finding whose text holds "false" in any case (meant for a value that is only true, false or null), so a
// private key block with those letters in its base64 was shown whole. Found by QA (#5158): about one in twenty thousand random blocks.
func TestPrivateKeyBlockWithFalseInItsBodyIsRedacted(t *testing.T) {
	require.NoError(t, Init())
	filler := strings.Repeat("Zq8Lm2Xv9Rt4Wk7Pd3Hs+/Ab5Cd6Ef7Gh8Ij9Kl0Mn1Op2Qr3St4Uv5Wx6Yz7", 5)
	for _, word := range []string{"false", "FALSE", "FaLsE"} {
		body := filler + word + filler
		block := "-----BEGIN OPENSSH " + "PRIVATE KEY-----\n" + body + "\n-----END OPENSSH " + "PRIVATE KEY-----"
		for name, text := range map[string]string{"alone": block, "in a line": "key follows\n" + block + "\nafter", "in prose": "here: " + block + " done"} {
			for who, rd := range map[string]interface{ Text([]byte) ([]byte, error) }{"Redactor": Redactor(), "hub": hubRedactor()} {
				out, err := rd.Text([]byte(text))
				require.NoError(t, err)
				assert.NotContains(t, string(out), filler[:40], "%s, %s, %s", who, name, word)
			}
		}
	}
	// Text around the block stays.
	out, _ := Redactor().Text([]byte("before\n-----BEGIN RSA " + "PRIVATE KEY-----\n" + filler + "false" + filler + "\n-----END RSA " + "PRIVATE KEY-----\nafter"))
	assert.True(t, strings.HasPrefix(string(out), "before\n") && strings.HasSuffix(string(out), "\nafter"), string(out))
}
