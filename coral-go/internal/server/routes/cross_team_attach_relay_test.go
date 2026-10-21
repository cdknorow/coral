package routes

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/board/boardtest"
	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/linktest"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/pkg/tunnel"
)

// C2 (#4403): coral-board send --address ... --attach, the send side across Corals.

// sentFile is one file frame the fake hub received.
type sentFile struct {
	Meta map[string]any
	Data []byte
	N    int64
}

// hubWire is a fake Coral Hub: it records, in order, every file and every message it is sent.
type hubWire struct {
	mu     sync.Mutex
	events []string // "file:<digest>" and "message"
	files  []sentFile
	msgs   int
	// fileAnswer decides the answer to the i-th file (0-based); default accepts.
	fileAnswer func(i int, f sentFile) (tunnel.RelayResponse, error)
}

func (w *hubWire) artifact(_ context.Context, n int64, body io.Reader) (tunnel.RelayResponse, error) {
	raw, _ := io.ReadAll(body)
	var f sentFile
	f.N = n
	if len(raw) >= 4 {
		ml := int(binary.BigEndian.Uint32(raw[:4]))
		if 4+ml <= len(raw) {
			_ = json.Unmarshal(raw[4:4+ml], &f.Meta)
			f.Data = raw[4+ml:]
		}
	}
	w.mu.Lock()
	i := len(w.files)
	w.files = append(w.files, f)
	w.events = append(w.events, "file:"+asString(f.Meta["digest"]))
	w.mu.Unlock()
	if w.fileAnswer != nil {
		return w.fileAnswer(i, f)
	}
	return tunnel.RelayResponse{Status: 200, Body: []byte(`{"ok":true}`)}, nil
}

func asString(v any) string { s, _ := v.(string); return s }

func (w *hubWire) relay(_ context.Context, path string, _ []byte) (tunnel.RelayResponse, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if path != tunnel.RelayMessagePath {
		return tunnel.RelayResponse{Status: 200, Body: []byte(`{"ok":true,"targets":[]}`)}, nil
	}
	w.msgs++
	w.events = append(w.events, "message")
	return tunnel.RelayResponse{Status: 200, Body: []byte(`{"ok":true,"target_message_id":"77"}`)}, nil
}

func (w *hubWire) snapshot() ([]string, []sentFile, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.events...), append([]sentFile(nil), w.files...), w.msgs
}

type relayAttachEnv struct {
	*attachEnv
	wire *hubWire
}

func newRelayAttachEnv(t *testing.T, artifactsOn bool) *relayAttachEnv {
	t.Helper()
	bs, err := boardtest.Open(t.TempDir() + "/board.db")
	require.NoError(t, err)
	t.Cleanup(func() { bs.Close() })
	e := &relayAttachEnv{attachEnv: &attachEnv{t: t, h: NewBoardHandler(bs), coralDir: t.TempDir()}, wire: &hubWire{}}
	e.h.SetTaskArtifactWriter(e.coralDir, nil)
	e.h.SetRelay(e.wire.relay)
	e.h.SetRelayArtifact(e.wire.artifact)
	clock := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex
	e.h.crossTeam.now = func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); clock = clock.Add(time.Hour); return clock }
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if sid := r.Header.Get("X-Test-Session"); sid != "" {
				r = r.WithContext(context.WithValue(r.Context(), verifiedSessionKey{}, &store.SessionTokenOwner{SessionID: sid, SessionName: "claude-" + sid}))
				r = withActor(r, Actor{Kind: ActorToken, Name: r.Header.Get("X-Test-Name"), Session: "claude-" + sid})
			}
			if r.Header.Get("X-Test-Operator") != "" {
				r = withActor(r, Actor{Kind: ActorOperator, Name: "Operator"})
			}
			next.ServeHTTP(w, r)
		})
	})
	r.Post("/api/board/{project}/relay-messages", e.h.PostRelayMessage)
	r.Get("/api/board/{project}/artifacts", e.h.ListTeamArtifacts)
	srv := httptest.NewServer(r)
	t.Cleanup(func() { srv.Close(); e.h.WaitBackground() })
	e.url = srv.URL
	ctx := context.Background()
	for _, s := range []struct{ board, id, role, sid string }{
		{"alpha", "Orchestrator", "Orchestrator", alphaOrchSID},
		{"beta", "Orchestrator", "Orchestrator", betaOrchSID},
		{"beta", "QA", "QA Engineer", betaQASID},
	} {
		_, err := bs.Subscribe(ctx, s.board, s.id, s.role, "claude-"+s.sid, nil, "mentions")
		require.NoError(t, err)
	}
	for _, b := range []string{"alpha", "beta"} {
		_, err := bs.UpdateBoardSettings(ctx, b, board.BoardSettingsUpdate{CrossTeamMessaging: ptrBool(true), CrossTeamArtifacts: ptrBool(artifactsOn)})
		require.NoError(t, err)
	}
	return e
}

