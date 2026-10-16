package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/cloud"
	"github.com/cdknorow/coral/pkg/tunnel"
)

// #3489 part 2: sending a cross-team message to another Coral through Coral Hub.

type fakeHub struct {
	mu    sync.Mutex
	calls []struct {
		Path string
		Body map[string]any
	}
	answer func(path string, body map[string]any) (tunnel.RelayResponse, error)
}

func (f *fakeHub) relay(_ context.Context, path string, raw []byte) (tunnel.RelayResponse, error) {
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	f.mu.Lock()
	f.calls = append(f.calls, struct {
		Path string
		Body map[string]any
	}{path, body})
	f.mu.Unlock()
	return f.answer(path, body)
}

func (f *fakeHub) last() (string, map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.calls[len(f.calls)-1]
	return c.Path, c.Body
}

type sendEnv struct {
	t   *testing.T
	url string
	h   *BoardHandler
	bs  *board.Store
	hub *fakeHub
}

func newSendEnv(t *testing.T) *sendEnv {
	t.Helper()
	bs, err := board.NewStoreWithKey(t.TempDir()+"/board.db", "")
	require.NoError(t, err)
	t.Cleanup(func() { bs.Close() })
	e := &sendEnv{t: t, h: NewBoardHandler(bs), bs: bs, hub: &fakeHub{}}
	e.hub.answer = func(path string, _ map[string]any) (tunnel.RelayResponse, error) {
		if path == tunnel.RelayTargetsPath {
			return tunnel.RelayResponse{Status: 200, Body: []byte(`{"ok":true,"targets":[{"address":"homelab/qa","server":"homelab","team":"qa","reachable":true}]}`)}, nil
		}
		return tunnel.RelayResponse{Status: 200, Body: []byte(`{"ok":true,"target_message_id":"77"}`)}, nil
	}
	e.h.SetRelay(e.hub.relay)
	r := chi.NewRouter()
	r.Put("/api/board/{project}/settings", e.h.UpdateBoardSettings)
	r.Post("/api/board/{project}/relay-messages", e.h.PostRelayMessage)
	r.Get("/api/board/{project}/relay-targets", e.h.ListRelayTargets)
	r.Get("/api/board/{project}/messages/all", e.h.ListAllMessages)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	e.url = srv.URL
	ctx := context.Background()
	for _, s := range []struct{ id, role string }{{"Orchestrator", "Orchestrator"}, {"Dev", "Backend Dev"}} {
		_, err := bs.Subscribe(ctx, "alpha", s.id, s.role, "tmux-alpha"+s.id, nil, "mentions")
		require.NoError(t, err)
	}
	code, out := e.do("PUT", "/api/board/alpha/settings", map[string]any{"subscriber_id": "Operator", "cross_team_messaging": true}, nil)
	require.Equal(t, http.StatusOK, code, out)
	return e
}

func (e *sendEnv) do(method, path string, body any, hdr http.Header) (int, map[string]any) {
	e.t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, e.url+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header[k] = v
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(e.t, err)
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if ra := resp.Header.Get("Retry-After"); ra != "" && out != nil {
		out["_retry_after"] = ra
	}
	return resp.StatusCode, out
}

func (e *sendEnv) send(extra map[string]any) (int, map[string]any) {
	body := map[string]any{"subscriber_id": "Orchestrator", "session_name": "tmux-alphaOrchestrator", "content": "please run the check", "to_address": "homelab/qa"}
	for k, v := range extra {
		body[k] = v
	}
	return e.do("POST", "/api/board/alpha/relay-messages", body, nil)
}

func (e *sendEnv) alphaMessages() []map[string]any {
	e.t.Helper()
	resp, err := http.Get(e.url + "/api/board/alpha/messages/all")
	require.NoError(e.t, err)
	defer resp.Body.Close()
	var out []map[string]any
	require.NoError(e.t, json.NewDecoder(resp.Body).Decode(&out))
	return out
}

