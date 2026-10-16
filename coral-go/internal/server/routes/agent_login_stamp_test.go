package routes

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// #4028: a login-screen read is reused while the agent's terminal wrote nothing (its output
// stamp is the same), not re-read every loginScreenTTL with a capture-pane; a new stamp, no stamp
// past the TTL, or loginScreenStampMax always reads again.
func TestLoginScreenReadReusedWhileOutputIsUnchanged(t *testing.T) {
	var l loginScreens
	reads := 0
	fn := func() (bool, string) { reads++; return false, "" }
	t0 := time.Unix(1_700_000_000, 0)
	l.read("a", "s1", t0, fn)
	assert.Equal(t, 1, reads)
	l.read("a", "s1", t0.Add(loginScreenTTL+time.Second), fn)
	assert.Equal(t, 1, reads, "nothing written since: no new capture")
	t2 := t0.Add(loginScreenTTL + 2*time.Second)
	l.read("a", "s2", t2, fn)
	assert.Equal(t, 2, reads, "new output: read again")
	l.read("a", "s2", t2.Add(loginScreenStampMax-time.Second), fn)
	assert.Equal(t, 2, reads, "same output, within the bound")
	l.read("a", "s2", t2.Add(loginScreenStampMax), fn)
	assert.Equal(t, 3, reads, "the stamp's reuse is bounded")

	l.read("b", "", t0, fn)
	l.read("b", "", t0.Add(time.Second), fn)
	assert.Equal(t, 4, reads, "without a stamp, the TTL still applies")
	l.read("b", "", t0.Add(loginScreenTTL+time.Second), fn)
	assert.Equal(t, 5, reads, "and past it, a stamp-less read is never matched")
}