// relay posts a send to homelab/qa from alpha's Orchestrator (session sid), with an optional message id.
func (e *relayAttachEnv) relay(sid, messageID string, attach ...string) (int, map[string]any, http.Header) {
	e.t.Helper()
	return e.relayText(sid, messageID, "here you go", attach...)
}

// relayText is relay with the message's own text.
func (e *relayAttachEnv) relayText(sid, messageID, content string, attach ...string) (int, map[string]any, http.Header) {
	e.t.Helper()
	sender, sessionName := "Orchestrator", "claude-"+sid
	if sid == "operator" {
		sender, sessionName = "Operator", ""
	}
	body := map[string]any{"subscriber_id": sender, "session_name": sessionName, "content": content, "to_address": "homelab/qa", "attach": attach}
	if messageID != "" {
		body["message_id"] = messageID
	}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", e.url+"/api/board/alpha/relay-messages", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if sid == "operator" {
		req.Header.Set("X-Test-Operator", "1")
	} else {
		req.Header.Set("X-Test-Session", sid)
		req.Header.Set("X-Test-Name", "Orchestrator")
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(e.t, err)
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out, resp.Header
}

func (e *relayAttachEnv) sharedRows() []map[string]any {
	e.t.Helper()
	req := httptest.NewRequest("GET", "/api/board/alpha/artifacts", nil)
	rec := httptest.NewRecorder()
	// The list route is on the env's own router: ask it over HTTP.
	resp, err := http.Get(e.url + "/api/board/alpha/artifacts")
	require.NoError(e.t, err)
	defer resp.Body.Close()
	_ = req
	_ = rec
	var out struct {
		Artifacts []map[string]any `json:"artifacts"`
	}
	require.NoError(e.t, json.NewDecoder(resp.Body).Decode(&out))
	var shared []map[string]any
	for _, a := range out.Artifacts {
		if a["source"] == "shared" {
			shared = append(shared, a)
		}
	}
	return shared
}

// nothingSent asserts that no file and no message went to Coral Hub, and nothing is recorded as shared.
func (e *relayAttachEnv) nothingSent(msg string) {
	e.t.Helper()
	events, _, _ := e.wire.snapshot()
	assert.Empty(e.t, events, msg+": nothing reached Coral Hub")
	assert.Empty(e.t, e.sharedRows(), msg+": no shared row")
	assert.Empty(e.t, e.h.crossTeamRecords("shared", "alpha"), msg)
}

// The files go first, in order, then the message; each frame is the design's: its metadata, then exactly
// the file's bytes under its stored id; the shared rows are written after, once.
func TestRelayAttachSendsFilesInOrderThenTheMessage(t *testing.T) {
	e := newRelayAttachEnv(t, true)
	a := e.upload(alphaOrchSID, "# alpha report\nnumbers\n")
	b := e.upload(alphaOrchSID, "# second file\nmore\n")
	code, out, _ := e.relay(alphaOrchSID, "msg-1", uri(a), uri(b))
	require.Equal(t, http.StatusOK, code, out)
	events, files, msgs := e.wire.snapshot()
	assert.Equal(t, []string{"file:sha256:" + a, "file:sha256:" + b, "message"}, events, "the files, in order, then the message")
	assert.Equal(t, 1, msgs)
	require.Len(t, files, 2)
	for i, id := range []string{a, b} {
		f := files[i]
		assert.Equal(t, "msg-1", f.Meta["message_id"])
		assert.Equal(t, "homelab/qa", f.Meta["to_address"])
		assert.Equal(t, "alpha", f.Meta["from_board"])
		assert.Equal(t, "sha256:"+id, f.Meta["digest"])
		assert.EqualValues(t, len(f.Data), f.Meta["size"])
		assert.Equal(t, "text/markdown", f.Meta["media_type"])
		want, err := os.ReadFile(filepath.Join(e.coralDir, "artifacts", "objects", id))
		require.NoError(t, err)
		assert.Equal(t, want, f.Data, "exactly the stored bytes")
		assert.EqualValues(t, 4+jsonLen(f.Meta)+len(f.Data), f.N, "the declared length is the whole frame")
	}
	assert.Equal(t, []any{uri(a), uri(b)}, out["attached"])
	assert.Contains(t, out["mirror"].(map[string]any)["content"], uri(a), "the links ride in the text")
	shared := e.sharedRows()
	assert.Len(t, shared, 2)
}

