package routes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
	"github.com/subgentic/coral/pkg/tunnel"
)

// #4510: Save for Later. The bookmarks are the Operator's; a message has a stable key; the snippet kept is redacted.

type savedRig struct {
	t   *testing.T
	srv *httptest.Server
	h   *SystemHandler
	db  *store.DB
}

func newSavedRig(t *testing.T) *savedRig {
	t.Helper()
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "saved.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	cfg := &config.Config{LogDir: t.TempDir()}
	cfg.UseCoralDir(t.TempDir())
	h := NewSystemHandler(db, cfg)
	r := chi.NewRouter()
	ident := testIdentity(db)
	r.Use(func(next http.Handler) http.Handler {
		withIdent := ident(next)
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.Header.Get(testDashboardHeader) != "" {
				next.ServeHTTP(w, req.WithContext(context.WithValue(req.Context(), callerKindKey{}, callerDashCookie)))
				return
			}
			withIdent.ServeHTTP(w, req)
		})
	})
	r.Get("/api/saved", h.SavedList)
	r.Post("/api/saved", h.SavedAdd)
	r.Get("/api/saved/{id}/message", h.SavedMessage)
	r.Patch("/api/saved/{id}", h.SavedUpdate)
	r.Delete("/api/saved/{id}", h.SavedDelete)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return &savedRig{t: t, srv: srv, h: h, db: db}
}

func (g *savedRig) do(method, path string, hdr map[string]string, body any) (int, map[string]any) {
	g.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, g.srv.URL+path, rd)
	require.NoError(g.t, err)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(g.t, err)
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

var savedAsOperator = map[string]string{testDashboardHeader: "1"}

func agentSave(key, snippet string) map[string]any {
	return map[string]any{"kind": "agent", "ref": "sess-1", "key": key, "agent_type": "claude", "title": "ro-a", "snippet": snippet, "said_at": "2026-10-17T08:00:00Z"}
}

