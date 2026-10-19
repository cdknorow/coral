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

// #5124: the first-launch question defaults to no: only "y" or "yes" turns remote access on; an empty Enter, a closed input and anything else leave it off.
func TestPromptRemoteAccessDefaultsToNo(t *testing.T) {
	for _, in := range []string{"", "\n", "n\n", "no\n", "maybe\n", " \n", "yy\n", "true\n", "1\n"} {
		var out bytes.Buffer
		assert.False(t, promptRemoteAccess(strings.NewReader(in), &out, false), "input %q", in)
		assert.Contains(t, out.String(), "[y/N]")
		assert.Contains(t, out.String(), "Remote access disabled.")
	}
	for _, in := range []string{"y\n", "Y\n", "yes\n", " YES \n"} {
		assert.True(t, promptRemoteAccess(strings.NewReader(in), &bytes.Buffer{}, false), "input %q", in)
	}
}