func jsonLen(m map[string]any) int {
	b, _ := json.Marshal(m)
	return len(b)
}

// A send with the sending board's switch off is refused locally, before any byte goes.
func TestRelayAttachSwitchOffIsRefusedLocally(t *testing.T) {
	e := newRelayAttachEnv(t, false)
	a := e.upload(alphaOrchSID, "# alpha report\n")
	code, out, _ := e.relay(alphaOrchSID, "", uri(a))
	assert.Equal(t, http.StatusForbidden, code)
	assert.Equal(t, "artifacts_off", out["error"])
	e.nothingSent("switch off")
	// A plain message with no attachment still goes (the switch is about files).
	code, out, _ = e.relay(alphaOrchSID, "")
	assert.Equal(t, http.StatusOK, code, out)
}

// Over a limit is refused before any byte goes: five files, one file over 8 MiB, a set over 16 MiB.
func TestRelayAttachOverLimitSetsAreRefusedBeforeAnyByte(t *testing.T) {
	e := newRelayAttachEnv(t, true)
	var ids []string
	for i := 0; i < 5; i++ {
		ids = append(ids, e.upload(alphaOrchSID, strings.Repeat("a", 10)+string(rune('A'+i))))
	}
	code, out, _ := e.relay(alphaOrchSID, "", uri(ids[0]), uri(ids[1]), uri(ids[2]), uri(ids[3]), uri(ids[4]))
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Equal(t, "attach_too_many", out["code"])
	e.nothingSent("five files")

	big := e.upload(alphaOrchSID, strings.Repeat("b", maxCrossTeamAttachBytes+1))
	code, out, _ = e.relay(alphaOrchSID, "", uri(big))
	assert.Equal(t, http.StatusRequestEntityTooLarge, code)
	assert.Equal(t, "attach_too_large", out["code"])
	e.nothingSent("one file over 8 MiB")

	six := func(c string) string { return e.upload(alphaOrchSID, strings.Repeat(c, 6<<20)) }
	code, out, _ = e.relay(alphaOrchSID, "", uri(six("c")), uri(six("d")), uri(six("e")))
	assert.Equal(t, http.StatusRequestEntityTooLarge, code)
	assert.Equal(t, "attach_total_too_large", out["code"])
	e.nothingSent("18 MiB in all")
}

// An early 429 from Coral Hub reaches the caller with its Retry-After and which file it was; the message is
// not sent and nothing is recorded.
func TestRelayAttachEarlyHubRefusalReachesTheCaller(t *testing.T) {
	e := newRelayAttachEnv(t, true)
	a := e.upload(alphaOrchSID, "# one\n")
	b := e.upload(alphaOrchSID, "# two\n")
	e.wire.fileAnswer = func(int, sentFile) (tunnel.RelayResponse, error) {
		return tunnel.RelayResponse{Status: 429, Body: []byte(`{"ok":false,"error":"artifact_quota","limit":"hourly","message":"free text from the hub"}`), RetryAfter: 7}, nil
	}
	code, out, hdr := e.relay(alphaOrchSID, "msg-9", uri(a), uri(b))
	assert.Equal(t, http.StatusTooManyRequests, code)
	assert.Equal(t, "artifact_quota", out["error"])
	assert.Equal(t, "7", hdr.Get("Retry-After"))
	msg, _ := out["message"].(string)
	assert.Contains(t, msg, "attachment 1 of 2")
	assert.Contains(t, msg, "limit: hourly")
	assert.Contains(t, msg, "a retry resends the files the hub already has")
	assert.Contains(t, msg, "--message-id msg-9")
	assert.NotContains(t, msg, "free text from the hub", "never the hub's own words")
	events, _, msgs := e.wire.snapshot()
	assert.Equal(t, []string{"file:sha256:" + a}, events, "stopped at the first file")
	assert.Zero(t, msgs)
	assert.Empty(t, e.sharedRows())
}

