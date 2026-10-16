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
// password. An ordinary line is written as it is.
func TestLogLinesAreRedacted(t *testing.T) {
	var file, term bytes.Buffer
	out, terminal := logOutput(&file, &term)
	secret := "2026/10/16 [launch] tmux session=claude-1 agent=claude cmd=claude --api-key=sk-ant-abcdefghijklmnopqrstuvwxyz0123 --mcp postgres://u:pw1234@db/x\n"
	plain := "2026/10/16 [launch] tmux session=claude-2 agent=claude cmd=claude --model opus\n"
	for _, l := range []string{secret, plain} {
		n, err := out.Write([]byte(l))
		assert.NoError(t, err)
		assert.Equal(t, len(l), n)
	}
	terminal.Flush(time.Second)
	for _, b := range []*bytes.Buffer{&file, &term} {
		got := b.String()
		for _, s := range []string{"sk-ant-abcdefghijklmnopqrstuvwxyz0123", "pw1234"} {
			assert.NotContains(t, got, s)
		}
		assert.True(t, strings.HasSuffix(got, plain), "an ordinary line is unchanged")
	}
}

// #3783: a line in which a private key starts (the redactor drops the rest of it) keeps its
// newline, so the next line doesn't run on after it.
func TestLogLineKeepsItsNewlineAfterAKeyStart(t *testing.T) {
	got := redactLogLine([]byte("[agent] printed -----BEGIN RSA PRIVATE KEY-----MIIEow\n"))
	assert.True(t, bytes.HasSuffix(got, []byte("\n")), "%q", got)
	assert.NotContains(t, string(got), "MIIEow")
	assert.Equal(t, []byte("plain\n"), redactLogLine([]byte("plain\n")))
	assert.Equal(t, []byte("no newline"), redactLogLine([]byte("no newline")))
}
