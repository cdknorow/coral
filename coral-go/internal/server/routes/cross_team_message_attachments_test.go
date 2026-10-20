package routes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #5506: a message that arrived from another Coral lists, in its envelope (external.attachments), the files stored here for it, in the order the sender attached them: id, name, media
// type, size. Nothing when there are none. The dashboard reads it to show each file, and to say "not on this machine" for a link in the text that is not in the list.

type envelopeAttachment struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	Size      int64  `json:"size"`
}

// attachmentsOf is the message's envelope as the board serves it: the attachments (nil when the key is absent) and the whole envelope.
func (e *artifactEnv) attachmentsOf(project string, id float64) ([]envelopeAttachment, map[string]json.RawMessage) {
	e.t.Helper()
	m, err := e.h.bs.GetMessageByID(e.t.Context(), project, int64(id))
	require.NoError(e.t, err)
	require.NotNil(e.t, m)
	var env map[string]json.RawMessage
	require.NoError(e.t, json.Unmarshal(m.External, &env))
	var atts []envelopeAttachment
	if raw, ok := env["attachments"]; ok {
		require.NoError(e.t, json.Unmarshal(raw, &atts))
	}
	return atts, env
}

func TestInboundMessageListsItsFilesInTheOrderTheyWereAttached(t *testing.T) {
	e := newArtifactEnv(t)
	e.open()
	// The sender pushes the files in order; each arrival is a moment later (the real clock moves, the test's is made to).
	e.h.received.now = func() time.Time { e.now = e.now.Add(time.Second); return e.now }
	zz := []byte("# zz first\nbody zz\n") // digests sort in no particular order: the list must follow arrival, not the names
	aa := cleanPNG(t)
	mm := []byte("# mm third\nbody mm\n")
	for _, f := range []struct {
		data []byte
		name string
		mt   string
	}{{zz, "report.md", "text/markdown"}, {aa, "shot.png", "image/png"}, {mm, "notes.txt", "text/plain"}} {
		m := meta(f.data, f.name)
		m["media_type"] = f.mt
		status, out := e.send("auth", relaySrc, "", frame(m, f.data))
		require.Equal(t, 200, status, out)
	}
	code, msg := e.deliver(relaySrc, "m1")
	require.Equal(t, 200, code, msg)

	atts, env := e.attachmentsOf("beta", msg["target_message_id"].(float64))
	require.Len(t, atts, 3)
	ids := []string{strings.TrimPrefix(sha256Of(zz), "sha256:"), strings.TrimPrefix(sha256Of(aa), "sha256:"), strings.TrimPrefix(sha256Of(mm), "sha256:")}
	for i, a := range atts {
		assert.Equal(t, ids[i], a.ID, "attach order, entry %d", i)
		assert.Regexp(t, `^[0-9a-f]{64}$`, a.ID)
		assert.NotEmpty(t, a.Name)
		assert.NotEmpty(t, a.MediaType)
	}
	assert.EqualValues(t, len(zz), atts[0].Size)
	assert.EqualValues(t, len(aa), atts[1].Size)
	assert.True(t, strings.HasPrefix(atts[1].MediaType, "image/"), "an image says so: %q", atts[1].MediaType)
	var direction string
	require.NoError(t, json.Unmarshal(env["direction"], &direction))
	assert.Equal(t, "inbound", direction, "the rest of the envelope is as it was")
	assert.Contains(t, env, "relay_message_id")
}

// No files, no key. A file that came with another message is not listed on this one.
func TestInboundMessageWithoutFilesHasNoAttachmentsAndOthersFilesAreNotItsOwn(t *testing.T) {
	e := newArtifactEnv(t)
	e.open()
	data := []byte("# for m1 only\n")
	status, out := e.send("auth", relaySrc, "", frame(meta(data, "one.md"), data)) // meta says message m1
	require.Equal(t, 200, status, out)
	code, msg := e.deliver(relaySrc, "m2") // a different message: nothing was pushed for it
	require.Equal(t, 200, code, msg)
	atts, env := e.attachmentsOf("beta", msg["target_message_id"].(float64))
	assert.Empty(t, atts)
	assert.NotContains(t, env, "attachments", "the key is absent, not an empty list")
	code, msg = e.deliver(relaySrc, "m1")
	require.Equal(t, 200, code, msg)
	atts, _ = e.attachmentsOf("beta", msg["target_message_id"].(float64))
	require.Len(t, atts, 1)
	assert.Equal(t, strings.TrimPrefix(sha256Of(data), "sha256:"), atts[0].ID)
}