func TestSavedMessagesAreTheOperatorsAndKeptOnce(t *testing.T) {
	g := newSavedRig(t)
	k1, k2 := strings.Repeat("a1", 10), strings.Repeat("b2", 10)

	// An agent, a script and a forged Coral Hub stamp are refused on every route.
	for name, hdr := range map[string]map[string]string{"an agent's token": {testAgentHeader: "s1"}, "a script": {}, "a forged hub stamp": {tunnel.ViaHeader: "x"}} {
		for _, c := range []struct{ m, p string }{{"GET", "/api/saved"}, {"POST", "/api/saved"}, {"PATCH", "/api/saved/1"}, {"DELETE", "/api/saved/1"}} {
			code, out := g.do(c.m, c.p, hdr, agentSave(k1, "x"))
			assert.Equal(t, http.StatusForbidden, code, name+" "+c.m+" "+c.p)
			want := "operator_only"
			if name == "a forged hub stamp" {
				want = "owner_only" // every saved route is the Hub owner's alone through the hub (#5753, #5763): a stamp that is not the connector's is a hub request with no owner
			}
			assert.Equal(t, want, out["code"], name)
		}
	}
	_, list := g.do("GET", "/api/saved", savedAsOperator, nil)
	assert.Empty(t, list["items"], "nothing saved by the refused callers")

	// Saving: the bookmark, the same message twice is one, newest first, done and open again, remove.
	code, a := g.do("POST", "/api/saved", savedAsOperator, agentSave(k1, "first message"))
	require.Equal(t, http.StatusOK, code, a)
	assert.Equal(t, "agent", a["kind"])
	assert.Equal(t, "sess-1", a["ref"])
	assert.Equal(t, k1, a["key"])
	assert.Equal(t, "ro-a", a["title"])
	assert.NotEmpty(t, a["saved_at"])
	assert.Nil(t, a["done_at"])
	code, again := g.do("POST", "/api/saved", savedAsOperator, agentSave(k1, "first message"))
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, a["id"], again["id"], "saving the same message again is the same bookmark")
	code, b := g.do("POST", "/api/saved", savedAsOperator, map[string]any{"kind": "board", "ref": "team-x", "key": "1234", "title": "team-x", "snippet": "a board message", "said_at": "2026-10-17T09:00:00Z"})
	require.Equal(t, http.StatusOK, code, b)
	_, c := g.do("POST", "/api/saved", savedAsOperator, agentSave(k2, "second message"))

	_, list = g.do("GET", "/api/saved", savedAsOperator, nil)
	items := list["items"].([]any)
	require.Len(t, items, 3)
	assert.Equal(t, c["id"], items[0].(map[string]any)["id"], "newest saved first")
	assert.EqualValues(t, 3, list["open"])
	assert.EqualValues(t, 0, list["done"])
	_, onlyAgent := g.do("GET", "/api/saved?kind=agent&conv=sess-1", savedAsOperator, nil)
	assert.Len(t, onlyAgent["items"], 2, "one conversation's bookmarks")

	_, counts := g.do("GET", "/api/saved?limit=0", savedAsOperator, nil)
	assert.Empty(t, counts["items"], "limit=0 is the counts only")
	assert.EqualValues(t, 3, counts["open"])
	_, newest := g.do("GET", "/api/saved?limit=1", savedAsOperator, nil)
	require.Len(t, newest["items"], 1)
	assert.Equal(t, c["id"], newest["items"].([]any)[0].(map[string]any)["id"], "the newest one")
	code, _ = g.do("GET", "/api/saved?limit=-1", savedAsOperator, nil)
	assert.Equal(t, http.StatusBadRequest, code)

	id := fmt.Sprint(int64(a["id"].(float64)))
	code, done := g.do("PATCH", "/api/saved/"+id, savedAsOperator, map[string]any{"done": true})
	require.Equal(t, http.StatusOK, code, done)
	assert.NotNil(t, done["done_at"])
	_, open := g.do("GET", "/api/saved?state=open", savedAsOperator, nil)
	_, doneList := g.do("GET", "/api/saved?state=done", savedAsOperator, nil)
	assert.Len(t, open["items"], 2)
	assert.Len(t, doneList["items"], 1)
	assert.EqualValues(t, 2, doneList["open"])
	assert.EqualValues(t, 1, doneList["done"])
	_, reopened := g.do("PATCH", "/api/saved/"+id, savedAsOperator, map[string]any{"done": false})
	assert.Nil(t, reopened["done_at"])

	code, _ = g.do("DELETE", "/api/saved/"+id, savedAsOperator, nil)
	assert.Equal(t, http.StatusOK, code)
	code, _ = g.do("DELETE", "/api/saved/"+id, savedAsOperator, nil)
	assert.Equal(t, http.StatusNotFound, code, "already gone")
	code, _ = g.do("PATCH", "/api/saved/"+id, savedAsOperator, map[string]any{"done": true})
	assert.Equal(t, http.StatusNotFound, code)

	// Bad input.
	for name, body := range map[string]map[string]any{
		"a kind that is neither":   {"kind": "chat", "ref": "x", "key": k1},
		"an agent key too short":   {"kind": "agent", "ref": "x", "key": "abc"},
		"an agent key not hex":     {"kind": "agent", "ref": "x", "key": strings.Repeat("z", 20)},
		"a board key not a number": {"kind": "board", "ref": "x", "key": "abc"},
		"no ref":                   {"kind": "agent", "ref": "  ", "key": k1},
	} {
		code, _ := g.do("POST", "/api/saved", savedAsOperator, body)
		assert.Equal(t, http.StatusBadRequest, code, name)
	}
	code, _ = g.do("GET", "/api/saved?state=weird", savedAsOperator, nil)
	assert.Equal(t, http.StatusBadRequest, code)
	code, _ = g.do("PATCH", "/api/saved/abc", savedAsOperator, map[string]any{"done": true})
	assert.Equal(t, http.StatusBadRequest, code)
	code, _ = g.do("PATCH", "/api/saved/"+fmt.Sprint(int64(b["id"].(float64))), savedAsOperator, map[string]any{})
	assert.Equal(t, http.StatusBadRequest, code, "done is required")
}

func TestSavedSnippetIsRedactedOneLineAndShort(t *testing.T) {
	g := newSavedRig(t)
	token := "ghp_" + strings.Repeat("A1b2C3d4E5", 4)
	text := "use the token " + token + "\nthen\n\n   run it " + strings.Repeat("word ", 100)
	code, out := g.do("POST", "/api/saved", savedAsOperator, agentSave(strings.Repeat("c3", 10), text))
	require.Equal(t, http.StatusOK, code, out)
	snip := out["snippet"].(string)
	assert.NotContains(t, snip, token, "a token in the message is not kept")
	assert.Contains(t, snip, "[redacted token]")
	assert.NotContains(t, snip, "\n", "one line")
	assert.LessOrEqual(t, len(snip), maxSavedSnippet+len("…"), "short")
	assert.True(t, strings.HasSuffix(snip, "…"), "cut text says so")
	_, list := g.do("GET", "/api/saved", savedAsOperator, nil)
	raw, _ := json.Marshal(list)
	assert.NotContains(t, string(raw), token, "nor in the list")
}

func TestSavedRoutesAreAtTheTunnelsTiers(t *testing.T) {
	// The tunnel's table decides what Coral Hub may do: the list at View, the changes at Interact.
	for _, c := range []struct {
		m, p string
		want tunnel.Tier
	}{
		{"GET", "/api/saved", tunnel.TierView},
		{"GET", "/api/saved?state=open&kind=agent&conv=abc", tunnel.TierView},
		{"GET", "/api/saved?q=parser&sort=oldest&limit=20", tunnel.TierView},
		{"POST", "/api/saved", tunnel.TierInteract},
		{"PATCH", "/api/saved/1", tunnel.TierInteract},
		{"DELETE", "/api/saved/1", tunnel.TierInteract},
	} {
		path, query, _ := strings.Cut(c.p, "?")
		assert.Equal(t, c.want, tunnel.Classify(c.m, path, query), c.m+" "+c.p)
	}
	assert.Equal(t, tunnel.TierNever, tunnel.Classify("GET", "/api/saved", "whatever=1"), "a query parameter the handler does not read is refused")
	assert.Equal(t, tunnel.TierNever, tunnel.Classify("PUT", "/api/saved/1", ""), "no other method")
}

