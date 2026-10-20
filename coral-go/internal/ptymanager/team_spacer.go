package ptymanager

import (
	"context"
	"errors"
	"hash/fnv"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"
)

// The per-team spacer (#3613 B, GitHub issue #25): Coral's own fan-outs (the board notifier's
// pass, wait-resolved and unblocked-task loops, team launch, reset, resurrect, Wake and Wake All)
// used to type into, or start, every agent of a team in the same instant, so their API turns
// started together against one account's rate limit. Consecutive automated sends and launches to
// agents of the same team are now at least TeamGap (2 s) plus up to TeamJitter (1 s) apart.
//   - The first one is never delayed; a team of one waits nothing (QA).
//   - Only automated sends and launches are spaced: the Operator's input, keys and kill never go
//     through here (Security).
//   - Coral never touches the CLIs' own API calls: this only decides when Coral types or launches.
//   - A send never waits more than MaxTeamWait (Security's SHOULD 2): beyond it the gate holds it
//     ("not now") and its loop tries again, so a burst never parks minutes of sends.
//   - A launch or wake never makes its request wait (Security's MUST): its terminal is created at
//     once and only the CLI's start command is typed when the slot comes (ReserveTeamStart).

const (
	TeamGap     = 2 * time.Second
	TeamJitter  = time.Second
	MaxTeamWait = 30 * time.Second
	// MaxStartWait: an agent's start is never typed more than this after its launch or wake;
	// beyond it the start goes now (Security's NIT: a flood of launches can't hold agents in an
	// idle shell for minutes).
	MaxStartWait = 60 * time.Second
)

// ErrTeamBusy: the team's next send slot is more than MaxTeamWait away; not now.
var ErrTeamBusy = errors.New("the team's automated sends are spaced: not now")

type teamSpacer struct {
	mu    sync.Mutex
	next  map[string]time.Time // the earliest time the team's next send may go
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) bool
	gap   func() time.Duration
}

// spacerP is the process's spacer; tests swap it (setSpacer, team_spacer_test.go) while a goroutine of an earlier test may still
// be reading it, so it is read through spacer().
var spacerP atomic.Pointer[teamSpacer]

func init() { spacerP.Store(newTeamSpacer()) }

func spacer() *teamSpacer { return spacerP.Load() }

func newTeamSpacer() *teamSpacer {
	return &teamSpacer{
		next: map[string]time.Time{},
		now:  time.Now,
		sleep: func(ctx context.Context, d time.Duration) bool {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return false
			case <-t.C:
				return true
			}
		},
		gap: func() time.Duration { return TeamGap + time.Duration(rand.Int64N(int64(TeamJitter))) },
	}
}

// reserve takes the team's next slot and returns how long to wait for it (0 for the first).
// With max > 0, a slot further away than max isn't taken: ok is false.
func (s *teamSpacer) reserve(team string, max time.Duration) (wait time.Duration, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	at := now
	if t, ok := s.next[team]; ok && t.After(now) {
		at = t
	}
	if max > 0 && at.Sub(now) > max {
		return at.Sub(now), false
	}
	s.next[team] = at.Add(s.gap())
	// Forget teams that have been quiet a while (the map stays small).
	for k, t := range s.next {
		if now.Sub(t) > time.Minute {
			delete(s.next, k)
		}
	}
	return at.Sub(now), true
}

// SpaceTeam waits for team's next automated send slot. team "" (an agent on no board) never
// waits. It returns ErrTeamBusy, without waiting, when the slot is over MaxTeamWait away, and
// ctx's error when ctx ended first.
func SpaceTeam(ctx context.Context, team string) error {
	if team == "" {
		return ctx.Err()
	}
	d, ok := spacer().reserve(team, MaxTeamWait)
	if !ok {
		return ErrTeamBusy
	}
	if d > 0 && !spacer().sleep(ctx, d) {
		return ctx.Err()
	}
	return ctx.Err()
}

// ReserveTeamStart takes team's next slot for an agent's start and returns how long from now
// it comes; the caller types the start command then, in the background. Never refused: a
// launched agent must start. A slot over MaxStartWait away isn't taken and the start goes now
// (0). team "": 0.
func ReserveTeamStart(team string) time.Duration {
	if team == "" {
		return 0
	}
	if d, ok := spacer().reserve(team, MaxStartWait); ok {
		return d
	}
	return 0
}

// The team an agent session belongs to, for spacing automated sends (the server registers it:
// the board the session is subscribed to). Unset or "": not spaced.
var (
	teamOfMu sync.RWMutex
	teamOf   func(sessionName string) string
)

// SetTeamResolver registers how to find an agent session's team.
func SetTeamResolver(f func(sessionName string) string) {
	teamOfMu.Lock()
	teamOf = f
	teamOfMu.Unlock()
}

func teamFor(sessionName string) string {
	teamOfMu.RLock()
	f := teamOf
	teamOfMu.RUnlock()
	if f == nil {
		return ""
	}
	return f(sessionName)
}

// JitterAround is d scaled by a factor from 0.8 to 1.2 that stays the same for one key (#3613):
// reminders and re-nudges that would all fire together (they were set, or restored, in the same
// instant) are spread out instead. Never 0 and never more than 1.2 d.
func JitterAround(d time.Duration, key string) time.Duration {
	h := fnv.New64a()
	h.Write([]byte(key))
	frac := float64(h.Sum64()%10001) / 10000 // 0..1
	return time.Duration(float64(d) * (0.8 + 0.4*frac))
}
