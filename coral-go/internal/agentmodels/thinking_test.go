package agentmodels

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestThinkingLevelsAndArgs(t *testing.T) {
	assert.Equal(t, []string{"low", "medium", "high", "xhigh", "max"}, ThinkingLevels("claude"))
	assert.Nil(t, ThinkingLevels("codex"), "a type with no documented flag takes no level yet")
	got := ThinkingLevels("claude")
	got[0], got[len(got)-1] = "bogus", "bogus"
	got = append(got, "extra")
	assert.True(t, ValidThinking("claude", "low"), "what a caller does to the returned slice does not change what is accepted")
	assert.False(t, ValidThinking("claude", "bogus"))
	assert.Equal(t, []string{"low", "medium", "high", "xhigh", "max"}, ThinkingLevels("claude"))
	for _, l := range []string{"", "low", "medium", "high", "xhigh", "max"} {
		assert.True(t, ValidThinking("claude", l), l)
	}
	for _, l := range []string{"ultra", "HIGH", "high ", "--yolo", "high --model x", "none", "off"} {
		assert.False(t, ValidThinking("claude", l), "%q", l)
	}
	assert.True(t, ValidThinking("codex", ""), "the default is valid for every type")
	assert.False(t, ValidThinking("codex", "high"))
	assert.False(t, ValidThinking("terminal", "high"))

	assert.Equal(t, []string{"--effort", "xhigh"}, ThinkingArgs("claude", "xhigh"))
	assert.Nil(t, ThinkingArgs("claude", ""))
	assert.Nil(t, ThinkingArgs("claude", "bogus"), "never a flag for a level that was not validated")
	assert.Nil(t, ThinkingArgs("codex", "high"))
}

func TestStripThinkingFlags(t *testing.T) {
	cases := []struct{ in, want []string }{
		{nil, []string{}},
		{[]string{"--verbose"}, []string{"--verbose"}},
		{[]string{"--effort", "high", "--verbose"}, []string{"--verbose"}},
		{[]string{"--verbose", "--effort=low"}, []string{"--verbose"}},
		{[]string{"--model", "opus", "--effort", "max", "--effort", "low"}, []string{"--model", "opus"}},
		{[]string{"--effort"}, []string{}},
		{[]string{"--effort", "--dangerously-skip-permissions", "--verbose"}, []string{"--dangerously-skip-permissions", "--verbose"}},
		{[]string{"--verbose", "--effort", "-p"}, []string{"--verbose", "-p"}},
		{[]string{"--effort", "high", "--effort", "--verbose"}, []string{"--verbose"}},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, StripThinkingFlags(c.in), "%v", c.in)
	}
}
