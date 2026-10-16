package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// #3763 (secrets audit #3762 findings 3 and 4): every line coral.log and the terminal get is
// redacted: an agent's launch line with a key in its flags, an error quoting a URL with a
// password. An ordinary line is written as it is. Since #3808 the rules are betterleaks'
// (internal/secrets): these secrets are ones it finds.
func TestLogLinesAreRedacted(t *testing.T) {
	var file, term bytes.Buffer
	out, terminal := logOutput(&file, &term)
	key := "sk-ant-api03-" + strings.Repeat("Xk29dLq7Pz", 9) + "Vb3" + "AA" // 93 characters, then AA
	pw := "Zq8" + "rT2vLm9XkP4sWn7B"
	secret := "2026/10/16 [launch] tmux session=claude-1 agent=claude cmd=claude ANTHROPIC_API_KEY=" + key + " --mcp postgres://u:" + pw + "@db/x\n"
	plain := "2026/10/16 [launch] tmux session=claude-2 agent=claude cmd=claude --model opus\n"
	for _, l := range []string{secret, plain} {
		n, err := out.Write([]byte(l))
		assert.NoError(t, err)
		assert.Equal(t, len(l), n)
	}
	terminal.Flush(time.Second)
	for _, b := range []*bytes.Buffer{&file, &term} {
		got := b.String()
		for _, s := range []string{key, pw} {
			assert.NotContains(t, got, s)
		}
		assert.True(t, strings.HasSuffix(got, plain), "an ordinary line is unchanged")
	}
}

// #3783: a line keeps its newline whatever was redacted in it, so the next line doesn't run on.
func TestLogLineKeepsItsNewline(t *testing.T) {
	pw := "Zq8" + "rT2vLm9XkP4sWn7B"
	got := redactLogLine([]byte("[agent] dialled postgres://u:" + pw + "@db/x\n"))
	assert.True(t, bytes.HasSuffix(got, []byte("\n")), "%q", got)
	assert.NotContains(t, string(got), pw)
	assert.Equal(t, []byte("plain\n"), redactLogLine([]byte("plain\n")))
	assert.Equal(t, []byte("no newline"), redactLogLine([]byte("no newline")))
}
