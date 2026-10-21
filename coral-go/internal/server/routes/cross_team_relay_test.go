package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/httputil"
	"github.com/subgentic/coral/pkg/tunnel"
)

// #3489: a cross-team message Coral Hub relays from another Coral of the same account.

const relaySrc = "0b6c6a52-8a0e-4f4e-9d61-3f1e2a1b9c00"

// relay posts a relayed message to board beta. via is the connector stamp: "auth" for this
// process's real one, "forged" for any other value, "" for none (a local caller).
func (e *crossTeamEnv) relay(via, hubUser string, body map[string]any) (int, map[string]any) {
	e.t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", e.url+"/api/board/beta/external-messages", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tunnel.HubServerHeader, relaySrc)
	switch via {
	case "auth":
		req.Header.Set(tunnel.ViaHeader, cloud.ViaSecret())
	case "forged":
		req.Header.Set(tunnel.ViaHeader, "1")
	}
	if hubUser != "" {
		req.Header.Set(tunnel.HubUserHeader, hubUser)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(e.t, err)
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func relayBody(id string, hops int, content string) map[string]any {
	return map[string]any{"from_board": "epoch-tests", "subscriber_id": "Orchestrator", "content": content,
		"relay": map[string]any{"message_id": id, "hops": hops}}
}

func (e *crossTeamEnv) allowServers(ids ...string) (int, map[string]any) {
	if ids == nil {
		ids = []string{} // [] clears the list; null (the field left out) changes nothing
	}
	return e.do("PUT", "/api/board/beta/settings", map[string]any{"subscriber_id": "Operator", "cross_team_servers": ids})
}

func (e *crossTeamEnv) betaMessages() []map[string]any {
	e.t.Helper()
	resp, err := http.Get(e.url + "/api/board/beta/messages/all")
	require.NoError(e.t, err)
	defer resp.Body.Close()
	var out []map[string]any
	require.NoError(e.t, json.NewDecoder(resp.Body).Decode(&out))
	return out
}

// The header is trusted only on a request this process's connector stamped: a local
// caller, or a forged stamp, is refused and nothing is delivered. A request naming both a
// server and a Coral Hub user is refused too.
func TestRelayedMessageNeedsAnAuthenticTunnel(t *testing.T) {
	e := newCrossTeamEnv(t)
	e.enable("beta", true)
	_, _ = e.allowServers(relaySrc)
	before := len(e.betaMessages())

	code, out := e.relay("", "", relayBody("m1", 0, "hello"))
	assert.Equal(t, http.StatusForbidden, code)
	assert.Equal(t, "hub_server_untrusted", out["error"])
	code, out = e.relay("forged", "", relayBody("m1", 0, "hello"))
	assert.Equal(t, http.StatusForbidden, code)
	assert.Equal(t, "hub_server_untrusted", out["error"])
	code, out = e.relay("auth", "hub-user-1234abcd", relayBody("m1", 0, "hello"))
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Equal(t, "ambiguous_origin", out["error"])

	assert.Len(t, e.betaMessages(), before, "nothing delivered")
}

// Only a board with cross-team messaging on, listing the source server, receives it; it
// is stored once, inbound only, from the sender's role, board and server, with an
// @mention for the board's Orchestrator, and the reply names no recipients.
func TestRelayedMessageNeedsTheBoardsAllowlist(t *testing.T) {
	e := newCrossTeamEnv(t)
	clock := time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	e.h.crossTeam.now = func() time.Time { return clock }

	code, out := e.relay("auth", "", relayBody("m1", 0, "please run the check"))
	assert.Equal(t, http.StatusNotFound, code)
	assert.Equal(t, "not_found", out["error"])

	e.enable("beta", true)
	code, out = e.relay("auth", "", relayBody("m1", 0, "please run the check"))
	assert.Equal(t, http.StatusNotFound, code)
	assert.Equal(t, "not_found", out["error"])

	code, out = e.allowServers("1111aaaa-0000-0000-0000-000000000000")
	require.Equal(t, http.StatusOK, code, out)
	code, out = e.relay("auth", "", relayBody("m1", 0, "please run the check"))
	assert.Equal(t, http.StatusNotFound, code)
	assert.Equal(t, "not_found", out["error"])

	code, out = e.allowServers(strings.ToUpper(relaySrc))
	require.Equal(t, http.StatusOK, code, out)
	assert.Equal(t, []any{relaySrc}, out["cross_team_servers"], "stored lower case")
	// A refusal costs the sender's rate token as an accepted message does (#5572): the three refusals above used the burst, so the sender waits.
	clock = clock.Add(time.Minute)
	code, out = e.relay("auth", "", relayBody("m1", 1, "please run the check"))
	require.Equal(t, http.StatusOK, code, out)
	assert.Equal(t, true, out["ok"])
	assert.NotNil(t, out["target_message_id"])
	assert.NotContains(t, out, "delivered_to")
	assert.NotContains(t, out, "mirror")

	msgs := e.betaMessages()
	last := msgs[len(msgs)-1]
	assert.Equal(t, "Orchestrator (epoch-tests, server 0b6c6a52)", last["subscriber_id"])
	assert.Contains(t, last["content"], "@Orchestrator")
	assert.Contains(t, last["content"], "please run the check")
	ext, _ := last["external"].(map[string]any)
	require.NotNil(t, ext, "the envelope is stored")
	assert.Equal(t, relaySrc, ext["server_id"])
	assert.Equal(t, "inbound", ext["direction"])
	assert.Equal(t, "m1", ext["relay_message_id"])
	assert.Equal(t, float64(1), ext["hops"])
}

// A message id already delivered from a server is answered as a duplicate, with the
// first delivery's id, and not delivered again.
func TestRelayedMessageIsDeliveredOnce(t *testing.T) {
	e := newCrossTeamEnv(t)
	e.enable("beta", true)
	_, _ = e.allowServers(relaySrc)
	code, first := e.relay("auth", "", relayBody("dup-1", 0, "once"))
	require.Equal(t, http.StatusOK, code, first)
	n := len(e.betaMessages())

	code, again := e.relay("auth", "", relayBody("dup-1", 0, "once"))
	require.Equal(t, http.StatusOK, code, again)
	assert.Equal(t, true, again["duplicate"])
	assert.Equal(t, first["target_message_id"], again["target_message_id"])
	assert.Len(t, e.betaMessages(), n, "not delivered twice")

	code, other := e.relay("auth", "", relayBody("dup-2", 0, "a new one"))
	require.Equal(t, http.StatusOK, code, other)
	assert.NotEqual(t, first["target_message_id"], other["target_message_id"])
}

// The relay fields and limits: a message id is required, a reply chain is capped, and a
// relayed message may be up to 32 KiB (local cross-team posts stay at 8000 bytes).
func TestRelayedMessageLimits(t *testing.T) {
	e := newCrossTeamEnv(t)
	e.enable("beta", true)
	_, _ = e.allowServers(relaySrc)

	noRelay := relayBody("x", 0, "hi")
	delete(noRelay, "relay")
	code, out := e.relay("auth", "", noRelay)
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Equal(t, "bad_request", out["error"])

	code, out = e.relay("auth", "", relayBody("bad id!", 0, "hi"))
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Equal(t, "bad_request", out["error"])

	code, out = e.relay("auth", "", relayBody("h4", maxRelayHops+1, "hi"))
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Equal(t, "too_many_hops", out["error"])

	code, out = e.relay("auth", "", relayBody("big", 0, strings.Repeat("x", maxRelayedContent+1)))
	assert.Equal(t, http.StatusRequestEntityTooLarge, code)
	assert.Equal(t, "too_large", out["error"])

	code, out = e.relay("auth", "", relayBody("ok-20k", maxRelayHops, strings.Repeat("y", 20000)))
	assert.Equal(t, http.StatusOK, code, out)
}

// The allowlist is the Operator's to set, holds only Coral Hub server ids, and is capped.
func TestRelayAllowlistSettingIsOperatorOnly(t *testing.T) {
	e := newCrossTeamEnv(t)
	code, _ := e.do("PUT", "/api/board/beta/settings", map[string]any{"subscriber_id": "QA", "cross_team_servers": []string{relaySrc}})
	assert.Equal(t, http.StatusForbidden, code)
	code, _ = e.allowServers("not a server id")
	assert.Equal(t, http.StatusBadRequest, code)
	many := make([]string, maxCrossTeamServer+1)
	for i := range many {
		many[i] = strings.Repeat("a", i+1)
	}
	code, _ = e.allowServers(many...)
	assert.Equal(t, http.StatusBadRequest, code)
	code, out := e.allowServers(relaySrc, relaySrc)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, []any{relaySrc}, out["cross_team_servers"], "de-duplicated")
	code, out = e.allowServers()
	require.Equal(t, http.StatusOK, code)
	assert.Empty(t, out["cross_team_servers"], "cleared")
}

// The browser identity guard leaves an AUTHENTIC relay to the handler (a remote
// Orchestrator is a server's sender, checked against the board's allowlist there), but a
// relay with a forged or missing stamp still goes through the guard, which refuses an
// Orchestrator identity as it does for any tunnel write.
func TestTunnelIdentityGuardLeavesOnlyAuthenticRelays(t *testing.T) {
	_, handler := setupBoardTestServer(t)
	_, err := handler.bs.Subscribe(context.Background(), "p", "Dev", "Developer", "s1", nil, "")
	require.NoError(t, err)
	passed := 0
	g := TunnelIdentityGuard(handler.bs, func(*http.Request) bool { return true })(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		passed++
	}))
	body := `{"from_board":"epoch-tests","subscriber_id":"Orchestrator","content":"hi","relay":{"message_id":"m1","hops":0}}`
	do := func(via string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/board/p/external-messages", strings.NewReader(body))
		r.Header.Set(tunnel.HubServerHeader, relaySrc)
		r.Header.Set(tunnel.ViaHeader, via)
		rec := httptest.NewRecorder()
		g.ServeHTTP(rec, r)
		return rec
	}
	rec := do(cloud.ViaSecret())
	assert.Equal(t, 1, passed, "an authentic relay reaches the handler")
	assert.Equal(t, http.StatusOK, rec.Code)
	rec = do("1")
	assert.Equal(t, 1, passed, "a forged stamp doesn't")
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "orchestrator_identity", codeOf(t, rec))
}