func TestMessageKeyIsStableAndDistinguishes(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	a := map[string]any{"type": "assistant", "timestamp": "2026-10-17T08:00:00Z", "text": "hello", "tool_uses": []map[string]any{{"id": "t1", "name": "Read"}}}
	b := map[string]any{"type": "assistant", "timestamp": "2026-10-17T08:00:00Z", "text": "hello", "tool_uses": []any{map[string]any{"id": "t1", "name": "Read"}}}
	assert.Equal(t, messageKey(secret, a), messageKey(secret, b), "the same message, however its tool list is typed")
	assert.Regexp(t, `^[0-9a-f]{20}$`, messageKey(secret, a))
	for name, c := range map[string]map[string]any{
		"another time": {"type": "assistant", "timestamp": "2026-10-17T08:00:01Z", "text": "hello"},
		"another text": {"type": "assistant", "timestamp": "2026-10-17T08:00:00Z", "text": "hello!"},
		"another type": {"type": "user", "timestamp": "2026-10-17T08:00:00Z", "content": "hello"},
		"a result":     {"type": "tool_result", "timestamp": "2026-10-17T08:00:00Z", "content": "hello", "tool_use_id": "t1"},
	} {
		assert.NotEqual(t, messageKey(secret, a), messageKey(secret, c), name)
	}
	orig := []map[string]any{a}
	keyed := withKeys(secret, orig)
	assert.Equal(t, messageKey(secret, a), keyed[0]["key"])
	_, polluted := a["key"]
	assert.False(t, polluted, "the reader's own message is not written to")
}

// plainKey is what the key used to be (a plain SHA-256 of the same fields): anyone who knows a message's text and time could compute it.
func plainKey(m map[string]any) string {
	h := sha256.New()
	str := func(v any) string { s, _ := v.(string); return s }
	for _, k := range []string{"type", "timestamp", "content", "text", "tool_use_id", "tool_name"} {
		h.Write([]byte(k))
		h.Write([]byte{0})
		h.Write([]byte(str(m[k])))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:20]
}

// The key is keyed (Security's #4510 finding): through Coral Hub at View a reply's text is redacted but its key is not, and a plain hash let a viewer test a guess for
// the redacted value offline. With a per-install secret that check needs the secret.
func TestMessageKeyIsKeyedByAPerInstallSecret(t *testing.T) {
	msg := map[string]any{"type": "user", "timestamp": "2026-10-17T08:00:00Z", "content": "my db password is hunter2"}
	guess := map[string]any{"type": "user", "timestamp": "2026-10-17T08:00:00Z", "content": "my db password is hunter2"}
	wrong := map[string]any{"type": "user", "timestamp": "2026-10-17T08:00:00Z", "content": "my db password is hunter3"}
	dirA, dirB := t.TempDir(), t.TempDir()
	cfgA, cfgB := &config.Config{}, &config.Config{}
	cfgA.UseCoralDir(dirA)
	cfgB.UseCoralDir(dirB)
	a, b := messageKeySecretFor(cfgA), messageKeySecretFor(cfgB)
	assert.NotEqual(t, a, b, "each installation has its own secret")
	assert.Len(t, a, 32)
	assert.NotEqual(t, plainKey(msg), messageKey(a, msg), "the key is not the plain hash of the text: knowing the text and time is not enough to compute it")
	assert.NotEqual(t, messageKey(a, msg), messageKey(b, msg), "another installation's key for the same message differs")
	assert.Equal(t, messageKey(a, msg), messageKey(a, guess), "the same text gives the same key under one secret")
	assert.NotEqual(t, messageKey(a, msg), messageKey(a, wrong))
	// find_key answers only for a key the caller got from a reply: a plain hash of a candidate finds nothing.
	list := []map[string]any{msg}
	assert.Equal(t, 0, findKey(a, list, messageKey(a, msg)))
	assert.Equal(t, -1, findKey(a, list, plainKey(guess)), "a guess's plain hash is not a key")

	// Kept: the same secret again from the file (a restart, a handler made again), mode 0600, 32 bytes of hex.
	keySecretMu.Lock()
	delete(keySecrets, dirA)
	keySecretMu.Unlock()
	assert.Equal(t, a, messageKeySecretFor(cfgA), "read back from the data directory")
	assert.Equal(t, messageKey(a, msg), messageKey(messageKeySecretFor(cfgA), msg), "same key after a restart")
	st, err := os.Stat(filepath.Join(dirA, messageKeySecretFile))
	require.NoError(t, err)
	if runtime.GOOS != "windows" {
		assert.Equal(t, os.FileMode(0o600), st.Mode().Perm())
	}
	raw, _ := os.ReadFile(filepath.Join(dirA, messageKeySecretFile))
	assert.Equal(t, hex.EncodeToString(a), strings.TrimSpace(string(raw)))

	// An unreadable secret file is not used: the process gets one of its own and the file is left as it was (no key is made from a bad file).
	dirC := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dirC, messageKeySecretFile), []byte("not hex"), 0o600))
	cfgC := &config.Config{}
	cfgC.UseCoralDir(dirC)
	c := messageKeySecretFor(cfgC)
	assert.Len(t, c, 32)
	assert.NotEqual(t, a, c)
	raw, _ = os.ReadFile(filepath.Join(dirC, messageKeySecretFile))
	assert.Equal(t, "not hex", string(raw), "the bad file is not overwritten")
}

