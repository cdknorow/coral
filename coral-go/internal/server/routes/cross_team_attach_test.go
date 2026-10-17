package routes

import (
	"bytes"
	"context"
	"encoding/json"
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

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/store"
)

// Sessions of the attach env: each Orchestrator's agent session (id and tmux name).
const (
	alphaOrchSID = "aaaa1111-0000-4000-8000-000000000001"
	betaOrchSID  = "bbbb2222-0000-4000-8000-000000000002"
	betaQASID    = "bbbb3333-0000-4000-8000-000000000003"
)

type attachEnv struct {
	t        *testing.T
	url      string
	h        *BoardHandler
	coralDir string
}

// newAttachEnv: boards alpha and beta, cross-team on, artifacts on (unless off), an artifact store.
// A request says who it is from with test headers the env turns into the identity layer's context:
// X-Test-Session (an agent's verified session id; its tmux name is "claude-<id>") or X-Test-Operator.
func newAttachEnv(t *testing.T, artifactsOn bool) *attachEnv {
	t.Helper()
	bs, err := board.NewStoreWithKey(t.TempDir()+"/board.db", "")
	require.NoError(t, err)
	t.Cleanup(func() { bs.Close() })
	e := &attachEnv{t: t, h: NewBoardHandler(bs), coralDir: t.TempDir()}
	e.h.SetTaskArtifactWriter(e.coralDir, nil)
	// The message rate limit is not what these tests check: an hour passes between sends.
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
	r.Put("/api/board/{project}/settings", e.h.UpdateBoardSettings)
	r.Post("/api/board/{project}/external-messages", e.h.PostExternalMessage)
	r.Get("/api/board/{project}/artifacts", e.h.ListTeamArtifacts)
	srv := httptest.NewServer(r)
	t.Cleanup(func() { srv.Close(); e.h.WaitBackground() })
	e.url = srv.URL
	ctx := context.Background()
	for _, s := range []struct{ board, id, role, sid string }{
		{"alpha", "Orchestrator", "Orchestrator", alphaOrchSID},
		{"beta", "Orchestrator", "Orchestrator", betaOrchSID},
		{"beta", "QA", "QA Engineer", betaQASID},
		{"gamma", "Orchestrator", "Orchestrator", "cccc4444-0000-4000-8000-000000000004"},
	} {
		_, err := bs.Subscribe(ctx, s.board, s.id, s.role, "claude-"+s.sid, nil, "mentions")
		require.NoError(t, err)
	}
	for _, b := range []string{"alpha", "beta", "gamma"} {
		_, err := bs.UpdateBoardSettings(ctx, b, board.BoardSettingsUpdate{CrossTeamMessaging: ptrBool(true), CrossTeamArtifacts: ptrBool(artifactsOn)})
		require.NoError(t, err)
	}
	return e
}

func ptrBool(b bool) *bool { return &b }

// send posts a cross-team message from board from to board to, as its Orchestrator (session sid), or
// as the Operator when sid is "operator".
func (e *attachEnv) send(from, to, sid string, attach ...string) (int, map[string]any) {
	e.t.Helper()
	sender, sessionName := "Orchestrator", ""
	if sid != "operator" {
		sessionName = "claude-" + sid
	} else {
		sender = "Operator"
	}
	b, _ := json.Marshal(map[string]any{"from_board": from, "subscriber_id": sender, "session_name": sessionName, "content": "here you go", "attach": attach})
	req, _ := http.NewRequest("POST", e.url+"/api/board/"+to+"/external-messages", bytes.NewReader(b))
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
	return resp.StatusCode, out
}

func (e *attachEnv) upload(sid, content string) string {
	e.t.Helper()
	return writeUpload(e.t, e.coralDir, sid, content, "file-"+content[:4]+".md", "2026-10-17T00:00:00Z")
}

func (e *attachEnv) messages(board string) int {
	e.t.Helper()
	msgs, err := e.h.bs.ListMessages(context.Background(), board, 1000, 0, 0)
	require.NoError(e.t, err)
	return len(msgs)
}

func uri(id string) string { return "coral://artifacts/" + id }

// An Orchestrator attaches its own upload: the message carries the link, and the sender's board
// gets a shared record and the receiver's a received one, both of the same object (no copy).
func TestAttachOwnUpload(t *testing.T) {
	e := newAttachEnv(t, true)
	d := e.upload(alphaOrchSID, "alpha's report")
	code, out := e.send("alpha", "beta", alphaOrchSID, uri(d))
	require.Equal(t, http.StatusOK, code, out)
	assert.Equal(t, []any{uri(d)}, out["attached"])
	assert.Contains(t, out["message"].(map[string]any)["content"], uri(d))
	assert.Contains(t, out["mirror"].(map[string]any)["content"], uri(d))
	shared, ok := e.h.crossTeamRecord("shared", "alpha", d)
	require.True(t, ok)
	assert.Equal(t, "beta", shared.OtherBoard)
	received, ok := e.h.crossTeamRecord("received", "beta", d)
	require.True(t, ok)
	assert.Equal(t, "alpha", received.OtherBoard)
	entries, _ := os.ReadDir(filepath.Join(e.coralDir, "artifacts", "objects"))
	assert.Len(t, entries, 2, "one object and its sidecar: nothing copied")
}

