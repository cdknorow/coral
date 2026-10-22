package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/pkg/redact"
	"github.com/subgentic/coral/pkg/tunnel"
)

// #5968: the cross-team relay on the NEW DEFAULT, end to end through a real tunnel. A fake hub advertises cross_team_links/1, sends a link snapshot and relays a message and a file; the
// connector stamps them (this connection's capability, the sending team the hub names, this process's secret) and the real board handlers decide. The boards here never had a switch set, so the
// switches read their default (on, #5916): a linked team is admitted, every other cause is refused, an explicit off holds, a stale snapshot refuses and the Messaging tab shows the link.

type defaultRelayEnv struct {
	*linksEnv
	r    chi.Router
	sess *tunnel.Session
	n    int
}

func newDefaultRelayEnv(t *testing.T) *defaultRelayEnv {
	t.Helper()
	e := &defaultRelayEnv{linksEnv: newLinksEnv(t)}
	e.h.coralDir = t.TempDir()
	clock := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	e.h.crossTeam.now = func() time.Time { clock = clock.Add(time.Hour); return clock } // the rate limit is not what is checked here
	for _, b := range []string{e.target, e.target2} {
		_, err := e.bs.Subscribe(t.Context(), b, "Orchestrator", "Orchestrator", "tmux-default-"+b, nil, "mentions")
		require.NoError(t, err)
		got, err := e.bs.GetBoardSettings(t.Context(), b)
		require.NoError(t, err)
		require.False(t, got.MessagingStored || got.ArtifactsStored, "the board never set a switch: the default is what decides")
	}

	e.r = chi.NewRouter()
	e.r.Post(tunnel.HubLinksPath, e.h.PostCrossTeamLinks)
	e.r.Post("/api/board/{project}/external-messages", e.h.PostExternalMessage)
	e.r.Post("/api/board/{project}/external-artifacts", e.h.PostExternalArtifact)
	e.r.Get("/api/board/{project}/cross-team-state", e.h.GetCrossTeamState)
	local := httptest.NewServer(e.r)
	t.Cleanup(local.Close)

	// The fake hub: advertises cross_team_links/1 in its Welcome.
	sessions := make(chan *tunnel.Session, 1)
	hubSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if s, err := tunnel.AcceptHTTP(w, req, func(context.Context, string) (any, error) { return "x", nil },
			tunnel.SessionConfig{Capabilities: func(any) []string { return []string{tunnel.CapCrossTeamLinks} }}); err == nil {
			sessions <- s
		}
	}))
	t.Cleanup(hubSrv.Close)
	launch := func() tunnel.Mode { return tunnel.TierLaunch }
	cs, err := tunnel.Dial(context.Background(), "ws"+strings.TrimPrefix(hubSrv.URL, "http"), "tok", tunnel.ClientOptions{
		Handler:       &tunnel.LocalHandler{BaseURL: local.URL, Redactor: redact.Rules, AllowRequest: tunnel.AllowRequest(launch), MaxMode: launch, ViaValue: cloud.ViaSecret()},
		AllowInsecure: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { cs.Close() })
	select {
	case e.sess = <-sessions:
	case <-time.After(10 * time.Second):
		t.Fatal("the connector never reached the fake hub")
	}
	t.Cleanup(func() { e.sess.Close() })
	return e
}

