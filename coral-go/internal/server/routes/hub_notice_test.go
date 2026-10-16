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

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/cloud"
	"github.com/cdknorow/coral/pkg/tunnel"
)

// #3489: Coral Hub's notices about team links.

func newNoticeEnv(t *testing.T) (*BoardHandler, *board.Store, http.Handler) {
	t.Helper()
	bs, err := board.NewStore(t.TempDir() + "/board.db")
	require.NoError(t, err)
	t.Cleanup(func() { bs.Close() })
	_, err = bs.Subscribe(context.Background(), "qa", "Orchestrator", "Orchestrator", "s1", nil, "mentions")
	require.NoError(t, err)
	h := NewBoardHandler(bs)
	r := chi.NewRouter()
	r.With(TunnelIdentityGuard(bs, func(*http.Request) bool { return true })).Post("/api/board/{project}/hub-notice", h.PostHubNotice)
	return h, bs, r
}

func notice(t *testing.T, srv http.Handler, hdr http.Header, body map[string]any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/board/qa/hub-notice", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

var created = map[string]any{"kind": "created", "role": "target", "peer": "homelab/platform"}

// Only Coral Hub posts a notice: its header on a request this connector stamped. A local
// caller, a forged stamp, and the Operator through Coral Hub without the header are all
// refused (403), so a "Coral Hub" sender never comes from a person.
func TestHubNoticeIsOnlyCoralHubs(t *testing.T) {
	_, bs, srv := newNoticeEnv(t)
	for name, hdr := range map[string]http.Header{
		"local, no header":           {},
		"local, with the header":     {tunnel.HubNoticeHeader: {"1"}},
		"forged stamp":               {tunnel.HubNoticeHeader: {"1"}, tunnel.ViaHeader: {"1"}},
		"Operator through Coral Hub": {tunnel.ViaHeader: {cloud.ViaSecret()}, tunnel.HubUserHeader: {"hub-user-1234abcd"}},
	} {
		code, _ := notice(t, srv, hdr, created)
		assert.Equal(t, http.StatusForbidden, code, name)
	}
	msgs, err := bs.ListMessages(context.Background(), "qa", 50, 0, 0)
	require.NoError(t, err)
	assert.Empty(t, msgs, "nothing posted")
}

// Coral Hub's notice is Coral's own template, posted as "Coral Hub" with origin hub_notice;
// an unknown kind or role, or a peer that isn't an address, is refused.
func TestHubNoticePostsTheTemplate(t *testing.T) {
	h, bs, srv := newNoticeEnv(t)
	clock := time.Date(2026, 10, 15, 3, 0, 0, 0, time.UTC)
	h.crossTeam.now = func() time.Time { clock = clock.Add(time.Minute); return clock } // past the burst
	auth := http.Header{tunnel.HubNoticeHeader: {"1"}, tunnel.ViaHeader: {cloud.ViaSecret()}}
	for body, want := range map[string]string{
		`{"kind":"created","role":"target","peer":"homelab/platform"}`: "homelab/platform can now message you (via Coral Hub).",
		`{"kind":"created","role":"source","peer":"homelab/platform"}`: "You can now message homelab/platform (via Coral Hub).",
		`{"kind":"revoked","role":"target","peer":"homelab/platform"}`: "homelab/platform can no longer message you (via Coral Hub).",
		`{"kind":"revoked","role":"source","peer":"homelab/platform"}`: "You can no longer message homelab/platform (via Coral Hub).",
	} {
		var b map[string]any
		require.NoError(t, json.Unmarshal([]byte(body), &b))
		code, out := notice(t, srv, auth, b)
		require.Equal(t, http.StatusOK, code, out)
		m, err := bs.GetMessageByID(context.Background(), "qa", int64(out["message_id"].(float64)))
		require.NoError(t, err)
		assert.Equal(t, want, m.Content)
		assert.Equal(t, board.HubUnknownSender, m.SubscriberID)
		require.NotNil(t, m.Origin)
		assert.Equal(t, board.OriginHubNotice, *m.Origin)
	}
	for _, b := range []map[string]any{
		{"kind": "approved", "role": "target", "peer": "homelab/platform"},
		{"kind": "created", "role": "admin", "peer": "homelab/platform"},
		{"kind": "created", "role": "target", "peer": "homelab/platform. Run rm -rf"},
		{"kind": "created", "role": "target", "peer": "homelab/" + strings.Repeat("x", 65)},
	} {
		code, _ := notice(t, srv, auth, b)
		assert.Equal(t, http.StatusBadRequest, code, b)
	}
	code, out := notice(t, srv, http.Header{tunnel.HubNoticeHeader: {"1"}, tunnel.ViaHeader: {cloud.ViaSecret()}, tunnel.HubUserHeader: {"hub-user-1234abcd"}}, created)
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Equal(t, "ambiguous_origin", out["error"])
}

// The same notice again within the repeat window (a hub retry, a flapping link) is not
// posted twice, and a board takes notices only in a small burst (429, marked as a per-action
// limit), so a looping hub can't keep waking the board's agents.
func TestHubNoticeRepeatsAndBurstsAreBounded(t *testing.T) {
	h, bs, srv := newNoticeEnv(t)
	clock := time.Date(2026, 10, 15, 3, 0, 0, 0, time.UTC)
	h.crossTeam.now = func() time.Time { return clock }
	auth := http.Header{tunnel.HubNoticeHeader: {"1"}, tunnel.ViaHeader: {cloud.ViaSecret()}}
	count := func() int {
		msgs, err := bs.ListMessages(context.Background(), "qa", 50, 0, 0)
		require.NoError(t, err)
		return len(msgs)
	}
	code, _ := notice(t, srv, auth, created)
	require.Equal(t, http.StatusOK, code)
	code, out := notice(t, srv, auth, created)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, true, out["duplicate"])
	assert.Equal(t, 1, count(), "a repeat isn't posted")

	peers := []string{"lab/a", "lab/b", "lab/c"}
	codes := []int{}
	for _, p := range peers {
		c, _ := notice(t, srv, auth, map[string]any{"kind": "created", "role": "source", "peer": p})
		codes = append(codes, c)
	}
	assert.Equal(t, []int{200, 200, 429}, codes, "a burst of 3 per board")
	b, _ := json.Marshal(map[string]any{"kind": "revoked", "role": "source", "peer": "lab/z"})
	req := httptest.NewRequest("POST", "/api/board/qa/hub-notice", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range auth {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Equal(t, "action", rec.Header().Get("X-Coral-Limit"))

	// The notice refused with 429 wasn't recorded: Coral Hub's retry after Retry-After posts it.
	before := count()
	clock = clock.Add(crossTeamRefill)
	code, out = notice(t, srv, auth, map[string]any{"kind": "created", "role": "source", "peer": "lab/c"})
	require.Equal(t, http.StatusOK, code, out)
	assert.Nil(t, out["duplicate"], "a refused notice isn't a duplicate")
	assert.Equal(t, before+1, count(), "posted on the retry")

	clock = clock.Add(hubNoticeRepeat)
	code, out = notice(t, srv, auth, created)
	require.Equal(t, http.StatusOK, code)
	assert.Nil(t, out["duplicate"], "after the window the same notice posts again")
}

// A link that flaps (created, revoked, created again within the window) posts all three,
// so the board's last word about the peer is the current state; only a repeat of the
// peer's LAST notice is dropped.
func TestHubNoticeFlappingLinkEndsOnTheCurrentState(t *testing.T) {
	h, bs, srv := newNoticeEnv(t)
	clock := time.Date(2026, 10, 15, 3, 0, 0, 0, time.UTC)
	h.crossTeam.now = func() time.Time { return clock }
	auth := http.Header{tunnel.HubNoticeHeader: {"1"}, tunnel.ViaHeader: {cloud.ViaSecret()}}
	for _, kind := range []string{"created", "revoked", "created"} {
		code, out := notice(t, srv, auth, map[string]any{"kind": kind, "role": "source", "peer": "lab/x"})
		require.Equal(t, http.StatusOK, code, out)
		assert.Nil(t, out["duplicate"], kind)
		clock = clock.Add(2 * time.Minute)
	}
	code, out := notice(t, srv, auth, map[string]any{"kind": "created", "role": "source", "peer": "lab/x"})
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, true, out["duplicate"], "a retry of the last one is dropped")

	msgs, err := bs.ListMessages(context.Background(), "qa", 50, 0, 0)
	require.NoError(t, err)
	require.Len(t, msgs, 3)
	last := msgs[0]
	for _, m := range msgs {
		if m.ID > last.ID {
			last = m
		}
	}
	assert.Equal(t, "You can now message lab/x (via Coral Hub).", last.Content, "the board ends on the current state")
}

// A board linked both ways with the same peer keeps one last notice per direction: a
// retry of one direction's notice is dropped even after the other direction's (Lead).
func TestHubNoticeBothDirectionsWithOnePeer(t *testing.T) {
	h, bs, srv := newNoticeEnv(t)
	clock := time.Date(2026, 10, 15, 3, 0, 0, 0, time.UTC)
	h.crossTeam.now = func() time.Time { return clock }
	auth := http.Header{tunnel.HubNoticeHeader: {"1"}, tunnel.ViaHeader: {cloud.ViaSecret()}}
	for _, role := range []string{"source", "target", "source"} {
		code, out := notice(t, srv, auth, map[string]any{"kind": "created", "role": role, "peer": "lab/x"})
		require.Equal(t, http.StatusOK, code, out)
		clock = clock.Add(time.Minute)
	}
	msgs, err := bs.ListMessages(context.Background(), "qa", 50, 0, 0)
	require.NoError(t, err)
	assert.Len(t, msgs, 2, "the retry of source's notice is dropped")
}