// The chat route and the history route carry each message's key, the same for the same message whatever the page.
func TestChatMessagesCarryStableKeysAcrossPages(t *testing.T) {
	server, _, _, _ := setupSessionsTestServer(t)
	dir := t.TempDir()
	projectDir := filepath.Join(dir, "project")
	require.NoError(t, os.MkdirAll(projectDir, 0755))
	t.Setenv("CLAUDE_PROJECTS_DIR", dir)
	sid := "00000000-0000-0000-0000-000000000510"
	var lines strings.Builder
	for i := range 8 {
		fmt.Fprintf(&lines, "{\"type\":\"user\",\"timestamp\":\"2026-10-17T08:00:%02dZ\",\"message\":{\"content\":\"m%d\"}}\n", i, i)
	}
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, sid+".jsonl"), []byte(lines.String()), 0644))
	keys := func(query string) map[string]string {
		t.Helper()
		resp, err := http.Get(fmt.Sprintf("%s/api/sessions/live/claude-%s/chat?session_id=%s&after=0&%s", server.URL, sid, sid, query))
		require.NoError(t, err)
		defer resp.Body.Close()
		var body struct {
			Messages []map[string]any `json:"messages"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		out := map[string]string{}
		for _, m := range body.Messages {
			k, _ := m["key"].(string)
			require.Regexp(t, `^[0-9a-f]{20}$`, k)
			out[fmt.Sprint(m["content"])] = k
		}
		return out
	}
	all := keys("limit=100")
	require.Len(t, all, 8)
	small := keys("limit=3&offset=0")
	older := keys("limit=3&before=5")
	for text, k := range small {
		assert.Equal(t, all[text], k, "the key of "+text+" does not depend on the page")
	}
	for text, k := range older {
		assert.Equal(t, all[text], k, "nor on a page above")
	}
	distinct := map[string]bool{}
	for _, k := range all {
		distinct[k] = true
	}
	assert.Len(t, distinct, 8, "every message has its own key")

	// ?find_key: is the message still there, and where.
	lookup := func(key string) (found bool, index, total int) {
		t.Helper()
		resp, err := http.Get(fmt.Sprintf("%s/api/sessions/live/claude-%s/chat?session_id=%s&find_key=%s", server.URL, sid, sid, key))
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body struct {
			Found bool `json:"found"`
			Index int  `json:"index"`
			Total int  `json:"total"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		return body.Found, body.Index, body.Total
	}
	found, index, total := lookup(all["m3"])
	assert.True(t, found)
	assert.Equal(t, 3, index)
	assert.Equal(t, 8, total)
	found, _, total = lookup(strings.Repeat("0", 20))
	assert.False(t, found, "a key the transcript does not have")
	assert.Equal(t, 8, total)
}