// Deliveries of the same message id at the same time (a hub retry racing the first
// attempt) are serialised by the claim: exactly one is delivered and every other answers
// duplicate with the same id; none fails.
func TestRelayedMessageRaceDeliversOnce(t *testing.T) {
	e := newCrossTeamEnv(t)
	e.enable("beta", true)
	_, _ = e.allowServers(relaySrc)
	before := len(e.betaMessages())
	const n = crossTeamBurst // all within the sender's burst, so the limiter lets every one through
	type result struct {
		code int
		out  map[string]any
	}
	results := make(chan result, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		go func() {
			<-start
			code, out := e.relay("auth", "", relayBody("race-1", 0, "only once"))
			results <- result{code, out}
		}()
	}
	close(start)
	delivered, duplicates := 0, 0
	var ids []any
	for i := 0; i < n; i++ {
		r := <-results
		require.Equal(t, http.StatusOK, r.code, r.out)
		ids = append(ids, r.out["target_message_id"])
		if r.out["duplicate"] == true {
			duplicates++
		} else {
			delivered++
		}
	}
	assert.Equal(t, 1, delivered)
	assert.Equal(t, n-1, duplicates)
	for _, id := range ids {
		assert.Equal(t, ids[0], id, "every answer names the one delivery")
	}
	assert.Len(t, e.betaMessages(), before+1)
}

