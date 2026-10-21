package routes

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/pkg/tunnel"
)

// #5514: a relayed sender names its own board and subscriber strings, so the per-(source, sender board, sender, target) limit is dodged by varying them. A limit per source Coral and target
// board, and one per source Coral overall, no string the sender chooses can change.

func (e *artifactEnv) relayedAs(src, target, id, fromBoard, subscriber, claim string) int {
	e.t.Helper()
	b, _ := json.Marshal(map[string]any{"from_board": fromBoard, "subscriber_id": subscriber, "content": "hello", "from_cross_team_id": claim,
		"relay": map[string]any{"message_id": id, "hops": 0}})
	req := httptest.NewRequest("POST", "/api/board/"+target+"/external-messages", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tunnel.HubServerHeader, src)
	req.Header.Set(tunnel.ViaHeader, cloud.ViaSecret())
	rec := httptest.NewRecorder()
	e.r.ServeHTTP(rec, req)
	return rec.Code
}

func TestVaryingTheSendersNamesDoesNotDodgeTheCoarseRelayLimit(t *testing.T) {
	for _, enforced := range []bool{false, true} {
		t.Run(fmt.Sprintf("allowlist in force: %v", enforced), func(t *testing.T) {
			e := newArtifactEnv(t)
			e.open()
			e.enforce(enforced)
			e.h.crossTeam.now = func() time.Time { return fixedNow } // a still clock: no token comes back
			e.list(board.AllowedSender{Server: relaySrc, CrossTeamID: theirID("a")})
			var ok, limited int
			for i := 0; i < 60; i++ { // every message under fresh names: the fine limit never sees the same key twice
				switch e.relayedAs(relaySrc, "beta", fmt.Sprintf("v%d", i), fmt.Sprintf("board-%d", i), fmt.Sprintf("Orchestrator %d", i), theirID("a")) {
				case http.StatusOK:
					ok++
				case http.StatusTooManyRequests:
					limited++
				}
			}
			assert.Equal(t, relayTargetBurst, ok, "30 get through to one board from one Coral, whatever the names")
			assert.Equal(t, 60-relayTargetBurst, limited, "and the rest are limited")
		})
	}
}

// The same names are still limited by the fine limit first (3 at once); a slow legitimate sender is not limited at all.
func TestTheFineLimitStillAppliesAndASlowSenderIsNeverLimited(t *testing.T) {
	e := newArtifactEnv(t)
	e.open()
	e.enforce(false)
	now := fixedNow
	e.h.crossTeam.now = func() time.Time { return now }
	var ok, limited int
	for i := 0; i < 6; i++ {
		switch e.relayedAs(relaySrc, "beta", fmt.Sprintf("f%d", i), "alpha", "Orchestrator", theirID("a")) {
		case http.StatusOK:
			ok++
		case http.StatusTooManyRequests:
			limited++
		}
	}
	assert.Equal(t, crossTeamBurst, ok, "the per-sender limit: a burst of 3")
	assert.Equal(t, 3, limited)

	for i := 0; i < 40; i++ { // one message a minute for 40 minutes: never limited
		now = now.Add(time.Minute)
		assert.Equal(t, http.StatusOK, e.relayedAs(relaySrc, "beta", fmt.Sprintf("s%d", i), "alpha", "Orchestrator", theirID("a")), "message %d", i)
	}
}

// Inventing target names does not get fresh budgets from one Coral: the per-source limit counts them all.
func TestInventingTargetNamesDoesNotGetFreshBudgets(t *testing.T) {
	e := newArtifactEnv(t)
	e.open()
	e.enforce(true)
	e.h.crossTeam.now = func() time.Time { return fixedNow }
	var through, limited int
	for i := 0; i < 200; i++ {
		switch e.relayedAs(relaySrc, fmt.Sprintf("nowhere-%d", i), fmt.Sprintf("n%d", i), fmt.Sprintf("b%d", i), fmt.Sprintf("s%d", i), theirID("a")) {
		case http.StatusTooManyRequests:
			limited++
		default:
			through++ // refused at the door: that is an answer, not a limit
		}
	}
	assert.Equal(t, relaySourceBurst, through, "120 messages in all from one Coral, whatever the target names")
	assert.Equal(t, 200-relaySourceBurst, limited)

	// Another Coral has its own budgets.
	assert.NotEqual(t, http.StatusTooManyRequests, e.relayedAs("another-coral", "beta", "o1", "x", "y", theirID("b")))
}
