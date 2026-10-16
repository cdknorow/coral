package ptymanager

import (
	"context"
	"hash/fnv"
	"math/rand/v2"
	"sync"
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

const (
	TeamGap    = 2 * time.Second
	TeamJitter = time.Second
)

type teamSpacer struct {
	mu    sync.Mutex
	next  map[string]time.Time // the earliest time the team's next send may go
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) bool
	gap   func() time.Duration
}

var spacer = newTeamSpacer()

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
func (s *teamSpacer) reserve(team string) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	at := now
	if t, ok := s.next[team]; ok && t.After(now) {
		at = t
	}
	s.next[team] = at.Add(s.gap())
	// Forget teams that have been quiet a while (the map stays small).
	for k, t := range s.next {
		if now.Sub(t) > time.Minute {
			delete(s.next, k)
		}
	}
	return at.Sub(now)
}

// SpaceTeam waits for team's next send or launch slot. team "" (an agent on no board) never
// waits. It reports false when ctx ended first.
func SpaceTeam(ctx context.Context, team string) bool {
	if team == "" {
		return ctx.Err() == nil
	}
	if d := spacer.reserve(team); d > 0 {
		return spacer.sleep(ctx, d)
	}
	return ctx.Err() == nil
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
