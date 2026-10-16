// Package logdedupe keeps a repeated log line from filling the log (#4112, Security's #4107
// audit: 100 failed sends to a gone pane gave 200 identical lines). The first line with a key is
// logged; the same key within the window is counted instead, and the next one after the window is
// logged with how many were held back.
package logdedupe

import (
	"sync"
	"time"
)

// keysMax bounds the keys kept; past it, the record starts over (each key then logs once more).
const keysMax = 1024

// Limiter decides, per key, which repeats of a line are logged.
type Limiter struct {
	every time.Duration
	now   func() time.Time

	mu sync.Mutex
	m  map[string]*entry
}

type entry struct {
	logged     time.Time // when a line with this key was last logged
	suppressed int       // repeats held back since then
}

// New returns a Limiter that logs a key at most once per every.
func New(every time.Duration) *Limiter {
	return &Limiter{every: every, now: time.Now, m: map[string]*entry{}}
}

// Allow reports whether a line with key is logged now, and, when it is, how many lines with the
// key were held back since the last one logged (for "N more like this").
func (l *Limiter) Allow(key string) (log bool, heldBack int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	e, ok := l.m[key]
	if !ok {
		if len(l.m) >= keysMax {
			l.m = map[string]*entry{}
		}
		l.m[key] = &entry{logged: now}
		return true, 0
	}
	if now.Sub(e.logged) < l.every {
		e.suppressed++
		return false, 0
	}
	heldBack = e.suppressed
	e.logged, e.suppressed = now, 0
	return true, heldBack
}

// SetClock replaces the Limiter's clock (tests).
func (l *Limiter) SetClock(now func() time.Time) {
	l.mu.Lock()
	l.now = now
	l.mu.Unlock()
}
