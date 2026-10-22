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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/pkg/tunnel"
)

// #5865: a relayed FILE follows the hub's link snapshot as a message does (#5862): on a links connection a fresh link from the sending team (X-Coral-Hub-Source-Team, never the frame's own
// claim) with the board's messaging and files switches on; a connection whose hub no longer advertises links is refused; a hub that never did keeps the old rule.

type fileDoorEnv struct {
	*doorEnv
	dir string
}

func newFileDoorEnv(t *testing.T) *fileDoorEnv {
	t.Helper()
	e := &fileDoorEnv{doorEnv: newDoorEnv(t), dir: t.TempDir()}
	e.h.coralDir = e.dir
	t.Cleanup(e.h.WaitBackground)
	e.r.Post("/api/board/{project}/external-artifacts", e.h.PostExternalArtifact)
	e.filesOn(e.target, true)
	e.filesOn(e.target2, true)
	return e
}

func (e *fileDoorEnv) filesOn(board_ string, on bool) {
	e.t.Helper()
	_, err := e.bs.UpdateBoardSettings(context.Background(), board_, board.BoardSettingsUpdate{CrossTeamArtifacts: &on})
	require.NoError(e.t, err)
}

// file posts a relayed file to target from server src, whose sending team is team as Coral Hub names it ("" sends none). The frame claims another team's cross-team id, which a links
// connection never reads.
func (e *fileDoorEnv) file(src, team, target string, cap bool) (int, string) {
	e.t.Helper()
	e.n++
	data := []byte("the file " + strings.Repeat("x", e.n))
	m := map[string]any{"message_id": "f" + strings.Repeat("a", e.n), "to_address": "srv/" + target, "from_board": "QA", "digest": sha256Of(data), "size": len(data), "name": "a.txt",
		"media_type": "text/plain", "from_cross_team_id": "x-" + strings.Repeat("f", 16)}
	req := httptest.NewRequest(http.MethodPost, "/api/board/"+target+"/external-artifacts", bytes.NewReader(frame(m, data)))
	req.Header.Set(tunnel.HubServerHeader, src)
	req.Header.Set(tunnel.ViaHeader, cloud.ViaSecret())
	if team != "" {
		req.Header.Set(tunnel.HubSourceTeamHeader, team)
	}
	if cap {
		req.Header.Set(tunnel.HubLinksCapHeader, "1")
	}
	rec := httptest.NewRecorder()
	e.r.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func (e *fileDoorEnv) storedCount() int {
	entries, _ := os.ReadDir(filepath.Join(e.dir, "artifacts", "objects"))
	return len(entries)
}

func TestTheLinksDoorAdmitsAFileFromALinkedTeamAndRefusesEveryOtherCause(t *testing.T) {
	e := newFileDoorEnv(t)
	e.snapshotOK(1, e.link("lab2", srcTeam1, e.target))
	code, body := e.file("lab2", srcTeam1, e.target, true)
	require.Equal(t, 200, code, body)
	assert.Positive(t, e.storedCount(), "stored")
	before := e.storedCount()

	refusals := map[string][2]any{}
	record := func(name string, code int, body string) { refusals[name] = [2]any{code, body} }
	c, b := e.file("lab2", srcTeam2, e.target, true)
	record("another team", c, b)
	c, b = e.file("lab2", "", e.target, true)
	record("no source team from the hub", c, b)
	c, b = e.file("lab3", srcTeam1, e.target, true)
	record("another server", c, b)
	c, b = e.file("lab2", srcTeam1, e.target2, true)
	record("a team it is not linked to", c, b)
	e.filesOn(e.target, false)
	c, b = e.file("lab2", srcTeam1, e.target, true)
	record("the files switch off", c, b)
	e.filesOn(e.target, true)
	e.switchOn(e.target, false)
	c, b = e.file("lab2", srcTeam1, e.target, true)
	record("the messaging switch off", c, b)
	e.switchOn(e.target, true)
	e.servers(e.target2, "lab9")
	c, b = e.file("lab9", srcTeam1, e.target2, true)
	record("on the server list but not linked", c, b)

	var first [2]any
	for name, r := range refusals {
		assert.Equal(t, 403, r[0], name)
		if first == ([2]any{}) {
			first = r
		}
		assert.Equal(t, first, r, name+": the same status and bytes as every other refusal")
	}
	assert.Len(t, refusals, 7)
	assert.Equal(t, before, e.storedCount(), "nothing refused was stored")
}

func TestAStaleOrMissingSnapshotAdmitsNoFile(t *testing.T) {
	e := newFileDoorEnv(t)
	code, _ := e.file("lab2", srcTeam1, e.target, true)
	assert.Equal(t, 403, code, "advertised, no snapshot yet in this process")
	e.snapshotOK(1, e.link("lab2", srcTeam1, e.target))
	code, _ = e.file("lab2", srcTeam1, e.target, true)
	assert.Equal(t, 200, code)
	e.advance(linkTTL + time.Second)
	code, _ = e.file("lab2", srcTeam1, e.target, true)
	assert.Equal(t, 403, code, "a snapshot older than the TTL admits nothing")
}

func TestADowngradedHubIsRefusedAFileNotSentBackToTheOldRule(t *testing.T) {
	e := newFileDoorEnv(t)
	e.servers(e.target, "lab2")
	e.snapshotOK(1, e.link("lab2", srcTeam1, e.target))
	code, _ := e.file("lab2", srcTeam1, e.target, false)
	assert.Equal(t, 403, code, "links advertised in this process, not on this connection: refused, though linked and on the server list")
	restarted := e.handler(e.bs)
	restarted.coralDir = e.dir
	e.wire(restarted)
	e.r.Post("/api/board/{project}/external-artifacts", restarted.PostExternalArtifact)
	code, _ = e.file("lab2", srcTeam1, e.target, false)
	assert.Equal(t, 403, code, "a restart does not forget that the hub advertised them")
}

// The refuse mode of the file door by itself (#5865 review): the three checks of PostExternalArtifact (before the body, after the metadata, before the store) all ask receivingOn, so it must say no
// for a connection without the capability once this computer has seen a links hub, with the board's switches on, the source on its server list and a fresh link: the old rule would say yes.
func TestReceivingOnIsFalseOnAConnectionThatLostTheLinksCapability(t *testing.T) {
	e := newFileDoorEnv(t)
	e.servers(e.target, "lab2")
	e.snapshotOK(1, e.link("lab2", srcTeam1, e.target))
	req := func(cap bool) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/api/board/"+e.target+"/external-artifacts", nil)
		r.Header.Set(tunnel.HubServerHeader, "lab2")
		r.Header.Set(tunnel.ViaHeader, cloud.ViaSecret())
		r.Header.Set(tunnel.HubSourceTeamHeader, srcTeam1)
		if cap {
			r.Header.Set(tunnel.HubLinksCapHeader, "1")
		}
		return r
	}
	ctx := context.Background()
	on, err := e.h.receivingOn(ctx, req(true), e.target, "lab2", "", false)
	require.NoError(t, err)
	require.True(t, on, "the same board, server and link are admitted on a links connection")
	for _, known := range []bool{false, true} {
		on, err = e.h.receivingOn(ctx, req(false), e.target, "lab2", "", known)
		require.NoError(t, err)
		assert.False(t, on, "no capability after links were seen: refused, not the old server-list rule")
	}
}

