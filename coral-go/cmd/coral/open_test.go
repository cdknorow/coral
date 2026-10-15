package main

import (
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
)

// #3532: `coral open --path` refuses anything but a page of this dashboard before it asks
// Coral for a link (loginlink.ValidPage and OnPage are tested in their package).
func TestOpenPathRefused(t *testing.T) {
	for _, bad := range []string{"chats", "//evil.example/x", "https://evil.example/", "/chats#x"} {
		assert.Equal(t, 2, runOpen([]string{"--print", "--path", bad}, io.Discard, io.Discard), bad)
	}
}
