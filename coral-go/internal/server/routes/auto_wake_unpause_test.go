package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/board/boardtest"
	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/store"
)

// #286 (QA's #278): after Coral restarts, auto-wake wakes a team's agents and resumes the board that Coral paused at startup for
// them, as the dashboard's Wake does; the woken agents then read what was posted meanwhile. A pause a person set with Pause
// before the restart is kept (#3668).

// restartBoardHandler is a board handler as Coral starts: the stored pauses loaded, then each board with sleeping agents paused
// for them (server.go's RestoreSleepingBoards).
func restartBoardHandler(t *testing.T, r *wakeRig) *BoardHandler {
	t.Helper()
	ctx := context.Background()
	bh := NewBoardHandler(r.h.bs)
	t.Cleanup(bh.WaitBackground)
	bh.LoadPaused(ctx)
	boards, err := r.ss.GetSleepingBoardNames(ctx)
	require.NoError(t, err)
	for _, b := range boards {
		bh.PauseForSleep(b)
	}
	r.h.SetBoardHandler(bh)
	return bh
}

// teamAsleepAfterCrash: one agent of team, on its board, left sleeping because Coral stopped, and a teammate reading everything.
func teamAsleepAfterCrash(t *testing.T, r *wakeRig, team string) {
	t.Helper()
	ctx := context.Background()
	r.add(sid(1), "Dev", store.SleepReasonCoralStopped, "", false)
	_, err := r.h.db.ExecContext(ctx, "UPDATE live_sessions SET board_name = ? WHERE session_id = ?", team, sid(1))
	require.NoError(t, err)
	_, err = r.h.bs.Subscribe(ctx, team, naming.SubscriberID("Dev", "claude"), "Dev", naming.SessionName("claude", sid(1)), nil, "all")
	require.NoError(t, err)
	_, err = r.h.bs.Subscribe(ctx, team, "orch-sub", "Orchestrator", "claude-orchestrator", nil, "all")
	require.NoError(t, err)
}

// readAs is GET /api/board/{team}/messages for subscriber, as coral-board read asks.
func readAs(t *testing.T, bh *BoardHandler, team, subscriber string) []board.Message {
	t.Helper()
	rt := chi.NewRouter()
	rt.Get("/api/board/{project}/messages", bh.ReadMessages)
	srv := httptest.NewServer(rt)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/board/" + team + "/messages?subscriber_id=" + subscriber)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var msgs []board.Message
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&msgs))
	return msgs
}

func TestAutoWakeResumesTheBoardCoralPausedForIt(t *testing.T) {
	const team = "team-286"
	ctx := context.Background()
	r := newWakeRig(t)
	teamAsleepAfterCrash(t, r, team)

	bh := restartBoardHandler(t, r)
	require.True(t, bh.IsPaused(team), "Coral pauses the board of its sleeping agents at startup")

	r.h.AutoWakeAfterRestart(ctx)

	sleeping, _ := r.asleep(sid(1))
	require.False(t, sleeping, "the agent is woken")
	assert.False(t, bh.IsPaused(team), "the board is resumed with its agent")
	// QA's steps: a teammate posts after the wake, and the woken agent's read returns it with Coral's own notice (a paused
	// board's read returned [], "No new messages").
	_, err := r.h.bs.PostMessage(ctx, team, "orch-sub", "@Dev posted after the wake", nil)
	require.NoError(t, err)
	var texts []string
	for _, m := range readAs(t, bh, team, naming.SubscriberID("Dev", "claude")) {
		texts = append(texts, m.Content)
	}
	assert.Contains(t, texts, "@Dev posted after the wake")
	assert.Contains(t, texts, "@Dev "+AutoWakeNotice)

	// And the resume is stored: the next start finds the board not paused.
	paused, err := r.h.bs.PausedBoardsBy(ctx)
	require.NoError(t, err)
	assert.NotContains(t, paused, team)
}

// A second crash before auto-wake ran: the startup pause was stored as sleep's, so the next start's auto-wake still lifts it.
func TestAutoWakeResumesAStartupPauseStoredByAnEarlierStart(t *testing.T) {
	const team = "team-286b"
	ctx := context.Background()
	r := newWakeRig(t)
	teamAsleepAfterCrash(t, r, team)
	restartBoardHandler(t, r) // the first start pauses the board, then Coral stops again before waking anyone

	bh := restartBoardHandler(t, r)
	require.True(t, bh.IsPaused(team))
	r.h.AutoWakeAfterRestart(ctx)
	assert.False(t, bh.IsPaused(team), "the stored pause is sleep's, so auto-wake lifts it")
}