// hub sends one request over the tunnel as Coral Hub does: the snapshot marker for a snapshot, the sending server and team for a relayed message or file.
func (e *defaultRelayEnv) hub(path string, hdr map[string]string, body []byte) (int, string) {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(tunnel.WithHubTier(context.Background(), "launch"), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://hub"+path, bytes.NewReader(body))
	require.NoError(e.t, err)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := e.sess.RoundTrip(req)
	require.NoError(e.t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (e *defaultRelayEnv) sendSnapshot(version int64, links ...snapshotLink) {
	e.t.Helper()
	code, body := e.hub(tunnel.HubLinksPath, map[string]string{tunnel.HubLinksHeader: tunnel.HubLinksSnapshot, "Content-Type": "application/json"}, []byte(e.snapshot(version, links...)))
	require.Equal(e.t, 200, code, body)
}

func (e *defaultRelayEnv) message(src, team, target string) (int, string) {
	e.t.Helper()
	e.n++
	b, _ := json.Marshal(map[string]any{"from_board": "QA", "subscriber_id": "Orchestrator", "content": "hello", "relay": map[string]any{"message_id": fmt.Sprintf("m%d", e.n), "hops": 0},
		"from_cross_team_id": "x-" + strings.Repeat("f", 16)}) // another team's id, which the links door never reads
	return e.hub("/api/board/"+target+"/external-messages", relayHeaders(src, team, "application/json"), b)
}

func (e *defaultRelayEnv) file(src, team, target string) (int, string) {
	e.t.Helper()
	e.n++
	data := []byte("the file " + strings.Repeat("x", e.n))
	m := map[string]any{"message_id": "f" + strings.Repeat("a", e.n), "to_address": "srv/" + target, "from_board": "QA", "digest": sha256Of(data), "size": len(data), "name": "a.txt",
		"media_type": "text/plain", "from_cross_team_id": "x-" + strings.Repeat("f", 16)}
	return e.hub("/api/board/"+target+"/external-artifacts", relayHeaders(src, team, "application/octet-stream"), frame(m, data))
}

func relayHeaders(src, team, contentType string) map[string]string {
	h := map[string]string{tunnel.HubServerHeader: src, "Content-Type": contentType}
	if team != "" {
		h[tunnel.HubSourceTeamHeader] = team
	}
	return h
}

func (e *defaultRelayEnv) set(target string, messaging, files bool) {
	e.t.Helper()
	_, err := e.bs.UpdateBoardSettings(context.Background(), target, board.BoardSettingsUpdate{CrossTeamMessaging: &messaging, CrossTeamArtifacts: &files})
	require.NoError(e.t, err)
}

func TestALinkedTeamIsAdmittedOnTheDefaultThroughARealTunnelAndEveryOtherCauseIsRefused(t *testing.T) {
	e := newDefaultRelayEnv(t)
	e.sendSnapshot(1, e.link("lab2", srcTeam1, e.target))

	code, body := e.message("lab2", srcTeam1, e.target)
	assert.Equal(t, 200, code, "a linked team's message, no switch ever set: "+body)
	code, body = e.file("lab2", srcTeam1, e.target)
	assert.Equal(t, 200, code, "and its file: "+body)

	type result struct {
		code int
		body string
	}
	refused := func(name string, send func(src, team, target string) (int, string), src, team, target string) result {
		c, b := send(src, team, target)
		assert.GreaterOrEqual(t, c, 400, name)
		return result{c, b}
	}
	// A board that does not exist is refused too. A message gets the same bytes as every other refusal; a file's refusal is its own 404 today (the door looks the board up first), which is
	// not what this test pins.
	code, _ = e.message("lab2", srcTeam1, "b-zzzzzzzzzz")
	assert.Equal(t, 404, code, "no such team: a message is refused")
	code, _ = e.file("lab2", srcTeam1, "b-zzzzzzzzzz")
	assert.Equal(t, 404, code, "no such team: a file is refused")
	for kind, send := range map[string]func(string, string, string) (int, string){"message": e.message, "file": e.file} {
		var first result
		for name, r := range map[string]result{
			"another team":                 refused(kind+": another team", send, "lab2", srcTeam2, e.target),
			"no sending team from the hub": refused(kind+": no sending team", send, "lab2", "", e.target),
			"another server":               refused(kind+": another server", send, "lab3", srcTeam1, e.target),
			"a team it is not linked to":   refused(kind+": not linked", send, "lab2", srcTeam1, e.target2),
		} {
			if first == (result{}) {
				first = r
			}
			assert.Equal(t, first, r, kind+" / "+name+": the same status and bytes as every other refusal")
		}
	}
}

func TestAnExplicitOffHoldsOnTheNewDefault(t *testing.T) {
	e := newDefaultRelayEnv(t)
	e.sendSnapshot(1, e.link("lab2", srcTeam1, e.target))
	e.set(e.target, false, true)
	code, _ := e.message("lab2", srcTeam1, e.target)
	assert.GreaterOrEqual(t, code, 400, "messaging explicitly off: the linked team's message is refused")
	code, _ = e.file("lab2", srcTeam1, e.target)
	assert.GreaterOrEqual(t, code, 400, "and so is its file: both switches decide a file")
	e.set(e.target, true, false)
	code, _ = e.message("lab2", srcTeam1, e.target)
	assert.Equal(t, 200, code, "messaging on, files explicitly off: the message is admitted")
	code, _ = e.file("lab2", srcTeam1, e.target)
	assert.GreaterOrEqual(t, code, 400, "and the file is refused")
	e.set(e.target, true, true)
	code, _ = e.file("lab2", srcTeam1, e.target)
	assert.Equal(t, 200, code, "both on explicitly: admitted again")
}

func TestAStaleOrRevokedSnapshotRefusesTheDefaultToo(t *testing.T) {
	e := newDefaultRelayEnv(t)
	e.sendSnapshot(1, e.link("lab2", srcTeam1, e.target))
	code, _ := e.message("lab2", srcTeam1, e.target)
	require.Equal(t, 200, code)
	e.advance(linkTTL + time.Second)
	code, _ = e.message("lab2", srcTeam1, e.target)
	assert.GreaterOrEqual(t, code, 400, "a snapshot older than the TTL admits no message")
	code, _ = e.file("lab2", srcTeam1, e.target)
	assert.GreaterOrEqual(t, code, 400, "and no file")
	e.sendSnapshot(2, e.link("lab2", srcTeam1, e.target))
	code, _ = e.message("lab2", srcTeam1, e.target)
	assert.Equal(t, 200, code, "a fresh snapshot admits again")
	e.sendSnapshot(3) // the link was revoked
	code, _ = e.message("lab2", srcTeam1, e.target)
	assert.GreaterOrEqual(t, code, 400, "the next snapshot without the link revokes it")
	code, _ = e.file("lab2", srcTeam1, e.target)
	assert.GreaterOrEqual(t, code, 400, "for files too")
}

func TestTheMessagingTabShowsTheLinkOnTheNewDefault(t *testing.T) {
	e := newDefaultRelayEnv(t)
	state := func(target string) (links []map[string]any, elsewhere, here bool) {
		t.Helper()
		rec := httptest.NewRecorder()
		e.r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/board/"+target+"/cross-team-state", nil))
		require.Equal(t, 200, rec.Code, rec.Body.String())
		var out struct {
			Links     []map[string]any `json:"links"`
			Elsewhere bool             `json:"messaging_elsewhere"`
			Here      bool             `json:"messaging_here"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		return out.Links, out.Elsewhere, out.Here
	}
	links, elsewhere, here := state(e.target)
	assert.Empty(t, links, "no snapshot yet: no link")
	assert.True(t, elsewhere, "teams on other computers: on by default")
	assert.False(t, here, "teams on this computer: off until the owner turns it on")

	e.sendSnapshot(1, e.link("lab2", srcTeam1, e.target))
	links, elsewhere, here = state(e.target)
	require.Len(t, links, 1, "the linked team is listed")
	assert.Equal(t, srcTeam1, links[0]["team_id"])
	assert.Equal(t, "lab2", links[0]["server_id"])
	assert.True(t, elsewhere)
	assert.False(t, here, "a link does not open this computer's own teams to each other")
	other, _, _ := state(e.target2)
	assert.Empty(t, other, "the other board shows no link it does not have")

	code, _ := e.message(links[0]["server_id"].(string), links[0]["team_id"].(string), e.target)
	assert.Equal(t, 200, code, "the team the tab shows as linked can send")
}