func TestAHubThatNeverAdvertisedLinksKeepsTheOldFileRule(t *testing.T) {
	e := newFileDoorEnv(t)
	e.servers(e.target, "lab2")
	code, body := e.file("lab2", srcTeam1, e.target, false)
	assert.Equal(t, 200, code, "the server list, the messaging switch and the files switch admit: "+body)
	code, _ = e.file("lab3", srcTeam1, e.target, false)
	assert.Equal(t, 403, code, "a server that is not listed is refused")
	e.filesOn(e.target, false)
	code, _ = e.file("lab2", srcTeam1, e.target, false)
	assert.Equal(t, 403, code, "and the files switch still decides")
	assert.False(t, e.seen)
}

// The panel and the door read one table: a team the Messaging tab shows as linked can send a file; one it does not show cannot.
func TestATeamTheMessagingTabShowsAsLinkedCanSendAFile(t *testing.T) {
	e := newFileDoorEnv(t)
	e.snapshotOK(1, e.link("lab2", srcTeam1, e.target), e.link("lab2", srcTeam2, e.target2))
	for target, linkedTeam := range map[string]string{e.target: srcTeam1, e.target2: srcTeam2} {
		req := httptest.NewRequest(http.MethodGet, "/api/board/"+target+"/cross-team-state", nil)
		rec := httptest.NewRecorder()
		e.r.ServeHTTP(rec, req)
		require.Equal(t, 200, rec.Code, rec.Body.String())
		var out struct {
			Links []map[string]any `json:"links"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		require.Len(t, out.Links, 1, target)
		assert.Equal(t, linkedTeam, out.Links[0]["team_id"])
		code, body := e.file(out.Links[0]["server_id"].(string), out.Links[0]["team_id"].(string), target, true)
		assert.Equal(t, 200, code, "shown as linked: admitted: "+body)
	}
	code, _ := e.file("lab2", srcTeam2, e.target, true)
	assert.Equal(t, 403, code, "a team the tab does not show for this board is refused")
}