// A failing second file sends no message and records nothing; the retry with the same message id sends it.
func TestRelayAttachFailingSecondFileSendsNoMessage(t *testing.T) {
	e := newRelayAttachEnv(t, true)
	a := e.upload(alphaOrchSID, "# one\n")
	b := e.upload(alphaOrchSID, "# two\n")
	fail := true
	e.wire.fileAnswer = func(i int, _ sentFile) (tunnel.RelayResponse, error) {
		if i == 1 && fail {
			return tunnel.RelayResponse{Status: 403, Body: []byte(`{"ok":false,"error":"artifacts_off"}`)}, nil
		}
		return tunnel.RelayResponse{Status: 200, Body: []byte(`{"ok":true}`)}, nil
	}
	code, out, _ := e.relay(alphaOrchSID, "msg-2", uri(a), uri(b))
	assert.Equal(t, http.StatusForbidden, code)
	assert.Equal(t, "artifacts_off", out["error"])
	assert.Contains(t, out["message"], "attachment 2 of 2")
	_, files, msgs := e.wire.snapshot()
	assert.Len(t, files, 2)
	assert.Zero(t, msgs, "the message is not sent")
	assert.Empty(t, e.sharedRows())
	assert.Equal(t, 0, len(e.h.crossTeamRecords("shared", "alpha")))

	fail = false
	code, out, _ = e.relay(alphaOrchSID, "msg-2", uri(a), uri(b))
	require.Equal(t, http.StatusOK, code, out)
	_, files, msgs = e.wire.snapshot()
	assert.Len(t, files, 4)
	assert.Equal(t, 1, msgs)
	for _, f := range files {
		assert.Equal(t, "msg-2", f.Meta["message_id"], "the retry is idempotent by the same id")
	}
	assert.Len(t, e.sharedRows(), 2)
}

// The hub's other answers become fixed words: 404 and 413 and anything else, never the hub's free text.
func TestRelayAttachHubAnswersMapToClearCodes(t *testing.T) {
	for _, c := range []struct {
		status int
		body   string
		code   string
		in     string
	}{
		{404, `{"ok":false,"error":"not_found"}`, "not_found", "no such address"},
		{413, `{"ok":false,"error":"artifact_too_large"}`, "artifact_too_large", "over the limit"},
		{502, `{"ok":false,"error":"target_unreachable"}`, "target_unreachable", "refused the file"},
		{500, `not json at all`, "hub_error", "refused the file"},
		{200, `{"ok":false}`, "hub_error", "refused the file"},
	} {
		e := newRelayAttachEnv(t, true)
		a := e.upload(alphaOrchSID, "# one\n")
		e.wire.fileAnswer = func(int, sentFile) (tunnel.RelayResponse, error) {
			return tunnel.RelayResponse{Status: c.status, Body: []byte(c.body)}, nil
		}
		code, out, _ := e.relay(alphaOrchSID, "", uri(a))
		assert.GreaterOrEqual(t, code, 400, c.body)
		assert.Equal(t, c.code, out["error"], c.body)
		assert.Contains(t, out["message"], c.in, c.body)
		e.nothingSentExceptFiles(1)
	}
}

func (e *relayAttachEnv) nothingSentExceptFiles(n int) {
	e.t.Helper()
	_, files, msgs := e.wire.snapshot()
	assert.Len(e.t, files, n)
	assert.Zero(e.t, msgs, "no message")
	assert.Empty(e.t, e.sharedRows())
}

