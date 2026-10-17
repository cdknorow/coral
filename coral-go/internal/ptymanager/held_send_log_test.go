package ptymanager

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// #4112 part 2: a held send failing again and again (a pane that is gone) warns once, then once
// per window with the count; another session's or another error's failure is its own line.
func TestHeldSendFailuresAreCoalesced(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	at := time.Now()
	heldSendFailures.SetClock(func() time.Time { return at })
	t.Cleanup(func() { heldSendFailures.SetClock(time.Now) })

	gone := errors.New("the agent's terminal is gone")
	for i := 0; i < 100; i++ {
		logHeldSendFailure("claude-held-4112", "board nudge", gone)
	}
	assert.Equal(t, 1, strings.Count(buf.String(), "a held send failed"), "one line for 100 identical failures")
	logHeldSendFailure("claude-other-4112", "board nudge", gone)
	logHeldSendFailure("claude-held-4112", "board nudge", errors.New("another error"))
	assert.Equal(t, 3, strings.Count(buf.String(), "a held send failed"), "another session's and another error's are their own")

	at = at.Add(5 * time.Minute)
	logHeldSendFailure("claude-held-4112", "board nudge", gone)
	assert.Contains(t, buf.String(), "more_like_it=99")
}