// Off on either board: refused, naming the board(s), nothing posted.
func TestAttachNeedsArtifactsOnBothBoards(t *testing.T) {
	e := newAttachEnv(t, false)
	d := e.upload(alphaOrchSID, "alpha's report")
	code, out := e.send("alpha", "beta", alphaOrchSID, uri(d))
	require.Equal(t, http.StatusForbidden, code)
	assert.Equal(t, "artifacts_off", out["code"])
	assert.Contains(t, out["error"], `"alpha" and "beta"`)
	_, err := e.h.bs.UpdateBoardSettings(context.Background(), "alpha", board.BoardSettingsUpdate{CrossTeamArtifacts: ptrBool(true)})
	require.NoError(t, err)
	_, out = e.send("alpha", "beta", alphaOrchSID, uri(d))
	assert.Contains(t, out["error"], `"beta"`)
	assert.NotContains(t, out["error"], `"alpha"`)
	assert.Equal(t, 0, e.messages("beta"))
}

// L2, T1, T2: another team's upload (named in a task result, or linked in a post) is refused exactly
// like an id that does not exist, and its bytes are never read.
func TestAttachAnotherTeamsFileIsRefusedLikeAnUnknownID(t *testing.T) {
	e := newAttachEnv(t, true)
	d := e.upload(betaQASID, "beta's private file")
	read := 0
	attachBeforeHash = func(string) { read++ }
	t.Cleanup(func() { attachBeforeHash = nil })
	completeWithArtifacts(t, e.h, "alpha", "cites beta's file", []board.TaskArtifact{{Name: "x.md", URI: uri(d)}})
	_, err := e.h.bs.PostMessage(context.Background(), "alpha", "Orchestrator", "see "+uri(d), nil)
	require.NoError(t, err)

	code, theirs := e.send("alpha", "beta", alphaOrchSID, uri(d))
	require.Equal(t, http.StatusBadRequest, code)
	unknown := strings.Repeat("9f", 32)
	code2, none := e.send("alpha", "beta", alphaOrchSID, uri(unknown))
	require.Equal(t, http.StatusBadRequest, code2)
	assert.Equal(t, "not_yours", theirs["code"])
	assert.Equal(t, theirs["code"], none["code"])
	assert.Equal(t, strings.Replace(theirs["error"].(string), d, unknown, 1), none["error"], "the same answer, but for the id")
	assert.Equal(t, 0, read, "no bytes read")
	assert.Equal(t, 0, e.messages("beta"))
}

// T4 and the attach-time rule: a task result citing D grants nothing until the sender's own session
// has an upload record of it; then the same row attaches with no change to it.
func TestAttachOldTaskRowsGrantNothing(t *testing.T) {
	e := newAttachEnv(t, true)
	d := storeManagedArtifact(t, e.coralDir, "legacy bytes", true, true)
	completeWithArtifacts(t, e.h, "alpha", "legacy result", []board.TaskArtifact{{Name: "old.md", URI: uri(d)}})
	code, out := e.send("alpha", "beta", alphaOrchSID, uri(d))
	require.Equal(t, http.StatusBadRequest, code)
	assert.Equal(t, "not_yours", out["code"])
	writeUpload(t, e.coralDir, alphaOrchSID, "legacy bytes", "old.md", "2026-10-17T00:00:00Z")
	code, out = e.send("alpha", "beta", alphaOrchSID, uri(d))
	assert.Equal(t, http.StatusOK, code, out)
}