// A message goes to Coral Hub as {message_id, hops 0, to_address, body of the five
// fields}, and the sender's copy is stored with the outbound envelope and the target id.
func TestRelaySendGoesToTheHubAndKeepsAMirror(t *testing.T) {
	e := newSendEnv(t)
	code, out := e.send(nil)
	require.Equal(t, http.StatusOK, code, out)
	assert.Equal(t, true, out["ok"])
	assert.Equal(t, float64(77), out["target_message_id"])
	mid, _ := out["message_id"].(string)
	assert.Regexp(t, `^[0-9a-f-]{36}$`, mid)

	path, body := e.hub.last()
	assert.Equal(t, tunnel.RelayMessagePath, path)
	assert.Equal(t, mid, body["message_id"])
	assert.Equal(t, float64(0), body["hops"])
	assert.Equal(t, "homelab/qa", body["to_address"])
	inner, _ := body["body"].(map[string]any)
	assert.Equal(t, map[string]any{"from_board": "alpha", "subscriber_id": "Orchestrator", "session_name": "tmux-alphaOrchestrator", "content": "please run the check", "to": ""}, inner)
	assert.NotContains(t, body, "source", "never a source id in the request")

	msgs := e.alphaMessages()
	last := msgs[len(msgs)-1]
	ext, _ := last["external"].(map[string]any)
	require.NotNil(t, ext)
	assert.Equal(t, "outbound", ext["direction"])
	assert.Equal(t, "homelab", ext["target_server_id"])
	assert.Equal(t, "qa", ext["target_board"])
	assert.Equal(t, float64(77), ext["target_message_id"])
	assert.Equal(t, mid, ext["relay_message_id"])
}

// Only this computer's Orchestrator (from its own session) or the Operator may send, only
// with the board's cross-team opt-in, never through Coral Hub, and only to a well-formed
// <server>/<team> address.
func TestRelaySendChecksTheSenderAndTheAddress(t *testing.T) {
	e := newSendEnv(t)
	code, out := e.do("POST", "/api/board/alpha/relay-messages", map[string]any{"subscriber_id": "Operator", "content": "hi", "to_address": "homelab/qa"},
		http.Header{tunnel.ViaHeader: {cloud.ViaSecret()}})
	assert.Equal(t, http.StatusForbidden, code)
	assert.Equal(t, "local_only", out["error"])

	code, out = e.send(map[string]any{"subscriber_id": "Dev", "session_name": "tmux-alphaDev"})
	assert.Equal(t, http.StatusForbidden, code)
	assert.Equal(t, "not_orchestrator", out["error"])
	code, out = e.send(map[string]any{"session_name": "someone-else"})
	assert.Equal(t, http.StatusForbidden, code)
	assert.Equal(t, "session_mismatch", out["error"])

	for _, bad := range []string{"homelab/qa/agent", "homelab", "../qa", "Homelab/qa", "homelab/../x", "a%2fb/qa", "homelab/qa?x=1", "homelab/" + strings.Repeat("q", 65), "id_0/qa", ""} {
		code, out = e.send(map[string]any{"to_address": bad})
		assert.Equal(t, http.StatusBadRequest, code, bad)
		assert.Equal(t, "bad_address", out["error"], bad)
	}
	code, _ = e.send(map[string]any{"to_address": "id-0123456789ab/qa"})
	assert.Equal(t, http.StatusOK, code, "the id-<12 hex> form")

	code, _ = e.do("PUT", "/api/board/alpha/settings", map[string]any{"subscriber_id": "Operator", "cross_team_messaging": false}, nil)
	require.Equal(t, http.StatusOK, code)
	code, out = e.send(map[string]any{"to_address": "homelab/ops"})
	assert.Equal(t, http.StatusForbidden, code)
	assert.Equal(t, "cross_team_disabled", out["error"])
}