// Transport errors: not linked and files not relayed refund the send's rate token and say so; a dead
// connection says to retry with the message id.
func TestRelayAttachTransportErrors(t *testing.T) {
	for _, c := range []struct {
		err    error
		status int
		code   string
	}{
		{cloud.ErrNotLinked, 503, "not_linked"},
		{tunnel.ErrRelayArtifactUnsupported, 501, "relay_unsupported"},
		{tunnel.ErrRelayArtifactClosed, 502, "hub_unreachable"},
		{errors.New("boom"), 502, "hub_unreachable"},
	} {
		e := newRelayAttachEnv(t, true)
		a := e.upload(alphaOrchSID, "# one\n")
		e.h.SetRelayArtifact(func(_ context.Context, _ int64, body io.Reader) (tunnel.RelayResponse, error) {
			_, _ = io.Copy(io.Discard, body)
			return tunnel.RelayResponse{}, c.err
		})
		code, out, _ := e.relay(alphaOrchSID, "m1", uri(a))
		assert.Equal(t, c.status, code, c.code)
		assert.Equal(t, c.code, out["error"])
		_, _, msgs := e.wire.snapshot()
		assert.Zero(t, msgs)
		assert.Empty(t, e.sharedRows())
	}
	// No file relay at all (not linked): 503 before anything is read.
	e := newRelayAttachEnv(t, true)
	e.h.SetRelayArtifact(nil)
	a := e.upload(alphaOrchSID, "# one\n")
	code, out, _ := e.relay(alphaOrchSID, "", uri(a))
	assert.Equal(t, 503, code)
	assert.Equal(t, "not_linked", out["error"])
	e.nothingSent("no relay")
}

// A file swapped (same size, other bytes) or removed after the check fails the send with nothing sent
// and nothing recorded.
func TestRelayAttachSwappedFileAbortsWithNothingSent(t *testing.T) {
	// A tamper runs inside the handler, on the server's goroutine: it must never call t.Skip, require or
	// FailNow there (that ended the server goroutine, the hook stayed set for every later test, and the
	// package run aborted with "test executed panic(nil) or runtime.Goexit" on Windows). It records its
	// error, checked on the test's own goroutine.
	var tamperErr error
	try := func(err error) {
		if err != nil && tamperErr == nil {
			tamperErr = err
		}
	}
	cases := map[string]func(path string){
		"rewritten in place": func(path string) {
			data, _ := os.ReadFile(path)
			for i := range data {
				data[i] ^= 0x01
			}
			try(os.WriteFile(path, data, 0o600))
		},
		"replaced by a link": func(path string) {
			try(os.Remove(path))
			try(os.WriteFile(path+".other", []byte("# one\n"), 0o600))
			try(os.Symlink(path+".other", path))
		},
		"deleted": func(path string) { try(os.Remove(path)) },
	}
	for name, tamper := range cases {
		t.Run(name, func(t *testing.T) {
			if name == "replaced by a link" {
				// Here, on the test's goroutine, not in the tamper: skips where links can't be made, fails on CI.
				linktest.RequireSymlinks(t)
			}
			tamperErr = nil
			e := newRelayAttachEnv(t, true)
			a := e.upload(alphaOrchSID, "# one\n")
			attachBeforeLoad = tamper
			t.Cleanup(func() { attachBeforeLoad = nil }) // whatever ends the test, no later test runs this hook
			code, out, _ := e.relay(alphaOrchSID, "", uri(a))
			attachBeforeLoad = nil
			require.NoError(t, tamperErr, "the tamper itself")
			assert.Equal(t, http.StatusConflict, code)
			assert.Equal(t, "attach_changed", out["code"])
			e.nothingSent(name)
		})
	}
}

// A text file with a secret in it is refused; binary types are not scanned; a clean text file goes.
func TestRelayAttachSecretScan(t *testing.T) {
	token := "ghp_" + "1a2B3c4D5e6F7g8H9i0J1k2L3m4N5o6P7q8R"
	e := newRelayAttachEnv(t, true)
	dirty := e.upload(alphaOrchSID, "# notes\ndeploy with token="+token+"\n")
	code, out, _ := e.relay(alphaOrchSID, "", uri(dirty))
	assert.Equal(t, http.StatusUnprocessableEntity, code, out)
	assert.Equal(t, "attach_secret", out["code"])
	e.nothingSent("a secret in a text file")

	// The same bytes in a binary file (a NUL in it) are not scanned: the file type says what is looked at.
	binary := e.upload(alphaOrchSID, "\x00\x01 binary "+token+"\n")
	code, out, _ = e.relay(alphaOrchSID, "", uri(binary))
	assert.Equal(t, http.StatusOK, code, out)
	_, files, _ := e.wire.snapshot()
	require.Len(t, files, 1)
}

// An id another team uploaded answers like one that is not stored, and nothing is sent.
func TestRelayAttachOtherTeamsUploadIsNotYours(t *testing.T) {
	e := newRelayAttachEnv(t, true)
	theirs := e.upload(betaOrchSID, "# beta's\n")
	code, out, _ := e.relay(alphaOrchSID, "", uri(theirs))
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Equal(t, "not_yours", out["code"])
	missing := strings.Repeat("0", 64)
	code2, out2, _ := e.relay(alphaOrchSID, "", uri(missing))
	assert.Equal(t, code, code2)
	assert.Equal(t, out["code"], out2["code"])
	e.nothingSent("another team's upload")
}

