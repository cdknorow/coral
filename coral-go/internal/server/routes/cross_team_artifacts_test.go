package routes

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/board/boardtest"
	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/pkg/tunnel"
)

// The receive side of cross-team artifacts (C3): a file another Coral sends through Coral Hub.

type artifactEnv struct {
	t   *testing.T
	h   *BoardHandler
	r   chi.Router
	dir string
	now time.Time
}

func newArtifactEnv(t *testing.T) *artifactEnv {
	t.Helper()
	bs, err := boardtest.Open(t.TempDir() + "/board.db")
	require.NoError(t, err)
	t.Cleanup(func() { bs.Close() })
	e := &artifactEnv{t: t, h: NewBoardHandler(bs), dir: t.TempDir(), now: time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)}
	e.h.coralDir = e.dir
	// A received-artifact store runs in a goBG goroutine that writes under coralDir (e.dir). Drain those before the
	// test ends, or a still-running write races t.TempDir's RemoveAll ("directory not empty", #4601 flake). Registered
	// after the t.TempDir calls above, so it runs before their cleanup (LIFO) and before the store closes.
	t.Cleanup(func() { e.h.WaitBackground() })
	e.h.received.now = func() time.Time { return e.now }
	e.r = chi.NewRouter()
	e.r.Get("/api/board/{project}/settings", e.h.GetBoardSettings)
	e.r.Put("/api/board/{project}/settings", e.h.UpdateBoardSettings)
	e.r.Post("/api/board/{project}/external-artifacts", e.h.PostExternalArtifact)
	e.r.Post("/api/board/{project}/external-messages", e.h.PostExternalMessage)
	e.r.Get("/api/board/{project}/artifacts", e.h.ListTeamArtifacts)
	_, err = bs.Subscribe(t.Context(), "beta", "Orchestrator", "Orchestrator", "tmux-beta", nil, "mentions")
	require.NoError(t, err)
	return e
}