// Coral Hub's refusals reach the sender as their code (and Retry-After), never free text,
// and nothing is stored; without a hub link the answer is 503 not_linked.
func TestRelaySendMapsTheHubsAnswers(t *testing.T) {
	e := newSendEnv(t)
	before := len(e.alphaMessages())
	for _, tc := range []struct {
		status int
		body   string
		retry  int
		code   string
	}{
		{404, `{"ok":false,"error":"not_found"}`, 0, "not_found"},
		{503, `{"ok":false,"error":"target_offline"}`, 30, "target_offline"},
		{429, `{"ok":false,"error":"rate_limited"}`, 12, "rate_limited"},
		{403, `{"ok":false,"error":"target_refused","reason":"source_not_allowed"}`, 0, "target_refused:source_not_allowed"},
		{502, `{"ok":false,"error":"B said: <script>"}`, 0, "hub_error"},
	} {
		e.hub.answer = func(string, map[string]any) (tunnel.RelayResponse, error) {
			return tunnel.RelayResponse{Status: tc.status, Body: []byte(tc.body), RetryAfter: tc.retry}, nil
		}
		code, out := e.send(map[string]any{"to_address": "homelab/x" + tc.code[:2]})
		assert.Equal(t, tc.status, code, tc.code)
		assert.Equal(t, tc.code, out["error"], tc.code)
		if tc.retry > 0 {
			assert.NotEmpty(t, out["_retry_after"], tc.code)
		}
	}
	assert.Len(t, e.alphaMessages(), before, "nothing stored for a refusal")

	e.hub.answer = func(string, map[string]any) (tunnel.RelayResponse, error) {
		return tunnel.RelayResponse{}, cloud.ErrNotLinked
	}
	code, out := e.send(map[string]any{"to_address": "homelab/nl"})
	assert.Equal(t, http.StatusServiceUnavailable, code)
	assert.Equal(t, "not_linked", out["error"])
	e.hub.answer = func(string, map[string]any) (tunnel.RelayResponse, error) {
		return tunnel.RelayResponse{}, tunnel.ErrRelayUnsupported
	}
	code, out = e.send(map[string]any{"to_address": "homelab/un"})
	assert.Equal(t, http.StatusNotImplemented, code)
	assert.Equal(t, "relay_unsupported", out["error"])
}

// A retry with the same message_id that Coral Hub answers "duplicate" stores no second copy.
func TestRelaySendRetryStoresOneMirror(t *testing.T) {
	e := newSendEnv(t)
	code, first := e.send(nil)
	require.Equal(t, http.StatusOK, code, first)
	n := len(e.alphaMessages())
	e.hub.answer = func(string, map[string]any) (tunnel.RelayResponse, error) {
		return tunnel.RelayResponse{Status: 200, Body: []byte(`{"ok":true,"duplicate":true}`)}, nil
	}
	code, again := e.send(map[string]any{"message_id": first["message_id"]})
	require.Equal(t, http.StatusOK, code, again)
	assert.Equal(t, true, again["duplicate"])
	assert.Len(t, e.alphaMessages(), n, "no second copy")
	_, body := e.hub.last()
	assert.Equal(t, first["message_id"], body["message_id"], "the retry reuses the id")
}

// A reply to a message relayed from another Coral counts one more hop; past 3, refused.
// reply_to must name such a message on this board.
func TestRelaySendReplyCountsHops(t *testing.T) {
	e := newSendEnv(t)
	ctx := context.Background()
	in, _, err := e.bs.PostRelayedCrossTeamMessage(ctx, board.CrossTeamPost{
		Origin:   board.MessageOrigin{ServerID: "0b6c6a52-8a0e-4f4e-9d61-3f1e2a1b9c00", Board: "qa", Sender: "Orchestrator", SenderRole: "Orchestrator", TargetServerID: "local", TargetBoard: "alpha", RelayMessageID: "in-1", Hops: 1},
		Mentions: []string{"Orchestrator"}, Content: "can you check?",
	})
	require.NoError(t, err)
	code, out := e.send(map[string]any{"reply_to": in.ID})
	require.Equal(t, http.StatusOK, code, out)
	_, body := e.hub.last()
	assert.Equal(t, float64(2), body["hops"])

	deep, _, err := e.bs.PostRelayedCrossTeamMessage(ctx, board.CrossTeamPost{
		Origin:   board.MessageOrigin{ServerID: "0b6c6a52-8a0e-4f4e-9d61-3f1e2a1b9c00", Board: "qa", Sender: "Orchestrator", SenderRole: "Orchestrator", TargetServerID: "local", TargetBoard: "alpha", RelayMessageID: "in-3", Hops: 3},
		Mentions: []string{"Orchestrator"}, Content: "and again",
	})
	require.NoError(t, err)
	code, out = e.send(map[string]any{"reply_to": deep.ID})
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Equal(t, "too_many_hops", out["error"])

	code, out = e.send(map[string]any{"reply_to": 999999})
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Equal(t, "bad_request", out["error"])
}