// The History view finds a saved message by its key too, and says so when the transcript no longer has it.
func TestHistoryDetailFindsAMessageByItsKey(t *testing.T) {
	_, h, ss, codexHome, _, _ := setupHistoryVerificationServer(t)
	ctx := context.Background()
	dayDir := filepath.Join(codexHome, "sessions", "2026", "10", "16")
	require.NoError(t, os.MkdirAll(dayDir, 0755))
	sid := "f6a7b8c9-d0e1-2345-f012-3456789abc99"
	var b strings.Builder
	b.WriteString(`{"timestamp":"2026-10-16T10:00:00.000Z","type":"session_meta","payload":{"id":"rollout-keys","cwd":"/repo"}}` + "\n")
	fmt.Fprintf(&b, `{"timestamp":"2026-10-16T10:00:00.100Z","type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"Coral session metadata:\n%s %s\n"}]}}`+"\n", at.CoralSessionMarkerPrefix, sid)
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&b, `{"timestamp":"2026-10-16T10:00:%02d.000Z","type":"event_msg","payload":{"type":"user_message","message":"message %d","images":[]}}`+"\n", i, i)
	}
	rollout := filepath.Join(dayDir, "rollout-keys.jsonl")
	require.NoError(t, os.WriteFile(rollout, []byte(b.String()), 0644))
	require.NoError(t, ss.UpsertSessionIndex(ctx, &store.SessionIndex{SessionID: sid, SourceType: "codex", SourceFile: rollout}))
	r := chi.NewRouter()
	r.Get("/api/sessions/history/{sessionID}", h.GetSessionDetail)
	get := func(q string) map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sessions/history/"+sid+q, nil))
		require.Equal(t, http.StatusOK, rec.Code, q)
		var out map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		return out
	}
	page := get("?limit=100")
	msgs := page["messages"].([]any)
	var key string
	for i, m := range msgs {
		if m.(map[string]any)["content"] == "message 12" {
			key = m.(map[string]any)["key"].(string)
			require.Regexp(t, `^[0-9a-f]{20}$`, key)
			_ = i
		}
	}
	require.NotEmpty(t, key)
	out := get("?find_key=" + key)
	assert.Equal(t, true, out["found"])
	idx := int(out["index"].(float64))
	assert.Equal(t, "message 12", msgs[idx].(map[string]any)["content"], "the index is the message's place in the transcript")
	assert.EqualValues(t, len(msgs), out["total"])
	assert.Equal(t, false, get("?find_key=" + strings.Repeat("f", 20))["found"])
}

// A hub reaches a registered server with that server's API key and nothing of the browser (#4551): the key is the Operator's, so the saved messages
// answer it; an agent's token is not, even with the key beside it, and a bare script (no credential) is not either.
func TestSavedMessagesAnswerTheServersAPIKeyButNeverAnAgent(t *testing.T) {
	g := newSavedRig(t)
	key := strings.Repeat("c3", 10)
	viaKey := map[string]string{"Authorization": "Bearer the-servers-key"} // the identity layer classes it api_key (it refuses a wrong key before any handler)

	code, a := g.do("POST", "/api/saved", viaKey, agentSave(key, "saved through a hub"))
	require.Equal(t, http.StatusOK, code, a)
	id := int(a["id"].(float64))
	code, list := g.do("GET", "/api/saved", viaKey, nil)
	require.Equal(t, http.StatusOK, code)
	assert.Len(t, list["items"], 1)
	code, _ = g.do("PATCH", "/api/saved/"+strconv.Itoa(id), viaKey, map[string]any{"done": true})
	assert.Equal(t, http.StatusOK, code, "Done through the key")
	code, _ = g.do("DELETE", "/api/saved/"+strconv.Itoa(id), viaKey, nil)
	assert.Equal(t, http.StatusOK, code, "Remove through the key")

	both := map[string]string{"Authorization": "Bearer the-servers-key", testAgentHeader: "s1"}
	for _, c := range []struct{ m, p string }{{"GET", "/api/saved"}, {"POST", "/api/saved"}, {"PATCH", "/api/saved/1"}, {"DELETE", "/api/saved/1"}} {
		code, out := g.do(c.m, c.p, both, agentSave(key, "x"))
		assert.Equal(t, http.StatusForbidden, code, "an agent token with a key beside it: "+c.m+" "+c.p)
		assert.Equal(t, "operator_only", out["code"])
		code, _ = g.do(c.m, c.p, map[string]string{}, agentSave(key, "x"))
		assert.Equal(t, http.StatusForbidden, code, "no credential: "+c.m+" "+c.p)
	}
}