// The same bytes sent again with another message: the file is stored once (its Received row keeps the first message), yet the second message lists it too.
func TestInboundMessageListsAFileThatWasAlreadyStored(t *testing.T) {
	e := newArtifactEnv(t)
	e.open()
	data := []byte("# same bytes\nagain\n")
	status, out := e.send("auth", relaySrc, "", frame(meta(data, "plan.md"), data))
	require.Equal(t, 200, status, out)
	code, first := e.deliver(relaySrc, "m1")
	require.Equal(t, 200, code, first)
	m := meta(data, "other.md")
	m["message_id"] = "m2"
	status, out = e.send("auth", relaySrc, "", frame(m, data))
	require.Equal(t, 200, status, out)
	code, second := e.deliver(relaySrc, "m2")
	require.Equal(t, 200, code, second)

	id := strings.TrimPrefix(sha256Of(data), "sha256:")
	a1, _ := e.attachmentsOf("beta", first["target_message_id"].(float64))
	a2, _ := e.attachmentsOf("beta", second["target_message_id"].(float64))
	require.Len(t, a1, 1)
	require.Len(t, a2, 1, "the second message lists it too")
	assert.Equal(t, id, a1[0].ID)
	assert.Equal(t, id, a2[0].ID)
	assert.Len(t, e.listed(), 1, "one Received row, as before")
}

// A message delivered twice (a retry) keeps its list; the board serves it through the ordinary message routes.
func TestInboundMessageListSurvivesARetryAndIsServed(t *testing.T) {
	e := newArtifactEnv(t)
	e.open()
	e.r.Get("/api/board/{project}/messages/all", e.h.ListAllMessages)
	data := []byte("# retry\n")
	status, out := e.send("auth", relaySrc, "", frame(meta(data, "r.md"), data))
	require.Equal(t, 200, status, out)
	code, first := e.deliver(relaySrc, "m1")
	require.Equal(t, 200, code, first)
	code, again := e.deliver(relaySrc, "m1")
	require.Equal(t, 200, code, again)
	assert.Equal(t, true, again["duplicate"])
	atts, _ := e.attachmentsOf("beta", first["target_message_id"].(float64))
	require.Len(t, atts, 1, "the retry did not wipe the list")

	rec := httptest.NewRecorder()
	e.r.ServeHTTP(rec, httptest.NewRequest("GET", "/api/board/beta/messages/all?limit=10", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"attachments":[{"id":"`+strings.TrimPrefix(sha256Of(data), "sha256:")+`"`)
}

// A message between two boards on this Coral lists its attached files the same way.
func TestLocalCrossTeamMessageListsItsAttachedFiles(t *testing.T) {
	e := newAttachEnv(t, true)
	d := e.upload(alphaOrchSID, "alpha's report")
	code, out := e.send("alpha", "beta", alphaOrchSID, uri(d))
	require.Equal(t, http.StatusOK, code, out)
	msg := out["message"].(map[string]any)
	m, err := e.h.bs.GetMessageByID(t.Context(), "beta", int64(msg["id"].(float64)))
	require.NoError(t, err)
	var env struct {
		Attachments []envelopeAttachment `json:"attachments"`
	}
	require.NoError(t, json.Unmarshal(m.External, &env))
	require.Len(t, env.Attachments, 1)
	assert.Equal(t, d, env.Attachments[0].ID)
	assert.NotEmpty(t, env.Attachments[0].Name)
	assert.Positive(t, env.Attachments[0].Size)
}

// #5507 (Security): the name an agent gave its upload is free text. On the same-Coral path it reaches the receiving message's envelope (and the dashboard and the hub) unless it is cleaned.
func TestLocalCrossTeamMessageListsAnUploadNameCleaned(t *testing.T) {
	e := newAttachEnv(t, true)
	hostile := "re‮port\x1b[31m\n\x00" + strings.Repeat("x", 500) + ".md"
	d := writeUpload(t, e.coralDir, alphaOrchSID, "alpha's report", hostile, afterJoin())
	code, out := e.send("alpha", "beta", alphaOrchSID, uri(d))
	require.Equal(t, http.StatusOK, code, out)
	msg := out["message"].(map[string]any)
	m, err := e.h.bs.GetMessageByID(t.Context(), "beta", int64(msg["id"].(float64)))
	require.NoError(t, err)
	var env struct {
		Attachments []envelopeAttachment `json:"attachments"`
	}
	require.NoError(t, json.Unmarshal(m.External, &env))
	require.Len(t, env.Attachments, 1)
	name := env.Attachments[0].Name
	for _, r := range name {
		assert.False(t, unicode.IsControl(r) || unicode.Is(unicode.Cf, r), "no control or format character in the listed name: %q", name)
	}
	assert.LessOrEqual(t, len(name), 200, "the listed name is bounded")
}