// C1's team scope applies across Corals too: an upload by a teammate of the sending board (another session
// of its team) is attachable by the Orchestrator, another board's still is not (above).
func TestRelayAttachTeammatesUploadIsAttachable(t *testing.T) {
	e := newRelayAttachEnv(t, true)
	_, err := e.h.bs.Subscribe(context.Background(), "alpha", "Dev", "Backend Dev", "claude-"+alphaDevSID, nil, "mentions")
	require.NoError(t, err)
	mine := e.upload(alphaDevSID, "# the dev's notes\n")
	code, out, _ := e.relay(alphaOrchSID, "", uri(mine))
	require.Equal(t, http.StatusOK, code, out)
	_, files, msgs := e.wire.snapshot()
	assert.Len(t, files, 1)
	assert.Equal(t, 1, msgs)
}

// The frame's name and media type are what the hub accepts, whatever the sidecar held.
func TestRelayArtifactFrameFields(t *testing.T) {
	a := attachedArtifact{ID: strings.Repeat("a", 64), Name: "re\u202Eport\x07 " + strings.Repeat("\u00e9", 300), MediaType: "text/plain; charset=utf-8"}
	frame, err := relayArtifactFrame("m1", "homelab/qa", "alpha", "", a, []byte("data"))
	require.NoError(t, err)
	ml := int(binary.BigEndian.Uint32(frame[:4]))
	m, err := parseReceivedMetaForTest(frame[4 : 4+ml])
	require.NoError(t, err, "the receiver's own parser accepts what the sender builds")
	assert.Equal(t, "text/plain", m["media_type"])
	assert.LessOrEqual(t, len(asString(m["name"])), 200)
	assert.NotContains(t, asString(m["name"]), "\u202E")
	assert.Equal(t, "data", string(frame[4+ml:]))

	a.Name, a.MediaType = "\x00\x01", "bad type"
	frame, err = relayArtifactFrame("m1", "homelab/qa", "alpha", "", a, []byte("data"))
	require.NoError(t, err)
	ml = int(binary.BigEndian.Uint32(frame[:4]))
	m, err = parseReceivedMetaForTest(frame[4 : 4+ml])
	require.NoError(t, err)
	assert.Equal(t, "artifact", m["name"])
	assert.Equal(t, "application/octet-stream", m["media_type"])
}

func parseReceivedMetaForTest(raw []byte) (map[string]any, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	for _, k := range []string{"message_id", "to_address", "from_board", "digest", "size", "name", "media_type"} {
		if _, ok := m[k]; !ok {
			return nil, errors.New("missing " + k)
		}
	}
	if len(m) != 7 {
		return nil, errors.New("extra member")
	}
	return m, nil
}

// A send that never reached Coral Hub (files not relayed) gives its rate token back: more refusals than the
// burst still answer 501, not 429.
func TestRelayAttachUnsupportedRefundsTheRateToken(t *testing.T) {
	e := newRelayAttachEnv(t, true)
	fixed := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	e.h.crossTeam.now = func() time.Time { return fixed }
	a := e.upload(alphaOrchSID, "# one\n")
	e.h.SetRelayArtifact(func(_ context.Context, _ int64, body io.Reader) (tunnel.RelayResponse, error) {
		_, _ = io.Copy(io.Discard, body)
		return tunnel.RelayResponse{}, tunnel.ErrRelayArtifactUnsupported
	})
	for i := 0; i < crossTeamBurst+2; i++ {
		code, out, _ := e.relay(alphaOrchSID, "", uri(a))
		require.Equal(t, http.StatusNotImplemented, code, "send %d: %v", i, out)
	}
}