// #4745: GET /api/saved?q=: the bookmarks whose title or snippet holds a plain, case-insensitive substring, newest first (sort=oldest the other
// way), with the total of what matched, and through Coral Hub the match is on what the viewer sees.
func TestSavedSearch(t *testing.T) {
	const tok = "ghp_aB3dE5fG7hI9jK1lM3nO5pQ7rS9tU1vW3xY5"
	g := newSavedRig(t)
	save := func(kind, ref, key, title, snippet string) {
		t.Helper()
		code, out := g.do("POST", "/api/saved", savedAsOperator, map[string]any{"kind": kind, "ref": ref, "key": key, "agent_type": "claude", "title": title, "snippet": snippet, "said_at": "2026-10-17T08:00:00Z"})
		require.Equal(t, http.StatusOK, code, out)
	}
	save("agent", "sess-1", strings.Repeat("a1", 10), "ro-a", "Fix the Parser before Friday")
	save("agent", "sess-1", strings.Repeat("b2", 10), "ro-a", "100% of tests pass")
	save("board", "team-x", "1001", "team-x", "deploy plan for the parser")
	save("agent", "sess-2", strings.Repeat("c3", 10), "key "+tok, "an unrelated note")
	titles := func(out map[string]any) []string {
		var ts []string
		for _, it := range out["items"].([]any) {
			ts = append(ts, it.(map[string]any)["snippet"].(string))
		}
		return ts
	}
	get := func(query string, hdr map[string]string) (int, map[string]any) {
		return g.do("GET", "/api/saved?"+query, hdr, nil)
	}
	code, out := get("q=PARSER", savedAsOperator)
	require.Equal(t, 200, code)
	assert.Equal(t, []string{"deploy plan for the parser", "Fix the Parser before Friday"}, titles(out), "newest saved first, any case")
	assert.EqualValues(t, 2, out["total"])
	assert.EqualValues(t, 4, out["open"], "the counts are of all bookmarks")
	code, out = get("q=parser&sort=oldest", savedAsOperator)
	assert.Equal(t, []string{"Fix the Parser before Friday", "deploy plan for the parser"}, titles(out))
	code, out = get("q=parser&sort=oldest&limit=1", savedAsOperator)
	assert.Equal(t, []string{"Fix the Parser before Friday"}, titles(out), "limit keeps the first of the sort's order")
	assert.EqualValues(t, 2, out["total"], "the total is every match")
	code, out = get("q=TEAM-X", savedAsOperator)
	assert.Equal(t, []string{"deploy plan for the parser"}, titles(out), "the title is searched too")
	code, out = get("q="+url.QueryEscape("100%"), savedAsOperator)
	assert.Equal(t, []string{"100% of tests pass"}, titles(out), "% is an ordinary character")
	code, out = get("q=_", savedAsOperator)
	assert.Equal(t, []string{"an unrelated note"}, titles(out), "_ is an ordinary character too: only the title with an underscore in it")
	code, out = get("q=parser&kind=agent&conv=sess-1", savedAsOperator)
	assert.Equal(t, []string{"Fix the Parser before Friday"}, titles(out), "with the other filters")
	code, out = get("", savedAsOperator)
	assert.EqualValues(t, 4, out["total"], "a reply with no q has its total too")
	// refusals
	for query, want := range map[string]string{"q=" + strings.Repeat("x", 201): "q is at most 200", "q=a&q=b": "given once", "sort=name": "unknown sort", "q=a%00b": "without control characters"} {
		code, out = get(query, savedAsOperator)
		assert.Equal(t, 400, code, query)
		assert.Contains(t, fmt.Sprint(out["error"]), want, query)
	}
	// through the hub: the Operator's own request with the connector's stamp; a q holding a secret is refused, the marker finds the title
	// (the list is the Hub owner's alone since #5753: the owner's request, with the connector's stamp, the owner marker and the enrolled label)
	withLabel(t, cmtHubUser, nil)
	hub := cmtAsHubOwner
	for name, hdr := range map[string]map[string]string{"a hub viewer": cmtAsHub, "the owner's credential on a request with no owner marker": cmtAsOwnerThroughTunnel} {
		code, out = get("q=parser", hdr)
		assert.Equal(t, 403, code, name)
		assert.Equal(t, "owner_only", out["code"], name)
	}
	code, out = get("q="+tok, hub)
	assert.Equal(t, 400, code)
	assert.Contains(t, fmt.Sprint(out["error"]), "q can't contain a secret")
	code, out = get("q=redacted", hub)
	assert.Equal(t, 200, code)
	assert.EqualValues(t, 1, out["total"], "the viewer sees [redacted token] in the title")
	code, out = get("q=parser", hub)
	assert.EqualValues(t, 2, out["total"])
	code, out = get("q="+tok, savedAsOperator)
	assert.EqualValues(t, 1, out["total"], "a local caller searches the stored title")
	code, out = get("q=redacted", savedAsOperator)
	assert.EqualValues(t, 0, out["total"])
}

