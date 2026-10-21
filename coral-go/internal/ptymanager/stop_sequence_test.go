package ptymanager

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

type stopRecorder struct {
	mu sync.Mutex
	w  [][]byte
}

func (r *stopRecorder) write(b []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.w = append(r.w, append([]byte(nil), b...))
	return nil
}

func (r *stopRecorder) writes() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, b := range r.w {
		out = append(out, string(b))
	}
	return out
}

var fastStop = stopTimings{secondAfter: 20 * time.Millisecond, poll: 5 * time.Millisecond, limit: 300 * time.Millisecond}

// #68 (audit F12): a stop that is one Ctrl-C leaves a raw-mode CLI waiting out the whole grace. The second Ctrl-C follows,
// and "exit" only once nothing is running under the shell.
func TestPoliteStopSendsASecondCtrlCThenExitForAnIdleShell(t *testing.T) {
	var r stopRecorder
	done := make(chan struct{})
	politeStop(r.write, done, func() (bool, error) { return false, nil }, fastStop)
	assert.Equal(t, []string{"\x03", "exit\r"}, r.writes())
}

func TestPoliteStopNeverTypesExitWhileAProgramIsRunning(t *testing.T) {
	var r stopRecorder
	done := make(chan struct{})
	start := time.Now()
	politeStop(r.write, done, func() (bool, error) { return true, nil }, fastStop)
	assert.Equal(t, []string{"\x03"}, r.writes(), "exit would be typed into the agent's input")
	assert.GreaterOrEqual(t, time.Since(start), fastStop.limit, "it waited out its limit")
	// A process list that can't be read is not an idle shell either.
	var r2 stopRecorder
	politeStop(r2.write, done, func() (bool, error) { return false, errors.New("snapshot") }, fastStop)
	assert.Equal(t, []string{"\x03"}, r2.writes())
}

func TestPoliteStopStopsWhenTheProcessIsGone(t *testing.T) {
	var r stopRecorder
	done := make(chan struct{})
	close(done) // exited on the first Ctrl-C
	politeStop(r.write, done, func() (bool, error) { return false, nil }, fastStop)
	assert.Empty(t, r.writes(), "no second Ctrl-C for a process that already went")

	// Gone after the second.
	var r2 stopRecorder
	done2 := make(chan struct{})
	go func() { time.Sleep(fastStop.secondAfter + 10*time.Millisecond); close(done2) }()
	politeStop(r2.write, done2, func() (bool, error) { return true, nil }, fastStop)
	assert.Equal(t, []string{"\x03"}, r2.writes())
}