func TestAutoWakeKeepsAPersonsPause(t *testing.T) {
	const team = "team-286c"
	ctx := context.Background()
	r := newWakeRig(t)
	teamAsleepAfterCrash(t, r, team)
	before := NewBoardHandler(r.h.bs)
	t.Cleanup(before.WaitBackground)
	before.SetPaused(team, true) // the person's Pause, before Coral stopped

	bh := restartBoardHandler(t, r)
	require.True(t, bh.IsPaused(team))
	r.h.AutoWakeAfterRestart(ctx)

	sleeping, _ := r.asleep(sid(1))
	require.False(t, sleeping, "the agent is still woken")
	assert.True(t, bh.IsPaused(team), "the person's Pause stays")
	paused, err := r.h.bs.PausedBoardsBy(ctx)
	require.NoError(t, err)
	assert.Equal(t, "", paused[team], "stored as a person's pause still")
	assert.Empty(t, readAs(t, bh, team, naming.SubscriberID("Dev", "claude")), "a paused board reads nothing, as before")
}

// A board paused before #286 has no "paused_by" row: it counts as a person's pause, so auto-wake keeps it (the safe side).
func TestAPauseStoredBeforeTheReasonCountsAsAPersons(t *testing.T) {
	const team = "team-286d"
	ctx := context.Background()
	r := newWakeRig(t)
	teamAsleepAfterCrash(t, r, team)
	require.NoError(t, r.h.bs.SetBoardPaused(ctx, team, true))

	bh := restartBoardHandler(t, r)
	r.h.AutoWakeAfterRestart(ctx)
	assert.True(t, bh.IsPaused(team))
}

// A person's Pause on a board already paused for sleep makes it the person's; the dashboard's Resume clears both.
func TestPausedByIsKeptStraight(t *testing.T) {
	ctx := context.Background()
	r := newWakeRig(t)
	bh := NewBoardHandler(r.h.bs)
	t.Cleanup(bh.WaitBackground)

	bh.PauseForSleep("b")
	got, err := r.h.bs.PausedBoardsBy(ctx)
	require.NoError(t, err)
	assert.Equal(t, board.PausedBySleep, got["b"])

	bh.SetPaused("b", true)
	got, err = r.h.bs.PausedBoardsBy(ctx)
	require.NoError(t, err)
	assert.Equal(t, "", got["b"], "a person's Pause replaces sleep's")
	assert.False(t, bh.LiftSleepPause("b"))
	assert.True(t, bh.IsPaused("b"))

	bh.PauseForSleep("b") // a sleep on a board the person paused keeps it the person's
	got, err = r.h.bs.PausedBoardsBy(ctx)
	require.NoError(t, err)
	assert.Equal(t, "", got["b"])

	bh.SetPaused("b", false)
	got, err = r.h.bs.PausedBoardsBy(ctx)
	require.NoError(t, err)
	assert.NotContains(t, got, "b")
}

// Security on #119: a person's Pause racing PauseForSleep, or racing LiftSleepPause, always ends as the person's pause, in
// memory and in the store, whichever runs first (each check and set is one step under pauseMu). Run with -race.
func TestAPersonsPauseWinsEitherRace(t *testing.T) {
	ctx := context.Background()
	bs, err := boardtest.Open(t.TempDir() + "/race.db")
	require.NoError(t, err)
	t.Cleanup(func() { bs.Close() })
	bh := NewBoardHandler(bs)
	t.Cleanup(bh.WaitBackground)

	race := func(a, b func()) {
		var wg sync.WaitGroup
		start := make(chan struct{})
		for _, f := range []func(){a, b} {
			wg.Add(1)
			go func(f func()) { defer wg.Done(); <-start; f() }(f)
		}
		close(start)
		wg.Wait()
	}
	for i := 0; i < 50; i++ {
		sleepFirst := fmt.Sprintf("sleep-%d", i)
		race(func() { bh.PauseForSleep(sleepFirst) }, func() { bh.SetPaused(sleepFirst, true) })

		lift := fmt.Sprintf("lift-%d", i)
		bh.PauseForSleep(lift)
		race(func() { bh.LiftSleepPause(lift) }, func() { bh.SetPaused(lift, true) })

		stored, err := bs.PausedBoardsBy(ctx)
		require.NoError(t, err)
		for _, b := range []string{sleepFirst, lift} {
			require.True(t, bh.IsPaused(b), "%s: the person's Pause stays", b)
			require.False(t, bh.LiftSleepPause(b), "%s: it is the person's, not sleep's", b)
			by, ok := stored[b]
			require.True(t, ok, "%s: stored as paused", b)
			require.Equal(t, "", by, "%s: stored as the person's", b)
		}
	}
}