// #4792: Saved for later is scoped to where it is opened from: an agent, a team, or everything. The server matches the scope (not the dashboard over a capped list), and the counts are of the scope.
func TestSavedScope(t *testing.T) {
	g := newSavedRig(t)
	save := func(body map[string]any) {
		t.Helper()
		body["agent_type"], body["said_at"] = "claude", "2026-10-17T08:00:00Z"
		code, out := g.do("POST", "/api/saved", savedAsOperator, body)
		require.Equal(t, http.StatusOK, code, out)
	}
	// two teams, an agent on each (the QA Engineer and the Frontend Dev), and the board messages each wrote or others did
	save(map[string]any{"kind": "agent", "ref": "sess-qa", "key": strings.Repeat("a1", 10), "title": "QA Engineer", "snippet": "qa chat one", "team": "coral-go-team"})
	save(map[string]any{"kind": "agent", "ref": "sess-qa", "key": strings.Repeat("a2", 10), "title": "QA Engineer", "snippet": "qa chat two", "team": "coral-go-team"})
	save(map[string]any{"kind": "agent", "ref": "sess-fe", "key": strings.Repeat("b1", 10), "title": "Frontend Dev", "snippet": "fe chat", "team": "coral-hub-team"})
	save(map[string]any{"kind": "board", "ref": "coral-go-team", "key": "11", "title": "coral-go-team", "snippet": "qa on the board", "author": "QA Engineer", "team": "ignored"})
	save(map[string]any{"kind": "board", "ref": "coral-go-team", "key": "12", "title": "coral-go-team", "snippet": "someone else on the board", "author": "Orchestrator"})
	save(map[string]any{"kind": "board", "ref": "coral-hub-team", "key": "13", "title": "coral-hub-team", "snippet": "fe on the hub board", "author": "Frontend Dev"})
	save(map[string]any{"kind": "board", "ref": "coral-hub-team", "key": "14", "title": "coral-hub-team", "snippet": "a QA Engineer of another team", "author": "QA Engineer"})
	// a mark done, to see the counts
	_, all := g.do("GET", "/api/saved?state=open", savedAsOperator, nil)
	var doneID int64
	for _, it := range all["items"].([]any) {
		if m := it.(map[string]any); m["snippet"] == "qa chat two" {
			doneID = int64(m["id"].(float64))
		}
	}
	code, _ := g.do("PATCH", fmt.Sprintf("/api/saved/%d", doneID), savedAsOperator, map[string]any{"done": true})
	require.Equal(t, 200, code)
	snippets := func(out map[string]any) []string {
		var ts []string
		for _, it := range out["items"].([]any) {
			ts = append(ts, it.(map[string]any)["snippet"].(string))
		}
		slices.Sort(ts)
		return ts
	}
	get := func(query string) map[string]any {
		t.Helper()
		code, out := g.do("GET", "/api/saved?"+query, savedAsOperator, nil)
		require.Equal(t, 200, code, out)
		return out
	}
	home := get("")
	assert.Len(t, home["items"], 7, "home: everything")
	assert.EqualValues(t, 6, home["open"])
	assert.EqualValues(t, 1, home["done"])
	team := get("team=coral-go-team")
	assert.Equal(t, []string{"qa chat one", "qa chat two", "qa on the board", "someone else on the board"}, snippets(team), "a team: its board's messages and its agents' chats")
	assert.EqualValues(t, 3, team["open"], "the counts are the team's")
	assert.EqualValues(t, 1, team["done"])
	other := get("team=coral-hub-team")
	assert.Equal(t, []string{"a QA Engineer of another team", "fe chat", "fe on the hub board"}, snippets(other))
	agent := get("agent=sess-qa&team=coral-go-team&author=" + url.QueryEscape("QA Engineer"))
	assert.Equal(t, []string{"qa chat one", "qa chat two", "qa on the board"}, snippets(agent), "an agent: its chat and the board messages it wrote, not another team's namesake")
	assert.EqualValues(t, 2, agent["open"])
	assert.EqualValues(t, 1, agent["done"])
	assert.Equal(t, []string{"fe chat"}, snippets(get("agent=sess-fe")), "an agent alone (no board): its chat only")
	assert.EqualValues(t, 1, get("agent=sess-fe")["open"])
	assert.Equal(t, []string{"qa chat one"}, snippets(get("team=coral-go-team&q=chat+one")), "the scope with a search")
	assert.EqualValues(t, 3, get("team=coral-go-team&q=chat+one&state=done&limit=0")["open"], "the counts ignore the search and the state")
	assert.Empty(t, snippets(get("team=no-such-team")))
	// refusals
	for query, want := range map[string]string{"author=QA": "author goes with agent and team", "agent=a&author=b": "author goes with agent and team", "team=a&team=b": "given once",
		"team=a%00b": "without control characters", "agent=" + strings.Repeat("x", 301): "at most 300"} {
		code, out := g.do("GET", "/api/saved?"+query, savedAsOperator, nil)
		assert.Equal(t, 400, code, query)
		assert.Contains(t, fmt.Sprint(out["error"]), want, query)
	}
}

// Bookmarks saved before the team and the author were kept: a board bookmark belongs to its board's team, and its author is read from the board when an agent's scope asks (once).
func TestSavedScopeOfBookmarksFromBeforeTheyWereKept(t *testing.T) {
	g := newSavedRig(t)
	for _, k := range []string{"21", "22", "23"} {
		code, out := g.do("POST", "/api/saved", savedAsOperator, map[string]any{"kind": "board", "ref": "old-team", "key": k, "title": "old-team", "snippet": "old " + k})
		require.Equal(t, http.StatusOK, code, out)
	}
	asked := map[int64]int{}
	g.h.SetBoardAuthor(func(_ context.Context, project string, id int64) (string, bool, error) {
		asked[id]++
		assert.Equal(t, "old-team", project)
		switch id {
		case 21:
			return "QA Engineer", true, nil
		case 22:
			return "Orchestrator", true, nil
		}
		return "", false, nil // 23: the message was deleted
	})
	_, out := g.do("GET", "/api/saved?team=old-team", savedAsOperator, nil)
	assert.Len(t, out["items"], 3, "a board bookmark belongs to its board's team with no lookup")
	assert.Empty(t, asked, "a team scope asks the board nothing")
	q := "/api/saved?agent=sess-qa&team=old-team&author=" + url.QueryEscape("QA Engineer")
	_, out = g.do("GET", q, savedAsOperator, nil)
	require.Len(t, out["items"], 1)
	assert.Equal(t, "old 21", out["items"].([]any)[0].(map[string]any)["snippet"])
	assert.Equal(t, "QA Engineer", out["items"].([]any)[0].(map[string]any)["author"])
	_, out = g.do("GET", q, savedAsOperator, nil)
	assert.Len(t, out["items"], 1)
	assert.Equal(t, map[int64]int{21: 1, 22: 1, 23: 1}, asked, "each message is asked once, the deleted one too")
}