// The lookup asks Coral Hub for this board's targets and passes on only well-formed ones.
func TestRelayTargetsLookup(t *testing.T) {
	e := newSendEnv(t)
	e.hub.answer = func(path string, body map[string]any) (tunnel.RelayResponse, error) {
		assert.Equal(t, tunnel.RelayTargetsPath, path)
		assert.Equal(t, map[string]any{"from_board": "alpha"}, body)
		return tunnel.RelayResponse{Status: 200, Body: []byte(`{"ok":true,"targets":[
			{"address":"homelab/qa","server":"homelab","team":"qa","reachable":true},
			{"address":"homelab/../x","server":"homelab","team":"../x"},
			{"address":"other/ops","server":"homelab","team":"ops"}]}`)}, nil
	}
	code, out := e.do("GET", "/api/board/alpha/relay-targets?subscriber_id=Orchestrator&session_name=tmux-alphaOrchestrator", nil, nil)
	require.Equal(t, http.StatusOK, code, out)
	targets, _ := out["targets"].([]any)
	require.Len(t, targets, 1, "only well-formed, consistent entries")
	assert.Equal(t, "homelab/qa", targets[0].(map[string]any)["address"])

	code, out = e.do("GET", "/api/board/alpha/relay-targets?subscriber_id=Dev&session_name=tmux-alphaDev", nil, nil)
	assert.Equal(t, http.StatusForbidden, code)
	assert.Equal(t, "not_orchestrator", out["error"])
}

// The Operator must be a person to message another Coral (the dashboard or the signed
// CLI): a tokenless local process claiming "Operator" is refused on both routes. An
// Orchestrator from its own session is unchanged.
func TestRelayOperatorMustBeAPerson(t *testing.T) {
	e := newSendEnv(t)
	op := map[string]any{"subscriber_id": "Operator", "content": "hi", "to_address": "homelab/qa"}
	code, out := e.do("POST", "/api/board/alpha/relay-messages", op, nil)
	assert.Equal(t, http.StatusForbidden, code)
	assert.Equal(t, "operator_requires_person", out["error"])
	code, out = e.do("GET", "/api/board/alpha/relay-targets?subscriber_id=Operator", nil, nil)
	assert.Equal(t, http.StatusForbidden, code)
	assert.Equal(t, "operator_requires_person", out["error"])

	// The dashboard's own request (as the identity observer classifies it) may.
	dash := chi.NewRouter()
	dash.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), callerKindKey{}, callerDashCookie)))
		})
	})
	dash.Post("/api/board/{project}/relay-messages", e.h.PostRelayMessage)
	srv := httptest.NewServer(dash)
	defer srv.Close()
	b, _ := json.Marshal(op)
	resp, err := http.Post(srv.URL+"/api/board/alpha/relay-messages", "application/json", bytes.NewReader(b))
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode, "the Operator in the dashboard")
}

// The lookup needs the board's cross-team opt-in too; a board name that can't be an
// address team is refused up front; and a send that can't reach Coral Hub doesn't use the
// sender's quota.
func TestRelayOptInNameAndQuota(t *testing.T) {
	e := newSendEnv(t)
	code, _ := e.do("PUT", "/api/board/alpha/settings", map[string]any{"subscriber_id": "Operator", "cross_team_messaging": false}, nil)
	require.Equal(t, http.StatusOK, code)
	code, out := e.do("GET", "/api/board/alpha/relay-targets?subscriber_id=Orchestrator&session_name=tmux-alphaOrchestrator", nil, nil)
	assert.Equal(t, http.StatusForbidden, code)
	assert.Equal(t, "cross_team_disabled", out["error"])
	code, _ = e.do("PUT", "/api/board/alpha/settings", map[string]any{"subscriber_id": "Operator", "cross_team_messaging": true}, nil)
	require.Equal(t, http.StatusOK, code)

	code, out = e.do("POST", "/api/board/bad%20name/relay-messages", map[string]any{"subscriber_id": "Orchestrator", "content": "hi", "to_address": "homelab/qa"}, nil)
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Equal(t, "bad_request", out["error"])

	e.hub.answer = func(string, map[string]any) (tunnel.RelayResponse, error) {
		return tunnel.RelayResponse{}, cloud.ErrNotLinked
	}
	for i := 0; i < crossTeamBurst+2; i++ {
		code, out = e.send(nil)
		require.Equal(t, http.StatusServiceUnavailable, code, out)
	}
	e.hub.answer = func(string, map[string]any) (tunnel.RelayResponse, error) {
		return tunnel.RelayResponse{Status: 200, Body: []byte(`{"ok":true,"target_message_id":"1"}`)}, nil
	}
	code, out = e.send(nil)
	assert.Equal(t, http.StatusOK, code, "retries while disconnected didn't use the quota: %v", out)
}
