package routes

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// #4028: a login-screen read is reused while the agent's terminal wrote nothing (its output
// stamp is the same), not re-read every loginScreenTTL with a capture-pane; a new stamp, no stamp
// past the TTL, or loginScreenStampMax always reads again. #5838: the first reading of a stamp is read once more after the TTL before it is reused for long (the stamp can hold while
// the screen changes within its second).
func TestLoginScreenReadReusedWhileOutputIsUnchanged(t *testing.T) {
	var l loginScreens
	reads := 0
	fn := func() (bool, string) { reads++; return false, "" }
	t0 := time.Unix(1_700_000_000, 0)
	l.read("a", "s1", t0, fn)
	assert.Equal(t, 1, reads)
	l.read("a", "s1", t0.Add(loginScreenTTL+time.Second), fn)
	assert.Equal(t, 2, reads, "the first reading of this stamp is confirmed once, after the TTL")
	l.read("a", "s1", t0.Add(2*loginScreenTTL+2*time.Second), fn)
	assert.Equal(t, 2, reads, "confirmed, and nothing written since: no new capture")
	t2 := t0.Add(2*loginScreenTTL + 3*time.Second)
	l.read("a", "s2", t2, fn)
	assert.Equal(t, 3, reads, "new output: read again")
	l.read("a", "s2", t2.Add(loginScreenTTL+time.Second), fn)
	assert.Equal(t, 4, reads, "its first reading is confirmed after the TTL too")
	l.read("a", "s2", t2.Add(loginScreenStampMax-time.Second), fn)
	assert.Equal(t, 4, reads, "confirmed, same output, within the bound")
	l.read("a", "s2", t2.Add(loginScreenTTL+time.Second+loginScreenStampMax), fn)
	assert.Equal(t, 5, reads, "the stamp's reuse is bounded")

	l.read("b", "", t0, fn)
	l.read("b", "", t0.Add(time.Second), fn)
	assert.Equal(t, 6, reads, "without a stamp, the TTL still applies")
	l.read("b", "", t0.Add(loginScreenTTL+time.Second), fn)
	assert.Equal(t, 7, reads, "and past it, a stamp-less read is never matched")
}

// #5838 (UI/UX saw about a minute before the sign-in card): the first read of a new agent can be taken on the splash, and the login menu drawn in the same second leaves the
// output stamp unchanged. That "not a login" reading must not be reused for a minute: it is read again after the TTL and the card shows within a few seconds.
func TestALoginMenuDrawnUnderTheSameStampIsFoundAfterTheTTLNotAMinute(t *testing.T) {
	var l loginScreens
	menuDrawn := false
	fn := func() (bool, string) { return menuDrawn, "" }
	t0 := time.Unix(1_700_000_000, 0)
	on, _ := l.read("a", "same-second", t0, fn)
	assert.False(t, on, "the splash")
	menuDrawn = true // the menu appears; the stamp does not move
	on, _ = l.read("a", "same-second", t0.Add(time.Second), fn)
	assert.False(t, on, "within the TTL the reading is reused")
	on, _ = l.read("a", "same-second", t0.Add(loginScreenTTL+time.Second), fn)
	assert.True(t, on, "after the TTL it is read again: found within seconds, not after loginScreenStampMax")
	assert.Less(t, loginScreenTTL+time.Second, 10*time.Second)
}