// #4816 (Security): who wrote a board message is the board's word, not the request's. A client that labels a bookmark with another agent's
// name would put it in that agent's view (the agent, team and author scope), so with the board wired the request's author is not kept.
func TestSavedBoardAuthorIsTheBoardsNotTheRequests(t *testing.T) {
	g := newSavedRig(t)
	g.h.SetBoardAuthor(func(_ context.Context, project string, id int64) (string, bool, error) {
		assert.Equal(t, "coral-go-team", project)
		switch id {
		case 11:
			return "QA Engineer", true, nil
		case 12:
			return "", false, errors.New("the board is busy")
		}
		return "", false, nil // 13: no such message
	})
	save := func(key, claimed string) {
		t.Helper()
		code, out := g.do("POST", "/api/saved", savedAsOperator, map[string]any{"kind": "board", "ref": "coral-go-team", "key": key, "title": "coral-go-team",
			"snippet": "message " + key, "author": claimed, "agent_type": "claude", "said_at": "2026-10-17T08:00:00Z"})
		require.Equal(t, http.StatusOK, code, out)
	}
	save("11", "Orchestrator") // written by the QA Engineer, labelled as the Orchestrator's
	save("12", "Orchestrator") // the board can't be read just now
	save("13", "Orchestrator") // no such message
	view := func(author string) []string {
		t.Helper()
		_, out := g.do("GET", "/api/saved?agent=sess-x&team=coral-go-team&author="+url.QueryEscape(author), savedAsOperator, nil)
		var got []string
		for _, it := range out["items"].([]any) {
			got = append(got, it.(map[string]any)["snippet"].(string))
		}
		return got
	}
	assert.Equal(t, []string{"message 11"}, view("QA Engineer"), "its author is the board's")
	assert.Empty(t, view("Orchestrator"), "the request's label is in nobody's view: not the unreadable one, not the missing one")
}

// A board bookmark whose author is not known (the board failed when asked) is nobody's: it must not show in an agent's list, which a NULL treated as a match would do.
func TestSavedUnknownAuthorIsNotAMatchWhenTheBoardFails(t *testing.T) {
	g := newSavedRig(t)
	code, out := g.do("POST", "/api/saved", savedAsOperator, map[string]any{"kind": "board", "ref": "busy-team", "key": "31", "title": "busy-team", "snippet": "no author yet"})
	require.Equal(t, http.StatusOK, code, out)
	g.h.SetBoardAuthor(func(_ context.Context, _ string, _ int64) (string, bool, error) {
		return "", false, fmt.Errorf("board busy")
	})
	_, out = g.do("GET", "/api/saved?agent=sess-qa&team=busy-team&author="+url.QueryEscape("QA Engineer"), savedAsOperator, nil)
	assert.Empty(t, out["items"], "the fill failed: the bookmark is in no agent's list")
	assert.EqualValues(t, 0, out["open"])
	_, out = g.do("GET", "/api/saved?team=busy-team", savedAsOperator, nil)
	assert.Len(t, out["items"], 1, "it is still the team's")
}

// The fill looks up at most maxSavedAuthorFill board messages per request; the rest wait for the next ask.
func TestSavedAuthorFillIsCappedPerRequest(t *testing.T) {
	g := newSavedRig(t)
	for i := 0; i < maxSavedAuthorFill+5; i++ {
		code, out := g.do("POST", "/api/saved", savedAsOperator, map[string]any{"kind": "board", "ref": "big-team", "key": strconv.Itoa(1000 + i), "title": "big-team", "snippet": "old"})
		require.Equal(t, http.StatusOK, code, out)
	}
	asked := 0
	g.h.SetBoardAuthor(func(_ context.Context, _ string, _ int64) (string, bool, error) {
		asked++
		return "QA Engineer", true, nil
	})
	q := "/api/saved?agent=sess-qa&team=big-team&author=" + url.QueryEscape("QA Engineer")
	_, out := g.do("GET", q, savedAsOperator, nil)
	assert.Equal(t, maxSavedAuthorFill, asked, "one request asks at most the cap")
	assert.Len(t, out["items"], maxSavedAuthorFill)
	_, out = g.do("GET", q, savedAsOperator, nil)
	assert.Equal(t, maxSavedAuthorFill+5, asked, "the next ask does the rest, each message once")
	assert.Len(t, out["items"], maxSavedAuthorFill+5)
}
