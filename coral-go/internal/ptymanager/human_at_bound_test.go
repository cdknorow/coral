package ptymanager

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// #3999 (finding 5): humanAt forgets sessions nobody typed in for a long time, so it does not
// grow with every session name ever typed in.
func TestHumanAtIsBounded(t *testing.T) {
	m := &PTYBackend{humanAt: map[string]time.Time{}}
	old := time.Now().Add(-2 * humanAtForgetAfter)
	for i := 0; i < humanAtKeysMax+50; i++ {
		m.humanAt[fmt.Sprintf("old-%d", i)] = old
	}
	_ = m.SendHumanInput("now", []byte("x")) // the session doesn't exist: the error is not the point
	assert.Len(t, m.humanAt, 1, "the stale entries went, the fresh one stayed")
	assert.True(t, m.HumanInputSince("now", time.Now().Add(-time.Minute)))
}