// The name is checked again after the read: the path swapped for another file with the very same bytes (a new
// inode, so the digest alone would pass) is attach_changed, with nothing sent or recorded.
func TestRelayAttachSameBytesInAnotherFileAbortsWithNothingSent(t *testing.T) {
	e := newRelayAttachEnv(t, true)
	a := e.upload(alphaOrchSID, "# one\n")
	// Runs in the handler: errors are recorded, never require'd there (see the swapped-file test).
	var hookErr error
	attachAfterRead = func(path string) {
		data, err := os.ReadFile(path)
		if err == nil {
			err = os.Remove(path)
		}
		if err == nil {
			err = os.WriteFile(path, data, 0o600)
		}
		hookErr = err
	}
	t.Cleanup(func() { attachAfterRead = nil })
	code, out, _ := e.relay(alphaOrchSID, "", uri(a))
	attachAfterRead = nil
	require.NoError(t, hookErr, "the hook itself")
	assert.Equal(t, http.StatusConflict, code, out)
	assert.Equal(t, "attach_changed", out["code"])
	e.nothingSent("same bytes, another file")
}

// The sending board's switch is read again before each file: turned off while the first is on its way, the
// second is not sent (it is already in memory), and neither is the message (#4440).
func TestRelayAttachSwitchTurnedOffBetweenFiles(t *testing.T) {
	e := newRelayAttachEnv(t, true)
	a := e.upload(alphaOrchSID, "# one\n")
	b := e.upload(alphaOrchSID, "# two\n")
	e.wire.fileAnswer = func(i int, _ sentFile) (tunnel.RelayResponse, error) {
		if i == 0 {
			_, err := e.h.bs.UpdateBoardSettings(context.Background(), "alpha", board.BoardSettingsUpdate{CrossTeamArtifacts: ptrBool(false)})
			require.NoError(t, err)
		}
		return tunnel.RelayResponse{Status: 200, Body: []byte(`{"ok":true}`)}, nil
	}
	code, out, _ := e.relay(alphaOrchSID, "msg-9", uri(a), uri(b))
	assert.Equal(t, http.StatusForbidden, code)
	assert.Equal(t, "artifacts_off", out["error"])
	assert.Contains(t, out["message"], "attachment 2 of 2")
	_, files, msgs := e.wire.snapshot()
	assert.Len(t, files, 1, "the second file is not sent")
	assert.Zero(t, msgs, "nor the message")
	assert.Empty(t, e.sharedRows())
}

// The text of a message to another Coral is scanned like an attachment, with the same views: a token is
// refused plain, in base64 and split, with a fixed code that names nothing from the text; nothing reaches
// Coral Hub and no rate token is spent (#4449).
func TestRelayMessageTextWithASecretIsRefused(t *testing.T) {
	token := "ghp_" + "1a2B3c4D5e6F7g8H9i0J1k2L3m4N5o6P7q8R"
	e := newRelayAttachEnv(t, true)
	for name, text := range map[string]string{
		"plain":     "deploy with token=" + token,
		"base64":    "here: " + base64.StdEncoding.EncodeToString([]byte("token="+token)),
		"split":     token[:10] + "\n" + token[10:],
		"reversed":  string(reverseBytes([]byte(token))),
		"in a list": "- one\n- two\n- " + token + "\n",
	} {
		code, out, _ := e.relayText(alphaOrchSID, "", text)
		assert.Equal(t, http.StatusUnprocessableEntity, code, name)
		assert.Equal(t, "message_secret", out["error"], name)
		msg, _ := out["message"].(string)
		assert.NotContains(t, msg, token[:8], name+": the answer names no matched text")
		e.nothingSent(name)
	}

	// Ordinary prose with paths, URLs and ids passes and is sent.
	prose := "The log is at /Users/someone/Software/coral/coral-go/internal/server/routes/file_x.go and the run is https://example.com/api/board/some-long-project-name/artifacts/42?x=1. The password: is in the vault, not here."
	code, out, _ := e.relayText(alphaOrchSID, "", prose)
	require.Equal(t, http.StatusOK, code, out)
	_, _, msgs := e.wire.snapshot()
	assert.Equal(t, 1, msgs)
}

// A budget that runs out on the text fails closed with its own code.
func TestRelayMessageTextOverTheScanBudgetIsRefused(t *testing.T) {
	e := newRelayAttachEnv(t, true)
	var b strings.Builder
	for i := 0; i < scanMaxRuns+8; i++ {
		fmt.Fprintln(&b, "note", base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("plain note number %06d", i))))
	}
	code, out, _ := e.relayText(alphaOrchSID, "", b.String())
	assert.Equal(t, http.StatusUnprocessableEntity, code)
	assert.Equal(t, "message_unchecked", out["error"])
	e.nothingSent("over budget")
}