// T5 and L10: what a board received, or shared, is not attachable onward; a received file is
// citable by the board that received it, and by no other.
func TestAttachReceivedAndSharedRowsAreNotAttachable(t *testing.T) {
	e := newAttachEnv(t, true)
	d := e.upload(alphaOrchSID, "alpha's report")
	code, out := e.send("alpha", "beta", alphaOrchSID, uri(d))
	require.Equal(t, http.StatusOK, code, out)

	code, out = e.send("beta", "gamma", betaOrchSID, uri(d))
	require.Equal(t, http.StatusBadRequest, code, "beta received it: not forwarded")
	assert.Equal(t, "not_yours", out["code"])

	// beta's tasks may cite what beta received; gamma's may not.
	_, task := completeVia(t, e.h, "beta", vetCaller{actor: Actor{Kind: ActorToken, Name: "Orchestrator"}, session: betaOrchSID}, false, board.TaskArtifact{Name: "in.md", URI: uri(d)})
	assert.Equal(t, provenanceUploaded, task.Workflow.Artifacts[0].Provenance)
	_, task = completeVia(t, e.h, "gamma", vetCaller{actor: Actor{Kind: ActorToken, Name: "Orchestrator"}, session: "cccc4444-0000-4000-8000-000000000004"}, false, board.TaskArtifact{Name: "in.md", URI: uri(d)})
	assert.Equal(t, provenanceUnverified, task.Workflow.Artifacts[0].Provenance)

	// alpha's shared record is not an upload either: with alpha's upload record gone, alpha can't
	// send it again on the strength of having shared it.
	require.NoError(t, os.Remove(filepath.Join(uploadsDir(e.coralDir, alphaOrchSID), d+".json")))
	e.h.uploads = uploadDirCache{}
	code, out = e.send("alpha", "gamma", alphaOrchSID, uri(d))
	require.Equal(t, http.StatusBadRequest, code)
	assert.Equal(t, "not_yours", out["code"])
}

// T6: one open from the check to the read. Bytes rewritten in place after the check fail the send
// (nothing posted); a file deleted before the open fails it; one deleted after the open (the handle
// is held) is sent with its original bytes.
func TestAttachCheckAndReadAreOneStep(t *testing.T) {
	e := newAttachEnv(t, true)
	d := e.upload(alphaOrchSID, "the original bytes")
	path := filepath.Join(e.coralDir, "artifacts", "objects", d)
	t.Cleanup(func() { attachBeforeHash = nil })

	attachBeforeHash = func(p string) { require.NoError(t, os.WriteFile(p, []byte("the swapped bytes!"), 0o644)) } // same size
	code, out := e.send("alpha", "beta", alphaOrchSID, uri(d))
	require.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "attach_changed", out["code"])
	assert.Equal(t, 0, e.messages("beta"), "nothing sent")

	require.NoError(t, os.WriteFile(path, []byte("the original bytes"), 0o644))
	attachBeforeHash = func(p string) { require.NoError(t, os.Remove(p)) }
	code, out = e.send("alpha", "beta", alphaOrchSID, uri(d))
	require.Equal(t, http.StatusOK, code, "deleted after the open: the open handle still reads the original: %v", out)

	attachBeforeHash = nil
	code, out = e.send("alpha", "beta", alphaOrchSID, uri(d)) // the object is gone now
	require.Equal(t, http.StatusBadRequest, code)
	assert.Equal(t, "not_yours", out["code"])
}

// The per-send limits: 4 attachments, 8 MiB each, 16 MiB in all; a link that is not a managed one.
func TestAttachLimits(t *testing.T) {
	e := newAttachEnv(t, true)
	var five []string
	for i := 0; i < 5; i++ {
		five = append(five, uri(e.upload(alphaOrchSID, strings.Repeat(string(rune('a'+i)), 10))))
	}
	code, out := e.send("alpha", "beta", alphaOrchSID, five...)
	require.Equal(t, http.StatusBadRequest, code)
	assert.Equal(t, "attach_too_many", out["code"])
	code, out = e.send("alpha", "beta", alphaOrchSID, five[0], five[0], five[0], five[0], five[0], five[1])
	assert.Equal(t, http.StatusOK, code, "the same link twice is one attachment: %v", out)

	big := e.upload(alphaOrchSID, strings.Repeat("B", maxCrossTeamAttachBytes+1))
	code, out = e.send("alpha", "beta", alphaOrchSID, uri(big))
	require.Equal(t, http.StatusRequestEntityTooLarge, code)
	assert.Equal(t, "attach_too_large", out["code"])

	var three []string
	for i := 0; i < 3; i++ {
		three = append(three, uri(e.upload(alphaOrchSID, strings.Repeat(string(rune('p'+i)), 6<<20))))
	}
	code, out = e.send("alpha", "beta", alphaOrchSID, three...)
	require.Equal(t, http.StatusRequestEntityTooLarge, code)
	assert.Equal(t, "attach_total_too_large", out["code"])

	code, out = e.send("alpha", "beta", alphaOrchSID, "https://example.com/x")
	require.Equal(t, http.StatusBadRequest, code)
	assert.Equal(t, "bad_attach", out["code"])
}