func (e *artifactEnv) put(path string, body any, via bool) (int, map[string]any) {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("PUT", path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if via {
		req.Header.Set(tunnel.ViaHeader, cloud.ViaSecret())
	}
	rec := httptest.NewRecorder()
	e.r.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// open turns every switch on: messaging, the source, and artifacts.
func (e *artifactEnv) open() {
	e.t.Helper()
	code, out := e.put("/api/board/beta/settings", map[string]any{"subscriber_id": "Operator", "cross_team_messaging": true, "cross_team_servers": []string{relaySrc}, "cross_team_artifacts": true}, false)
	require.Equal(e.t, http.StatusOK, code, out)
}

func sha256Of(data []byte) string {
	s := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(s[:])
}

func meta(data []byte, name string) map[string]any {
	return map[string]any{"message_id": "m1", "to_address": "srv/beta", "from_board": "alpha", "digest": sha256Of(data), "size": len(data), "name": name, "media_type": "text/plain"}
}

// frame is the wire body: a 4-byte length, the metadata, then the bytes.
func frame(m map[string]any, data []byte) []byte {
	raw, _ := json.Marshal(m)
	return rawFrame(raw, data)
}

func rawFrame(raw, data []byte) []byte {
	out := binary.BigEndian.AppendUint32(nil, uint32(len(raw)))
	return append(append(out, raw...), data...)
}

// send posts body to the receive route as the connector would: via is "auth", "forged" or "".
func (e *artifactEnv) send(via, src, hubUser string, body []byte) (int, map[string]any) {
	e.t.Helper()
	req := httptest.NewRequest("POST", "/api/board/beta/external-artifacts", bytes.NewReader(body))
	if src != "" {
		req.Header.Set(tunnel.HubServerHeader, src)
	}
	switch via {
	case "auth":
		req.Header.Set(tunnel.ViaHeader, cloud.ViaSecret())
	case "forged":
		req.Header.Set(tunnel.ViaHeader, "1")
	}
	if hubUser != "" {
		req.Header.Set(tunnel.HubUserHeader, hubUser)
	}
	rec := httptest.NewRecorder()
	e.r.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// deliver posts the relayed message relayID from src (the message the files named it come with).
func (e *artifactEnv) deliver(src, relayID string) (int, map[string]any) {
	e.t.Helper()
	b, _ := json.Marshal(map[string]any{"from_board": "alpha", "subscriber_id": "Orchestrator", "content": "here are the files",
		"relay": map[string]any{"message_id": relayID, "hops": 0}})
	req := httptest.NewRequest("POST", "/api/board/beta/external-messages", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tunnel.HubServerHeader, src)
	req.Header.Set(tunnel.ViaHeader, cloud.ViaSecret())
	rec := httptest.NewRecorder()
	e.r.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func (e *artifactEnv) ok(data []byte, name string) (int, map[string]any) {
	return e.send("auth", relaySrc, "", frame(meta(data, name), data))
}

// stored lists what is in the shared object store (objects and sidecars, not temp files).
func (e *artifactEnv) stored() []string {
	entries, _ := os.ReadDir(filepath.Join(e.dir, "artifacts", "objects"))
	var names []string
	for _, en := range entries {
		names = append(names, en.Name())
	}
	return names
}

func (e *artifactEnv) listed() []map[string]any {
	e.t.Helper()
	req := httptest.NewRequest("GET", "/api/board/beta/artifacts", nil)
	rec := httptest.NewRecorder()
	e.r.ServeHTTP(rec, req)
	require.Equal(e.t, http.StatusOK, rec.Code)
	var out struct {
		Artifacts []map[string]any `json:"artifacts"`
	}
	require.NoError(e.t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out.Artifacts
}

// Only a request this process's connector stamped is a relay; a local caller, a forged stamp, no source and a
// Coral Hub user are all refused and nothing is stored.
func TestReceivedArtifactNeedsAnAuthenticTunnel(t *testing.T) {
	e := newArtifactEnv(t)
	e.open()
	data := []byte("notes\n")
	body := frame(meta(data, "n.txt"), data)
	for _, c := range []struct {
		via, src, user, code string
		status               int
	}{
		{"", relaySrc, "", "hub_server_untrusted", 403},
		{"forged", relaySrc, "", "hub_server_untrusted", 403},
		{"auth", "", "", "hub_server_untrusted", 403},
		{"auth", relaySrc, "hub-user-1234abcd", "ambiguous_origin", 400},
	} {
		status, out := e.send(c.via, c.src, c.user, body)
		assert.Equal(t, c.status, status, "%+v", c)
		assert.Equal(t, c.code, out["error"], "%+v", c)
	}
	assert.Empty(t, e.stored())
}

// Every switch that is off gives the same answer, so the sender cannot tell which; with all on the file arrives.
func TestReceivedArtifactSwitchesAnswerUniformly(t *testing.T) {
	data := []byte("notes\n")
	body := frame(meta(data, "n.txt"), data)
	var answers []string
	for name, set := range map[string]map[string]any{
		"everything off":     {"cross_team_messaging": false, "cross_team_artifacts": false},
		"messaging off":      {"cross_team_messaging": false, "cross_team_servers": []string{relaySrc}, "cross_team_artifacts": true},
		"source not allowed": {"cross_team_messaging": true, "cross_team_artifacts": true},
		"artifacts off":      {"cross_team_messaging": true, "cross_team_servers": []string{relaySrc}, "cross_team_artifacts": false},
		"defaults, no list":  {}, // the switches default to on (#5916), and with no list the old door admits nothing
	} {
		e := newArtifactEnv(t)
		set["subscriber_id"] = "Operator"
		code, out := e.put("/api/board/beta/settings", set, false)
		require.Equal(t, http.StatusOK, code, out)
		status, out := e.send("auth", relaySrc, "", body)
		assert.Equal(t, 403, status, name)
		assert.Equal(t, "artifacts_off", out["error"], name)
		b, _ := json.Marshal(out)
		answers = append(answers, string(b))
		assert.Empty(t, e.stored(), name)
	}
	for _, a := range answers {
		assert.Equal(t, answers[0], a, "one uniform answer")
	}
	e := newArtifactEnv(t)
	e.open()
	status, out := e.ok(data, "n.txt")
	assert.Equal(t, 200, status, out)
}

func TestReceivedArtifactUnknownBoardIsNotFound(t *testing.T) {
	e := newArtifactEnv(t)
	req := httptest.NewRequest("POST", "/api/board/nope/external-artifacts", bytes.NewReader(frame(meta([]byte("x"), "x"), []byte("x"))))
	req.Header.Set(tunnel.HubServerHeader, relaySrc)
	req.Header.Set(tunnel.ViaHeader, cloud.ViaSecret())
	rec := httptest.NewRecorder()
	e.r.ServeHTTP(rec, req)
	assert.Equal(t, 404, rec.Code)
}

// The setting is off by default, the Operator's alone, and Coral Hub cannot change it.
func TestCrossTeamArtifactsSettingIsTheOperatorsAtThisComputer(t *testing.T) {
	e := newArtifactEnv(t)
	req := httptest.NewRequest("GET", "/api/board/beta/settings", nil)
	rec := httptest.NewRecorder()
	e.r.ServeHTTP(rec, req)
	var s map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &s))
	assert.Equal(t, true, s["cross_team_artifacts"], "on by default: a kill switch (#5916)")
	code, out := e.put("/api/board/beta/settings", map[string]any{"subscriber_id": "Operator", "cross_team_artifacts": false}, false)
	require.Equal(t, http.StatusOK, code, out)

	code, _ = e.put("/api/board/beta/settings", map[string]any{"subscriber_id": "Operator", "cross_team_artifacts": true}, true)
	assert.Equal(t, http.StatusForbidden, code, "through the tunnel")
	code, out = e.put("/api/board/beta/settings", map[string]any{"subscriber_id": "Dev", "cross_team_artifacts": true}, false)
	assert.Equal(t, http.StatusForbidden, code, out)
	rec = httptest.NewRecorder()
	e.r.ServeHTTP(rec, httptest.NewRequest("GET", "/api/board/beta/settings", nil))
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &s))
	assert.Equal(t, false, s["cross_team_artifacts"], "still off")
	code, out = e.put("/api/board/beta/settings", map[string]any{"subscriber_id": "Operator", "cross_team_artifacts": true}, false)
	require.Equal(t, http.StatusOK, code, out)
	assert.Equal(t, true, out["cross_team_artifacts"])
}

// A file is received: stored under the sniffed type's name, listed as received with the receiver's provenance,
// and a repeat is a duplicate that keeps the first name and the first receipt.
func TestReceivedArtifactIsStoredListedAndDeduplicated(t *testing.T) {
	e := newArtifactEnv(t)
	e.open()
	data := []byte("# plan\nsome notes\n")
	status, out := e.ok(data, "../plan.md")
	require.Equal(t, 200, status, out)
	id := strings.TrimPrefix(sha256Of(data), "sha256:")
	assert.Equal(t, id, out["artifact_id"])
	got, err := os.ReadFile(filepath.Join(e.dir, "artifacts", "objects", id))
	require.NoError(t, err)
	assert.Equal(t, data, got)

	// Stored, but not listed until its message arrives (#4440).
	assert.Empty(t, e.listed())
	code, msg := e.deliver(relaySrc, "m1")
	require.Equal(t, 200, code, msg)

	rows := e.listed()
	require.Len(t, rows, 1)
	assert.Equal(t, "received", rows[0]["source"])
	assert.Equal(t, "received:"+id, rows[0]["id"], "a row of its own: it never relabels a task result's")
	assert.Equal(t, relaySrc+"/alpha", rows[0]["received_from"])
	assert.Equal(t, "alpha", rows[0]["from_board"])
	assert.Equal(t, "plan.txt", rows[0]["name"], "the extension comes from the sniffed type")
	assert.Equal(t, true, rows[0]["available"])

	// The same bytes again, under another name from another message: answered exactly as the first time
	// (no oracle for what this computer holds), the first name and the first receipt stand.
	first := out
	m := meta(data, "other.pdf")
	m["message_id"] = "m2"
	m["from_board"] = "gamma"
	status, out = e.send("auth", relaySrc, "", frame(m, data))
	require.Equal(t, 200, status, out)
	assert.Equal(t, first, out, "the same answer for new and stored bytes")
	assert.NotContains(t, out, "duplicate")
	code, msg = e.deliver(relaySrc, "m2")
	require.Equal(t, 200, code, msg)
	rows = e.listed()
	require.Len(t, rows, 1)
	assert.Equal(t, "plan.txt", rows[0]["name"])
	assert.Equal(t, "alpha", rows[0]["from_board"], "the first receipt is immutable")
}

// Every refusal is a fixed code, and a refused file is never in the shared store, not even as a temp file.
func TestReceivedArtifactRefusalsNeverReachTheStore(t *testing.T) {
	elf := append([]byte("\x7fELF"), make([]byte, 64)...)
	webp := []byte("RIFF\x10\x00\x00\x00WEBPVP8X\x0a\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00EXIF....")
	tiff := tiffWith([]uint16{0x0100, 0x010F})
	text := []byte("hello\n")
	wrongDigest := meta(text, "t.txt")
	wrongDigest["digest"] = sha256Of([]byte("something else"))
	for _, c := range []struct {
		name   string
		body   []byte
		status int
		code   string
	}{
		{"a renamed ELF", frame(meta(elf, "pic.png"), elf), 400, "type_refused"},
		{"a zip", frame(meta([]byte("PK\x03\x04aaaa"), "a.zip"), []byte("PK\x03\x04aaaa")), 400, "type_refused"},
		{"a TIFF with metadata", frame(meta(tiff, "p.tif"), tiff), 400, "image_metadata"},
		{"a WebP with EXIF", frame(meta(webp, "p.webp"), webp), 400, "image_metadata"},
		{"a wrong digest", frame(wrongDigest, text), 400, "digest_mismatch"},
	} {
		e := newArtifactEnv(t)
		e.open()
		status, out := e.send("auth", relaySrc, "", c.body)
		assert.Equal(t, c.status, status, c.name)
		assert.Equal(t, c.code, out["error"], c.name)
		assert.Empty(t, e.stored(), c.name+": nothing in the store")
		assert.Empty(t, e.listed(), c.name)
	}

	// The store's cap.
	e := newArtifactEnv(t)
	e.open()
	st := storeFor(e.dir)
	st.mu.Lock()
	st.maxBytes = func() int64 { return 4 }
	st.mu.Unlock()
	status, out := e.ok(text, "t.txt")
	assert.Equal(t, 413, status)
	assert.Equal(t, "store_full", out["error"])
	assert.Empty(t, e.stored())
}

// The frame is checked before any byte is trusted: lengths, members, size, and exactly size bytes then the end.
func TestReceivedArtifactFrameRules(t *testing.T) {
	data := []byte("hello\n")
	good := meta(data, "h.txt")
	with := func(f func(m map[string]any)) map[string]any {
		m := meta(data, "h.txt")
		f(m)
		return m
	}
	rawMeta, _ := json.Marshal(good)
	cases := map[string][]byte{
		"length 0":           rawFrame(nil, data),
		"length over 4096":   append(binary.BigEndian.AppendUint32(nil, 4097), bytes.Repeat([]byte(" "), 4097)...),
		"cut inside length":  {0, 0},
		"cut inside meta":    append(binary.BigEndian.AppendUint32(nil, 100), []byte(`{"a":1}`)...),
		"metadata not json":  rawFrame([]byte(`not json`), data),
		"an array":           rawFrame([]byte(`[]`), data),
		"text after object":  rawFrame(append(append([]byte{}, rawMeta...), ` {}`...), data),
		"duplicate member":   rawFrame(bytes.Replace(rawMeta, []byte(`"name":`), []byte(`"name":"a","name":`), 1), data),
		"case-variant":       rawFrame(bytes.Replace(rawMeta, []byte(`"digest"`), []byte(`"Digest"`), 1), data),
		"unknown member":     frame(with(func(m map[string]any) { m["received_from_server"] = "x" }), data),
		"a member missing":   frame(with(func(m map[string]any) { delete(m, "media_type") }), data),
		"size zero":          frame(with(func(m map[string]any) { m["size"] = 0 }), data),
		"size negative":      frame(with(func(m map[string]any) { m["size"] = -1 }), data),
		"size float":         rawFrame(bytes.Replace(rawMeta, []byte(`"size":6`), []byte(`"size":6.0`), 1), data),
		"size exponent":      rawFrame(bytes.Replace(rawMeta, []byte(`"size":6`), []byte(`"size":6e0`), 1), data),
		"size as text":       frame(with(func(m map[string]any) { m["size"] = "6" }), data),
		"size over the cap":  frame(with(func(m map[string]any) { m["size"] = maxReceivedArtifact + 1 }), data),
		"size overflows":     rawFrame(bytes.Replace(rawMeta, []byte(`"size":6`), []byte(`"size":99999999999999999999`), 1), data),
		"short body":         frame(with(func(m map[string]any) { m["size"] = 7 }), data),
		"extra byte":         frame(good, append(append([]byte{}, data...), 'x')),
		"bad digest syntax":  frame(with(func(m map[string]any) { m["digest"] = "sha256:ABC" }), data),
		"bad message id":     frame(with(func(m map[string]any) { m["message_id"] = "a b" }), data),
		"bad media type":     frame(with(func(m map[string]any) { m["media_type"] = "text/plain; x=y" }), data),
		"direction override": frame(with(func(m map[string]any) { m["name"] = "a\u202Egpj.exe" }), data),
		"control in name":    frame(with(func(m map[string]any) { m["name"] = "a\x07b" }), data),
		"name too long":      frame(with(func(m map[string]any) { m["name"] = strings.Repeat("a", 201) }), data),
		"empty name":         frame(with(func(m map[string]any) { m["name"] = "" }), data),
		"invalid utf-8":      rawFrame(bytes.Replace(rawMeta, []byte(`"alpha"`), []byte("\"\xff\xfe\""), 1), data),
	}
	for name, body := range cases {
		e := newArtifactEnv(t)
		e.open()
		status, out := e.send("auth", relaySrc, "", body)
		assert.Equal(t, 400, status, name)
		assert.Equal(t, "bad_frame", out["error"], name)
		assert.Empty(t, e.stored(), name)
	}
	e := newArtifactEnv(t)
	e.open()
	status, out := e.send("auth", relaySrc, "", frame(good, data))
	assert.Equal(t, 200, status, out)
}

// A declared length past the cap is refused before a byte is read.
func TestReceivedArtifactOverTheCapIsRefusedByItsDeclaredLength(t *testing.T) {
	e := newArtifactEnv(t)
	e.open()
	req := httptest.NewRequest("POST", "/api/board/beta/external-artifacts", &neverRead{t})
	req.ContentLength = 4 + maxReceivedMeta + maxReceivedArtifact + 1
	req.Header.Set(tunnel.HubServerHeader, relaySrc)
	req.Header.Set(tunnel.ViaHeader, cloud.ViaSecret())
	rec := httptest.NewRecorder()
	e.r.ServeHTTP(rec, req)
	assert.Equal(t, 413, rec.Code)
	assert.Contains(t, rec.Body.String(), "artifact_too_large")
	// No length at all (chunked) is refused too.
	req = httptest.NewRequest("POST", "/api/board/beta/external-artifacts", &neverRead{t})
	req.ContentLength = -1
	req.Header.Set(tunnel.HubServerHeader, relaySrc)
	req.Header.Set(tunnel.ViaHeader, cloud.ViaSecret())
	rec = httptest.NewRecorder()
	e.r.ServeHTTP(rec, req)
	assert.Equal(t, 400, rec.Code)
}

type neverRead struct{ t *testing.T }

func (n *neverRead) Read([]byte) (int, error) {
	n.t.Error("the body was read")
	return 0, fmt.Errorf("read")
}

// A hostile name is display text only: it never becomes a path or an extension.
func TestReceivedArtifactHostileNamesAreReduced(t *testing.T) {
	for sent, want := range map[string]string{
		"../../etc/run.sh": "run.txt",
		"nul":              "_nul.txt",
		"CON.exe":          "_CON.txt",
		"invoice.pdf.exe":  "invoice.pdf.txt",
	} {
		e := newArtifactEnv(t)
		e.open()
		data := []byte("echo hi\n")
		status, out := e.ok(data, sent)
		require.Equal(t, 200, status, out)
		code, msg := e.deliver(relaySrc, "m1")
		require.Equal(t, 200, code, msg)
		rows := e.listed()
		require.Len(t, rows, 1)
		assert.Equal(t, want, rows[0]["name"], sent)
	}
}

// The receiver's own quotas: a count an hour and bytes a day per source, bytes a day per target and for the
// whole Coral, each answered 429 with its limit, and the window moves on.
func TestReceivedLimiter(t *testing.T) {
	now := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	newL := func() *receivedLimiter { return &receivedLimiter{now: func() time.Time { return now }} }

	l := newL()
	for i := 0; i < receivedPerSourceHour; i++ {
		ok, _, _ := l.admit("a", "t", 1)
		require.True(t, ok, i)
	}
	ok, limit, retry := l.admit("a", "t", 1)
	assert.False(t, ok)
	assert.Equal(t, "hourly", limit)
	assert.Equal(t, time.Hour, retry)
	ok, _, _ = l.admit("b", "t", 1)
	assert.True(t, ok, "another source has its own count")
	now = now.Add(time.Hour + time.Second)
	ok, _, _ = l.admit("a", "t", 1)
	assert.True(t, ok, "an hour later")

	l = newL()
	ok, _, _ = l.admit("a", "t1", receivedSourceDay)
	require.True(t, ok)
	ok, limit, _ = l.admit("a", "t2", 1)
	assert.False(t, ok)
	assert.Equal(t, "daily", limit)

	l = newL()
	for _, src := range []string{"a", "b"} { // a source is held to its own daily bytes, so two fill a board
		ok, _, _ = l.admit(src, "t", receivedTargetDay/2)
		require.True(t, ok)
	}
	ok, limit, _ = l.admit("c", "t", 1)
	assert.False(t, ok)
	assert.Equal(t, "target", limit)
	ok, _, _ = l.admit("c", "other", 1)
	assert.True(t, ok, "another board")

	l = newL()
	for _, s := range []string{"a", "b", "c", "d", "e"} {
		for i := 0; i < 2; i++ {
			ok, _, _ = l.admit(s, fmt.Sprintf("t-%s-%d", s, i), receivedSourceDay/2)
			require.True(t, ok)
		}
	}
	ok, limit, _ = l.admit("f", "z", 1)
	assert.False(t, ok)
	assert.Equal(t, "account", limit)
	now = now.Add(25 * time.Hour)
	ok, _, _ = l.admit("a", "t", 1)
	assert.True(t, ok, "a day later")
}

// Over the route: the 21st file in an hour is 429 artifact_quota with its limit and a Retry-After, and a
// refused file counts too (a hostile sender cannot probe the gate for free).
func TestReceivedArtifactQuotaOverTheRoute(t *testing.T) {
	t.Parallel() // no shared state: these ran one after another (#5622)
	e := newArtifactEnv(t)
	e.open()
	for i := 0; i < receivedPerSourceHour; i++ {
		data := []byte(fmt.Sprintf("file %d\n", i))
		status, out := e.ok(data, "f.txt")
		require.Equal(t, 200, status, out)
	}
	req := httptest.NewRequest("POST", "/api/board/beta/external-artifacts", bytes.NewReader(frame(meta([]byte("late\n"), "l.txt"), []byte("late\n"))))
	req.Header.Set(tunnel.HubServerHeader, relaySrc)
	req.Header.Set(tunnel.ViaHeader, cloud.ViaSecret())
	rec := httptest.NewRecorder()
	e.r.ServeHTTP(rec, req)
	assert.Equal(t, 429, rec.Code)
	assert.Contains(t, rec.Body.String(), `"limit":"hourly"`)
	assert.Contains(t, rec.Body.String(), "artifact_quota")
	assert.NotEmpty(t, rec.Header().Get("Retry-After"))
}

// Only a few transfers are held in memory at once; the rest are told to retry.
func TestReceivedArtifactInFlightCap(t *testing.T) {
	e := newArtifactEnv(t)
	e.open()
	for i := 0; i < maxReceivedInFlight; i++ {
		receivedSlots <- struct{}{}
	}
	defer func() {
		for i := 0; i < maxReceivedInFlight; i++ {
			<-receivedSlots
		}
	}()
	data := []byte("hello\n")
	status, out := e.ok(data, "h.txt")
	assert.Equal(t, 429, status, out)
	assert.Empty(t, e.stored())
}

// The caps hold on their own, not only through the other checks: a consistent frame whose size is one over the
// cap, and valid metadata one byte over 4096, are refused as bad frames.
func TestReceivedArtifactCapsHoldOnTheirOwn(t *testing.T) {
	e := newArtifactEnv(t)
	e.open()
	big := bytes.Repeat([]byte("a"), maxReceivedArtifact+1)
	m := meta(big, "big.txt")
	req := httptest.NewRequest("POST", "/api/board/beta/external-artifacts", bytes.NewReader(frame(m, big)))
	req.Header.Set(tunnel.HubServerHeader, relaySrc)
	req.Header.Set(tunnel.ViaHeader, cloud.ViaSecret())
	rec := httptest.NewRecorder()
	e.r.ServeHTTP(rec, req)
	assert.Equal(t, 400, rec.Code)
	assert.Contains(t, rec.Body.String(), "bad_frame")
	assert.Empty(t, e.stored())

	data := []byte("hello\n")
	raw, _ := json.Marshal(meta(data, "h.txt"))
	raw = append(raw, bytes.Repeat([]byte(" "), maxReceivedMeta+1-len(raw))...)
	require.Len(t, raw, maxReceivedMeta+1)
	status, out := e.send("auth", relaySrc, "", rawFrame(raw, data))
	assert.Equal(t, 400, status)
	assert.Equal(t, "bad_frame", out["error"])
	raw = raw[:maxReceivedMeta]
	status, out = e.send("auth", relaySrc, "", rawFrame(raw, data))
	assert.Equal(t, 200, status, out)
}

// A body with a byte past size is refused even when the declared length says otherwise (the read, not the
// header, has the last word).
func TestReceivedArtifactExtraByteBeyondTheDeclaredLength(t *testing.T) {
	e := newArtifactEnv(t)
	e.open()
	data := []byte("hello\n")
	body := frame(meta(data, "h.txt"), data)
	req := httptest.NewRequest("POST", "/api/board/beta/external-artifacts", bytes.NewReader(append(append([]byte{}, body...), 'x')))
	req.ContentLength = int64(len(body))
	req.Header.Set(tunnel.HubServerHeader, relaySrc)
	req.Header.Set(tunnel.ViaHeader, cloud.ViaSecret())
	rec := httptest.NewRecorder()
	e.r.ServeHTTP(rec, req)
	assert.Equal(t, 400, rec.Code)
	assert.Contains(t, rec.Body.String(), "bad_frame")
	assert.Empty(t, e.stored())
}

// One linked Coral cannot hold every slot: a second transfer from a source that already has one in flight is
// told to retry; another source, and the same one afterwards, are served.
func TestReceivedArtifactOneInFlightPerSource(t *testing.T) {
	e := newArtifactEnv(t)
	e.open()
	require.True(t, e.h.received.begin(relaySrc))
	data := []byte("hello\n")
	status, out := e.ok(data, "h.txt")
	assert.Equal(t, 429, status, out)
	assert.Equal(t, "rate_limited", out["error"])
	assert.Empty(t, e.stored())
	e.h.received.end(relaySrc)
	status, out = e.ok(data, "h.txt")
	assert.Equal(t, 200, status, out)
	assert.False(t, e.h.received.begin(relaySrc) && e.h.received.begin(relaySrc), "begin refuses a second")
	e.h.received.end(relaySrc)
	assert.True(t, e.h.received.begin("other-source"), "another source is not held up")
}

// The receiver takes what the hub's default relays: 8 MiB a file, plus the frame (the hub's own default is
// the same 8 MiB + 4 + 4 KiB + 256), so a bigger file is refused before it is read.
func TestReceivedArtifactCapIsTheHubsDefault(t *testing.T) {
	assert.EqualValues(t, 8<<20, maxReceivedArtifact)
	e := newArtifactEnv(t)
	e.open()
	req := httptest.NewRequest("POST", "/api/board/beta/external-artifacts", &neverRead{t})
	req.ContentLength = 4 + maxReceivedMeta + 8<<20 + 1
	req.Header.Set(tunnel.HubServerHeader, relaySrc)
	req.Header.Set(tunnel.ViaHeader, cloud.ViaSecret())
	rec := httptest.NewRecorder()
	e.r.ServeHTTP(rec, req)
	assert.Equal(t, 413, rec.Code)
}

func (e *artifactEnv) sendUnder(src, relayID string, data []byte) (int, map[string]any) {
	m := meta(data, "f.txt")
	m["message_id"] = relayID
	return e.send("auth", src, "", frame(m, data))
}

const otherSrc = "7d1c0e55-3b6a-4f08-9e21-aa00bb11cc22"

// A file whose message has not arrived is stored but not listed; the message lists it, naming the message
// (#4440, Security's #4438 SHOULD 3).
func TestReceivedFilesWaitForTheirMessage(t *testing.T) {
	e := newArtifactEnv(t)
	e.open()
	one, two := []byte("first file\n"), []byte("second file\n")
	for _, d := range [][]byte{one, two} {
		status, out := e.sendUnder(relaySrc, "m1", d)
		require.Equal(t, 200, status, out)
	}
	assert.Len(t, e.stored(), 4, "both files are in the store (object and sidecar)")
	assert.Empty(t, e.listed(), "no message, nothing listed")

	// A message with another id, or from another server, does not claim them.
	_, _ = e.put("/api/board/beta/settings", map[string]any{"subscriber_id": "Operator", "cross_team_servers": []string{relaySrc, otherSrc}}, false)
	code, out := e.deliver(relaySrc, "m9")
	require.Equal(t, 200, code, out)
	code, out = e.deliver(otherSrc, "m1")
	require.Equal(t, 200, code, out)
	assert.Empty(t, e.listed(), "the id and the source must both match")

	code, out = e.deliver(relaySrc, "m1")
	require.Equal(t, 200, code, out)
	assert.Len(t, e.listed(), 2)
	id := strings.TrimPrefix(sha256Of(one), "sha256:")
	rec, ok := e.h.crossTeamRecord("received", "beta", id)
	require.True(t, ok)
	assert.Equal(t, int64(out["target_message_id"].(float64)), rec.MessageID, "the row names the message it came with")
	left, _ := os.ReadDir(e.h.boardRecordDir("pending", "beta"))
	assert.Empty(t, left, "listed ones are no longer pending")
}

// A retry of a message whose earlier attempt failed partway is charged once, not once a push; the free
// repeats of one file are bounded.
func TestReceivedRetryOfAMessageIsChargedOnce(t *testing.T) {
	e := newArtifactEnv(t)
	e.open()
	one := []byte("first file\n")
	for i := 0; i < 4; i++ {
		status, out := e.sendUnder(relaySrc, "m1", one)
		require.Equal(t, 200, status, out)
	}
	assert.Len(t, e.h.received.events, 1, "four pushes of one file for one message, one charge")
	status, out := e.sendUnder(relaySrc, "m2", one)
	require.Equal(t, 200, status, out)
	assert.Len(t, e.h.received.events, 2, "another message id is charged")

	// The same message and file again and again: the sixth repeat pays.
	for i := 0; i < 8; i++ {
		_, _ = e.sendUnder(relaySrc, "m3", one)
	}
	assert.Len(t, e.h.received.events, 4, "m3: the first push, and the push after its five free repeats")
}

// No message within the time: the file is never listed, and it is not deleted.
func TestReceivedFileWithoutAMessageInTimeIsNeverListed(t *testing.T) {
	e := newArtifactEnv(t)
	e.open()
	data := []byte("late file\n")
	status, out := e.sendUnder(relaySrc, "m1", data)
	require.Equal(t, 200, status, out)
	e.now = e.now.Add(receivedPendingFor + time.Minute)
	code, out := e.deliver(relaySrc, "m1")
	require.Equal(t, 200, code, out)
	assert.Empty(t, e.listed())
	_, err := os.Stat(filepath.Join(e.dir, "artifacts", "objects", strings.TrimPrefix(sha256Of(data), "sha256:")))
	assert.NoError(t, err, "the stored file stays")

	// The next receipt on the board keeps the expired record (#4441): it is the memory of a stored file no board lists, which counts toward the
	// received-files cap and can be deleted in Settings. A record whose object is gone is the one it sweeps.
	status, out = e.sendUnder(relaySrc, "m2", []byte("another\n"))
	require.Equal(t, 200, status, out)
	left, _ := os.ReadDir(e.h.boardRecordDir("pending", "beta"))
	assert.Len(t, left, 2, "the expired record and the new one")
	require.NoError(t, os.Remove(filepath.Join(e.dir, "artifacts", "objects", strings.TrimPrefix(sha256Of(data), "sha256:"))))
	status, out = e.sendUnder(relaySrc, "m3", []byte("a third\n"))
	require.Equal(t, 200, status, out)
	left, _ = os.ReadDir(e.h.boardRecordDir("pending", "beta"))
	assert.Len(t, left, 2, "the record of the deleted object was swept")
}

// The switch is read again just before a file is stored: turned off while a file arrives, the file is not
// stored; and with it off when the message comes, nothing is listed.
func TestReceivedSwitchTurnedOffBetweenFilesStopsTheSecond(t *testing.T) {
	e := newArtifactEnv(t)
	e.open()
	one, two := []byte("first file\n"), []byte("second file\n")
	status, out := e.sendUnder(relaySrc, "m1", one)
	require.Equal(t, 200, status, out)

	receivedBeforeStore = func() {
		_, _ = e.put("/api/board/beta/settings", map[string]any{"subscriber_id": "Operator", "cross_team_artifacts": false}, false)
	}
	t.Cleanup(func() { receivedBeforeStore = nil })
	status, out = e.sendUnder(relaySrc, "m1", two)
	assert.Equal(t, http.StatusForbidden, status, out)
	assert.Equal(t, "artifacts_off", out["error"])
	assert.Len(t, e.stored(), 2, "only the first file is stored (object and sidecar)")
	receivedBeforeStore = nil

	code, out := e.deliver(relaySrc, "m1")
	require.Equal(t, 200, code, out)
	assert.Empty(t, e.listed(), "files are off: the first one is not listed either")
}

// A file the Operator deleted while it waited is not listed when its message comes.
func TestReceivedFileDeletedWhilePendingIsNotListed(t *testing.T) {
	e := newArtifactEnv(t)
	e.open()
	data := []byte("deleted while waiting\n")
	status, out := e.sendUnder(relaySrc, "m1", data)
	require.Equal(t, 200, status, out)
	require.NoError(t, os.Remove(filepath.Join(e.dir, "artifacts", "objects", strings.TrimPrefix(sha256Of(data), "sha256:"))))
	code, out := e.deliver(relaySrc, "m1")
	require.Equal(t, 200, code, out)
	assert.Empty(t, e.listed())
}

// A second source sending bytes the board already has is recorded on the row (the first source stays,
// write-once); the same source and board again adds nothing (#4449, Security's #4438 INFO 3).
func TestReceivedSecondSourceIsRecorded(t *testing.T) {
	e := newArtifactEnv(t)
	e.open()
	_, _ = e.put("/api/board/beta/settings", map[string]any{"subscriber_id": "Operator", "cross_team_servers": []string{relaySrc, otherSrc}}, false)
	data := []byte("the same bytes from two Corals\n")
	id := strings.TrimPrefix(sha256Of(data), "sha256:")

	status, out := e.sendUnder(relaySrc, "m1", data)
	require.Equal(t, 200, status, out)
	code, out := e.deliver(relaySrc, "m1")
	require.Equal(t, 200, code, out)
	rows := e.listed()
	require.Len(t, rows, 1)
	assert.Empty(t, rows[0]["also_received_from"])

	status, out = e.sendUnder(otherSrc, "n1", data)
	require.Equal(t, 200, status, out)
	code, out = e.deliver(otherSrc, "n1")
	require.Equal(t, 200, code, out)
	// The same source and board again (a duplicate message, a retry) adds nothing.
	_, _ = e.sendUnder(otherSrc, "n1", data)
	_, _ = e.deliver(otherSrc, "n1")
	// The first source sending it again under another message adds nothing either.
	_, _ = e.sendUnder(relaySrc, "m2", data)
	_, _ = e.deliver(relaySrc, "m2")

	rows = e.listed()
	require.Len(t, rows, 1)
	assert.Equal(t, relaySrc+"/alpha", rows[0]["received_from"], "the first source stands")
	assert.Equal(t, []any{otherSrc + "/alpha"}, rows[0]["also_received_from"])
	rec, ok := e.h.crossTeamRecord("received", "beta", id)
	require.True(t, ok)
	assert.Equal(t, relaySrc, rec.Server)
	require.Len(t, rec.Also, 1)
	assert.Equal(t, otherSrc, rec.Also[0].Server)
	assert.NotZero(t, rec.Also[0].MessageID)

	// Another board of the first source is a second sender too.
	m := meta(data, "f.txt")
	m["message_id"], m["from_board"] = "m3", "gamma"
	status, out = e.send("auth", relaySrc, "", frame(m, data))
	require.Equal(t, 200, status, out)
	_, _ = e.deliver(relaySrc, "m3")
	rec, _ = e.h.crossTeamRecord("received", "beta", id)
	assert.Len(t, rec.Also, 2)

	// The list is bounded.
	for i := 0; i < maxAlsoReceipts+5; i++ {
		m := meta(data, "f.txt")
		m["message_id"], m["from_board"] = fmt.Sprintf("x%d", i), fmt.Sprintf("board-%d", i)
		e.h.received.events = nil // the hourly count and the message rate are not what is tested
		e.h.crossTeam = crossTeamLimiter{}
		_, _ = e.send("auth", relaySrc, "", frame(m, data))
		_, _ = e.deliver(relaySrc, fmt.Sprintf("x%d", i))
	}
	rec, _ = e.h.crossTeamRecord("received", "beta", id)
	assert.Len(t, rec.Also, maxAlsoReceipts)
}
