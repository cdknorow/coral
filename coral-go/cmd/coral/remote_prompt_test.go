package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// #3355: the first-launch remote-access question says plainly that it is unencrypted,
// unless Coral was started with TLS.
func TestPromptRemoteAccessWarnsAboutCleartext(t *testing.T) {
	var out bytes.Buffer
	assert.True(t, promptRemoteAccess(strings.NewReader("y\n"), &out, false))
	o := out.String()
	assert.Contains(t, o, "Warning: remote access is plain HTTP, NOT encrypted.")
	assert.Contains(t, o, "Coral Hub (encrypted), a TLS")
	assert.Contains(t, o, "--tls-cert and --tls-key")

	out.Reset()
	assert.False(t, promptRemoteAccess(strings.NewReader("\n"), &out, true))
	assert.NotContains(t, out.String(), "NOT encrypted")
	assert.Contains(t, out.String(), "They connect over HTTPS")
}
