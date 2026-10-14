package auth

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// #3418: the strict-mode locked page explains how to get in and never asks for a key.
func TestLockedPage(t *testing.T) {
	p := LockedPageHTML
	assert.Contains(t, p, "Coral is locked")
	assert.Contains(t, p, "coral open", "tells how to get in from a terminal")
	assert.Contains(t, p, "tray", "and from the app or tray")
	assert.Contains(t, p, `name="viewport"`, "works on phones")
	assert.Contains(t, p, "prefers-color-scheme: dark", "light and dark")
	assert.Contains(t, p, `name="coral-locked"`, "machine-detectable")
	low := strings.ToLower(p)
	for _, never := range []string{"<input", "<form", "<script", "api_key", "api-key", "api key"} {
		assert.NotContains(t, low, never, "never asks for or shows a key, and runs no script: "+never)
	}
}