// The Operator (a person) may send any stored object; a missing one is not found. An agent's token
// for another session than the sending Orchestrator's attaches nothing.
func TestAttachOperatorAndForeignTokens(t *testing.T) {
	e := newAttachEnv(t, true)
	d := e.upload(betaQASID, "anyone's file")
	code, out := e.send("alpha", "beta", "operator", uri(d))
	require.Equal(t, http.StatusOK, code, out)
	code, out = e.send("alpha", "beta", "operator", uri(strings.Repeat("7e", 32)))
	require.Equal(t, http.StatusNotFound, code)
	assert.Equal(t, "attach_not_found", out["code"])

	// beta's QA uploaded d; a request whose token is QA's but which names alpha's Orchestrator's
	// session is refused before attachments (the session check), and with the right session name
	// but QA's token, attaches nothing.
	mine := e.upload(betaQASID, "qa's own file")
	b, _ := json.Marshal(map[string]any{"from_board": "alpha", "subscriber_id": "Orchestrator", "session_name": "claude-" + alphaOrchSID, "content": "hi", "attach": []string{uri(mine)}})
	req, _ := http.NewRequest("POST", e.url+"/api/board/beta/external-messages", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Session", betaQASID)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var got map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&got)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Equal(t, "not_yours", got["code"])
}

// Each check stands alone: another team's EMPTY file (its size matches a missing record's zero) is
// refused by the record check; an own record whose size disagrees with the object is refused by the
// size check.
func TestAttachRecordAndSizeChecksEachRefuse(t *testing.T) {
	e := newAttachEnv(t, true)
	empty := storeManagedArtifact(t, e.coralDir, "", true, true)
	dir := uploadsDir(e.coralDir, betaQASID)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	rec, _ := json.Marshal(agentUploadRecord{agentArtifactMeta{Name: "empty", Size: 0, Digest: "sha256:" + empty}, "2026-10-17T00:00:00Z"})
	require.NoError(t, os.WriteFile(filepath.Join(dir, empty+".json"), rec, 0o644))
	code, out := e.send("alpha", "beta", alphaOrchSID, uri(empty))
	require.Equal(t, http.StatusBadRequest, code)
	assert.Equal(t, "not_yours", out["code"])

	d := e.upload(alphaOrchSID, "sized bytes")
	wrong, _ := json.Marshal(agentUploadRecord{agentArtifactMeta{Name: "x", Size: 999, Digest: "sha256:" + d}, "2026-10-17T00:00:00Z"})
	require.NoError(t, os.WriteFile(filepath.Join(uploadsDir(e.coralDir, alphaOrchSID), d+".json"), wrong, 0o644))
	e.h.uploads = uploadDirCache{}
	code, out = e.send("alpha", "beta", alphaOrchSID, uri(d))
	require.Equal(t, http.StatusBadRequest, code)
	assert.Equal(t, "not_yours", out["code"])
}

// The rows: the sender's Team Artifacts list a "shared" row (where it went), the receiver's a
// "received" one (where it came from, who sent it, no producer on the team), each a reference to
// the stored object; another board lists neither. Prune counts them as links; the Operator's delete
// removes them with the object.
func TestSharedAndReceivedRowsListPruneAndDelete(t *testing.T) {
	e := newAttachEnv(t, true)
	d := e.upload(alphaOrchSID, "alpha's report")
	code, out := e.send("alpha", "beta", alphaOrchSID, uri(d))
	require.Equal(t, http.StatusOK, code, out)

	find := func(board, source string) *teamArtifact {
		resp, err := http.Get(e.url + "/api/board/" + board + "/artifacts")
		require.NoError(t, err)
		defer resp.Body.Close()
		var got teamArtifactsResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		for i := range got.Artifacts {
			if got.Artifacts[i].Source == source {
				return &got.Artifacts[i]
			}
		}
		return nil
	}
	shared := find("alpha", "shared")
	require.NotNil(t, shared)
	assert.Equal(t, "shared:"+d, shared.ID)
	assert.Equal(t, "beta", shared.OtherBoard)
	assert.Equal(t, "local", shared.OtherServer)
	assert.True(t, shared.Available)
	require.NotNil(t, shared.Size)
	assert.Equal(t, int64(len("alpha's report")), *shared.Size)
	received := find("beta", "received")
	require.NotNil(t, received)
	assert.Equal(t, "alpha", received.OtherBoard)
	assert.Equal(t, "Orchestrator", received.SentBy)
	assert.Nil(t, received.ProducedBy, "not one of beta's agents")
	assert.NotEmpty(t, received.DownloadURL)
	assert.Nil(t, find("gamma", "received"))
	assert.Nil(t, find("gamma", "shared"))

	refs := map[string]bool{}
	require.NoError(t, scanCrossTeamRecordRefs(e.coralDir, refs))
	assert.True(t, refs[d], "a listed row is a link for prune")

	_, removed, found, err := deleteArtifactFiles(e.coralDir, d)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, 3, removed, "the upload record, the shared row and the received row")
	_, ok := e.h.crossTeamRecord("shared", "alpha", d)
	assert.False(t, ok)
	_, ok = e.h.crossTeamRecord("received", "beta", d)
	assert.False(t, ok)
}
