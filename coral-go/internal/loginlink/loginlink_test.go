package loginlink

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOr(t *testing.T) {
	base := "http://localhost:8420"
	assert.Equal(t, base, Or(base, nil), "no minter: the plain URL")
	assert.Equal(t, base, Or(base, func() (string, error) { return "", errors.New("older coral") }), "a failure: the plain URL")
	link := base + "/?login_token=abc"
	assert.Equal(t, link, Or(base, func() (string, error) { return link, nil }))
}

func TestRedactAndHasToken(t *testing.T) {
	link := "http://localhost:8420/?view=team&login_token=s3cret"
	r := Redact(link)
	assert.NotContains(t, r, "s3cret")
	assert.Contains(t, r, "login_token=REDACTED")
	assert.Contains(t, r, "view=team")
	assert.Equal(t, "http://localhost:8420/", Redact("http://localhost:8420/"))
	assert.True(t, HasToken(link))
	assert.False(t, HasToken("http://localhost:8420/"))
}

// Only the Coral on this computer is ever asked (the command-line token proves the same user).
func TestFromCLIOnlyAsksThisComputer(t *testing.T) {
	for _, base := range []string{"http://example.com:8420", "http://192.168.1.5:8420", "ftp://localhost", "localhost:8420"} {
		_, err := FromCLI(context.Background(), base, t.TempDir())
		assert.ErrorIs(t, err, ErrNotLocal, base)
	}
	for _, base := range []string{"http://localhost:8420", "http://127.0.0.1:1", "https://[::1]:8420"} {
		assert.True(t, local(base), base)
	}
}