// Past the sender's burst, a relayed message gets 429 marked as a per-action limit
// (X-Coral-Limit: action, Retry-After), with the relay contract's body.
func TestRelayedMessageRateLimitIsAnActionLimit(t *testing.T) {
	e := newCrossTeamEnv(t)
	e.enable("beta", true)
	_, _ = e.allowServers(relaySrc)
	for i := 0; i < crossTeamBurst; i++ {
		code, out := e.relay("auth", "", relayBody("burst-"+strconv.Itoa(i), 0, "hi"))
		require.Equal(t, http.StatusOK, code, out)
	}
	b, _ := json.Marshal(relayBody("burst-over", 0, "hi"))
	req, _ := http.NewRequest("POST", e.url+"/api/board/beta/external-messages", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tunnel.HubServerHeader, relaySrc)
	req.Header.Set(tunnel.ViaHeader, cloud.ViaSecret())
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	assert.Equal(t, "action", resp.Header.Get(httputil.ActionLimitHeader))
	assert.NotEmpty(t, resp.Header.Get("Retry-After"))
	var out map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	assert.Equal(t, false, out["ok"])
	assert.Equal(t, "rate_limited", out["error"])
}

// #4901: the sending board's name of a relayed message is shown as its origin: bounded plain text, as a received file's from_board already
// is. A control or formatting character, or more than 200 bytes, is a 400 and nothing is delivered.
func TestRelayedMessageFromBoardIsAPlainLabel(t *testing.T) {
	e := newCrossTeamEnv(t)
	e.enable("beta", true)
	_, _ = e.allowServers(relaySrc)
	n := len(e.betaMessages())
	for i, bad := range []string{"epoch\x1b[2Jtests", "epoch\u0085tests", "epoch\u2028tests", "epoch\u202etests", "epoch\u200btests", "epoch\ntests", strings.Repeat("b", 201)} {
		body := relayBody(fmt.Sprintf("bad-%d", i), 0, "hi")
		body["from_board"] = bad
		code, out := e.relay("auth", "", body)
		assert.Equal(t, http.StatusBadRequest, code, "from_board %q: %v", bad, out)
	}
	assert.Len(t, e.betaMessages(), n, "nothing was delivered")
	body := relayBody("good-1", 0, "hi")
	body["from_board"] = "epoch tests (v2) 🙂"
	code, out := e.relay("auth", "", body)
	assert.Equal(t, http.StatusOK, code, out)
}

