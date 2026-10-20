package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/cloud"
	"github.com/cdknorow/coral/pkg/tunnel"
)

// #5508 on the relayed doors: a message or a file from another Coral needs a list entry with BOTH the hub-verified server and the cross-team id the sending Coral claims.

func (e *artifactEnv) enforce(on bool) {
	e.h.SetAllowlistFlag(func(context.Context) bool { return on })
	// The message rate limit is not what most of these tests check: an hour passes between messages (the rate tests set their own still clock).
	clock := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	e.h.crossTeam.now = func() time.Time { clock = clock.Add(time.Hour); return clock }
}

func (e *artifactEnv) list(entries ...board.AllowedSender) {
	e.t.Helper()
	if entries == nil {
		entries = []board.AllowedSender{}
	}
	_, err := e.h.bs.UpdateBoardSettings(context.Background(), "beta", board.BoardSettingsUpdate{CrossTeamAllowed: &entries})
	require.NoError(e.t, err)
}

// relayed posts the relayed message relayID from src with the given claim and subscriber, to board target; it returns the status, the exact bytes and the headers' content length.
func (e *artifactEnv) relayed(src, target, relayID, claim, subscriber string) (int, string, string) {
	e.t.Helper()
	body := map[string]any{"from_board": "alpha", "subscriber_id": subscriber, "content": "here are the files", "relay": map[string]any{"message_id": relayID, "hops": 0}}
	if claim != "-" {
		body["from_cross_team_id"] = claim
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/board/"+target+"/external-messages", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tunnel.HubServerHeader, src)
	req.Header.Set(tunnel.ViaHeader, cloud.ViaSecret())
	rec := httptest.NewRecorder()
	e.r.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String(), rec.Header().Get("Content-Length")
}

func theirID(c string) string { return "x-" + strings.Repeat(c, 16) }

func TestRelayedMessageNeedsBothTheServerAndTheClaimedIDOnTheList(t *testing.T) {
	e := newArtifactEnv(t)
	e.open()
	e.enforce(true)
	listed := board.AllowedSender{Server: relaySrc, CrossTeamID: theirID("a")}

	// Empty list: nobody.
	code, body, _ := e.relayed(relaySrc, "beta", "r1", theirID("a"), "Orchestrator")
	assert.Equal(t, http.StatusForbidden, code, body)
	assert.Contains(t, body, `"error":"cross_team_refused"`)

	e.list(listed)
	code, body, _ = e.relayed(relaySrc, "beta", "r2", theirID("a"), "Orchestrator")
	assert.Equal(t, http.StatusOK, code, body)

	refusals := map[string]struct{ src, claim string }{
		"the right id on the wrong server": {"other-server", theirID("a")},
		"the right server, another id":     {relaySrc, theirID("b")},
		"no claim at all":                  {relaySrc, "-"},
		"an empty claim":                   {relaySrc, ""},
		"a name instead of an id":          {relaySrc, "alpha"},
		"a malformed id":                   {relaySrc, "x-SHORT"},
		"an upper-case id":                 {relaySrc, strings.ToUpper(theirID("a"))},
	}
	var first, firstLen string
	n := 0
	for name, c := range refusals {
		n++
		code, body, length := e.relayed(c.src, "beta", "ref"+strings.Repeat("z", n), c.claim, "Orchestrator")
		assert.Equal(t, http.StatusForbidden, code, name)
		assert.Contains(t, body, "cross_team_refused", name)
		if c.claim != "" && c.claim != "-" {
			assert.NotContains(t, body, c.claim, name+": a claimed id is never echoed")
		}
		assert.NotContains(t, body, "x-", name)
		if first == "" {
			first, firstLen = body, length
		}
		assert.Equal(t, first, body, name+": the same bytes")
		assert.Equal(t, firstLen, length, name)
	}
}

// A relayed message that SAYS it is from the Operator is only a claim by the sending Coral: it needs a list entry like any other.
func TestARelayedOperatorClaimIsNotExempt(t *testing.T) {
	e := newArtifactEnv(t)
	e.open()
	e.enforce(true)
	code, body, _ := e.relayed(relaySrc, "beta", "op1", theirID("c"), "Operator")
	assert.Equal(t, http.StatusForbidden, code, body)
	e.list(board.AllowedSender{Server: relaySrc, CrossTeamID: theirID("c")})
	code, body, _ = e.relayed(relaySrc, "beta", "op2", theirID("c"), "Operator")
	assert.Equal(t, http.StatusOK, code, body)
}

// The same refusal, the same bytes and the same steps for a board that is not there, one that is off and one that does not list the sender; removal stops the next message.
func TestRelayedRefusalIsTheSameForEveryCauseAndRemovalStopsTheNextMessage(t *testing.T) {
	e := newArtifactEnv(t)
	e.open()
	e.enforce(true)
	var steps []string
	doorTrace = func(s string) { steps = append(steps, s) }
	defer func() { doorTrace = nil }()
	ask := func(target, id string) (int, string, []string) {
		steps = nil
		code, body, _ := e.relayed(relaySrc, target, id, theirID("a"), "Orchestrator")
		return code, body, append([]string(nil), steps...)
	}
	c1, b1, s1 := ask("beta", "u1")    // exists, empty list
	c2, b2, s2 := ask("nowhere", "u2") // does not exist
	assert.Equal(t, http.StatusForbidden, c1)
	assert.Equal(t, c1, c2)
	assert.Equal(t, b1, b2, "an unknown board answers as an unlisted sender does")
	assert.Equal(t, s1, s2)
	assert.Equal(t, []string{"exists", "settings", "list", "recipients"}, s1)

	e.list(board.AllowedSender{Server: relaySrc, CrossTeamID: theirID("a")})
	code, body, _ := ask("beta", "u3")
	require.Equal(t, http.StatusOK, code, body)
	e.list()
	code, _, _ = ask("beta", "u4")
	assert.Equal(t, http.StatusForbidden, code, "removed: the very next relayed message is refused")
}

// The relayed refusal costs the sender's rate token as an accepted message does.
func TestARefusedRelayedMessageCostsTheSameRateToken(t *testing.T) {
	e := newArtifactEnv(t)
	e.open()
	e.enforce(true)
	e.h.crossTeam.now = func() time.Time { return fixedNow }
	var refused, limited int
	for i := 0; i < 15; i++ {
		code, _, _ := e.relayed(relaySrc, "beta", "t"+strings.Repeat("x", i), theirID("a"), "Orchestrator")
		switch code {
		case http.StatusForbidden:
			refused++
		case http.StatusTooManyRequests:
			limited++
		}
	}
	assert.Positive(t, refused)
	assert.Positive(t, limited, "refusals spend the token: the sender is limited though nothing was accepted")
}

// Files follow the list: an unlisted sender's file is refused with the one uniform answer, a listed sender's is accepted and promoted with its message, and removal stops the next file.
func TestFilesFollowTheList(t *testing.T) {
	e := newArtifactEnv(t)
	e.open()
	e.enforce(true)
	data := []byte("# plan\nsome notes\n")
	withClaim := func(claim string) map[string]any {
		m := meta(data, "plan.md")
		if claim != "" {
			m["from_cross_team_id"] = claim
		}
		return m
	}

	status, out := e.send("auth", relaySrc, "", frame(withClaim(theirID("a")), data))
	assert.Equal(t, http.StatusForbidden, status, "an empty list: no files")
	assert.Equal(t, "artifacts_off", out["error"])
	assert.Empty(t, e.stored(), "nothing refused is stored")

	e.list(board.AllowedSender{Server: relaySrc, CrossTeamID: theirID("a")})
	status, out = e.send("auth", relaySrc, "", frame(withClaim(""), data))
	assert.Equal(t, http.StatusForbidden, status, "no claim: refused")
	status, out = e.send("auth", relaySrc, "", frame(withClaim(theirID("b")), data))
	assert.Equal(t, http.StatusForbidden, status, "another board's id: refused")
	status, out = e.send("auth", "other-server", "", frame(withClaim(theirID("a")), data))
	assert.Equal(t, http.StatusForbidden, status, "the right id on the wrong server: refused")
	assert.Empty(t, e.stored())

	status, out = e.send("auth", relaySrc, "", frame(withClaim(theirID("a")), data))
	require.Equal(t, 200, status, out)
	code, body, _ := e.relayed(relaySrc, "beta", "m1", theirID("a"), "Orchestrator")
	require.Equal(t, 200, code, body)
	assert.Len(t, e.listed(), 1, "a listed sender's file is listed with its message")

	// Removed: the next file is refused, and a message that names an earlier pending file lists none of it.
	e.list()
	more := []byte("# another\n")
	m := meta(more, "more.md")
	m["message_id"] = "m2"
	m["from_cross_team_id"] = theirID("a")
	status, _ = e.send("auth", relaySrc, "", frame(m, more))
	assert.Equal(t, http.StatusForbidden, status, "removed: the very next file is refused")

	// An unknown board is the same answer as every other refusal at the file door.
	req := httptest.NewRequest("POST", "/api/board/nowhere/external-artifacts", bytes.NewReader(frame(withClaim(theirID("a")), data)))
	req.Header.Set(tunnel.HubServerHeader, relaySrc)
	req.Header.Set(tunnel.ViaHeader, cloud.ViaSecret())
	rec := httptest.NewRecorder()
	e.r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "artifacts_off")
}

// Flag off: today's rules for relayed messages and files, and the list's "would refuse" counted.
func TestRelayedFlagOffKeepsTodaysRulesAndCountsWhatWouldBeRefused(t *testing.T) {
	e := newArtifactEnv(t)
	e.open()
	e.enforce(false)
	for i := 0; i < 2; i++ {
		code, body, _ := e.relayed(relaySrc, "beta", "f"+strings.Repeat("y", i), theirID("a"), "Orchestrator")
		assert.Equal(t, http.StatusOK, code, body)
	}
	log, err := e.h.bs.ListLog(context.Background(), "beta")
	require.NoError(t, err)
	require.Len(t, log, 1)
	assert.Equal(t, board.LogWouldRefuse, log[0].Kind)
	assert.Equal(t, 2, log[0].Count)
	assert.Equal(t, []board.AllowedSender{{Server: relaySrc, CrossTeamID: theirID("a")}}, log[0].Senders)
	// A file today: accepted with no claim at all (a Coral from before the list).
	data := []byte("# old coral\n")
	status, out := e.send("auth", relaySrc, "", frame(meta(data, "old.md"), data))
	assert.Equal(t, 200, status, out)
}
