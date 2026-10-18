package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPrintMentionWarnings(t *testing.T) {
	var out bytes.Buffer
	printMentionWarnings(&out, map[string]any{"id": 5.0, "warnings": []any{"no subscriber named 'DO Expert'; did you mean 'Digital Ocean Expert'?", "no subscriber named 'Zed'", ""}})
	assert.Equal(t, "Warning: no subscriber named 'DO Expert'; did you mean 'Digital Ocean Expert'?\nWarning: no subscriber named 'Zed'\n", out.String())
	out.Reset()
	printMentionWarnings(&out, map[string]any{"id": 5.0})
	assert.Empty(t, out.String())
	printMentionWarnings(&out, map[string]any{"warnings": "not a list"})
	assert.Empty(t, out.String())
}