// #4901: the attachment lines of a cross-team message are typed into the receiving Orchestrator's terminal, so a stored upload name (one made
// before uploads were checked) is cleaned of control and formatting characters there too.
func TestAttachmentLinesCarryOnlyPlainNames(t *testing.T) {
	id := strings.Repeat("a", 64)
	out := attachmentLines([]attachedArtifact{
		{ID: id, Name: "report\x1b[2J\x07.md"}, {ID: id, Name: "a\u0085b\u2028c\u202ed.txt"}, {ID: id, Name: "\x1b\x1b"}, {ID: id, Name: "bad\xffutf8.txt"}, {ID: id, Name: "fine 🙂 (v2).md"},
	})
	for _, r := range out {
		assert.False(t, (r < 0x20 && r != '\n') || r == 0x7f || (r >= 0x80 && r <= 0x9f) || r == '\u2028' || r == '\u2029' || r == '\u202e', "no control or formatting character in %q", out)
	}
	assert.Contains(t, out, "- report[2J.md: coral://artifacts/"+id)
	assert.Contains(t, out, "- abcd.txt: ")
	assert.Contains(t, out, "- artifact: ", "a name of only control characters is the default")
	assert.Contains(t, out, "- badutf8.txt: ")
	assert.Contains(t, out, "- fine 🙂 (v2).md: ")
	assert.Equal(t, 5, strings.Count(out, "\n- "), "one line each")
}
