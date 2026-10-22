package routes

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/pkg/tunnel"
)

// The Add image button (#6088) sends the image through Coral Hub: the upload record and the log line say which hub account sent it, the account's hourly share is its own (half of the
// owner's), a HEIC is refused in words a person can act on, and the retry time is in the reply.

// postImageVia is postImage as the connector executes it for Coral Hub: stamped with the via header, and with the hub account when the hub sent one.
func postImageVia(t *testing.T, name string, data []byte, session, hubUser string) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", name)
	require.NoError(t, err)
	_, err = fw.Write(data)
	require.NoError(t, err)
	require.NoError(t, mw.WriteField("session_id", session))
	require.NoError(t, mw.Close())
	req := httptest.NewRequest(http.MethodPost, "/api/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set(tunnel.ViaHeader, "1")
	if hubUser != "" {
		req.Header.Set(tunnel.HubUserHeader, hubUser)
	}
	rec := httptest.NewRecorder()
	UploadFile(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func uploadRecordOf(t *testing.T, rig *artifactRig, session string, art map[string]any) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(rig.coralDir, "artifacts", "uploads", session, art["id"].(string)+".json"))
	require.NoError(t, err)
	var rec map[string]any
	require.NoError(t, json.Unmarshal(raw, &rec))
	return rec
}

// An image sent through Coral Hub keeps the hub account on its record (uploaded_by stays "owner"); one from this computer's own dashboard has none; a malformed account is not kept.
func TestAnImageThroughTheHubRecordsTheHubAccount(t *testing.T) {
	rig := uploadRig(t)
	code, out := postImageVia(t, "a.png", cleanPNG(t), "s1", "hub-user-0a1b2c3d")
	require.Equal(t, http.StatusOK, code, out)
	rec := uploadRecordOf(t, rig, "s1", artifactOf(out))
	assert.Equal(t, "hub-user-0a1b2c3d", rec["hub_user"])
	assert.Equal(t, "owner", rec["uploaded_by"])

	_, out = postImage(t, "b.png", append(cleanPNG(t), 0), "s1") // another picture (other bytes): the dashboard's own
	rec = uploadRecordOf(t, rig, "s1", artifactOf(out))
	assert.NotContains(t, rec, "hub_user")

	for _, bad := range []string{"alice@example.com", "Alice", "hub-user-XYZ", "hub-user-0a1b2c3d0", strings.Repeat("x", 80)} {
		png := append(cleanPNG(t), byte(len(bad)))
		_, out = postImageVia(t, "c.png", png, "s1", bad)
		require.NotNil(t, artifactOf(out), out)
		assert.NotContains(t, uploadRecordOf(t, rig, "s1", artifactOf(out)), "hub_user", "%q is not a hub account and is not recorded", bad)
	}
}

// A hub account has half the owner's hourly allowance, in its own window: using it up neither stops the owner's own dashboard nor another hub account, and the reply says when to try again.
func TestAHubAccountsHourlyShareIsItsOwn(t *testing.T) {
	rig := uploadRig(t)
	st := storeFor(rig.coralDir)
	a, b := "hub-user-aaaaaaaa", "hub-user-bbbbbbbb"
	for i := 0; i < uploadWindowCount/2; i++ {
		ok, _ := st.admitUpload(dashboardHubBucket + a)
		require.True(t, ok, "upload %d is inside the half", i)
	}
	ok, _ := st.admitUpload(dashboardHubBucket + a)
	assert.False(t, ok, "the 51st is refused: half of 100")

	code, out := postImageVia(t, "d.png", cleanPNG(t), "s1", a)
	require.Equal(t, http.StatusOK, code, "the upload itself still succeeds")
	assert.Nil(t, artifactOf(out))
	assert.Contains(t, out["artifact_error"], "hourly limit")
	assert.Regexp(t, `^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$`, out["artifact_retry_at"], "the retry time is a timestamp the dashboard shows in the viewer's own time")

	_, out = postImageVia(t, "e.png", cleanPNG(t), "s1", b)
	assert.NotNil(t, artifactOf(out), "another hub account is not stopped: %v", out)
	_, out = postImage(t, "f.png", cleanPNG(t), "s1")
	assert.NotNil(t, artifactOf(out), "the owner's own dashboard is not stopped: %v", out)
	_, out = postImageVia(t, "g.png", cleanPNG(t), "s1", "")
	assert.NotNil(t, artifactOf(out), "a hub request with no account has the shared hub window, which is not used up: %v", out)
}

// The hub half is half of the bytes too.
func TestAHubWindowIsHalfTheBytes(t *testing.T) {
	count, bytes := windowLimits(dashboardHubBucket + "hub-user-aaaaaaaa")
	ownerCount, ownerBytes := windowLimits(dashboardUploadBucket)
	assert.Equal(t, ownerCount/2, count)
	assert.Equal(t, ownerBytes/2, bytes)
}

// A HEIC (an iPhone's own format) is refused with a sentence that says what to do, by extension and by declared type; nothing is saved. (HEIC bytes under an allowed name stay as documented: kept for the
// agent, no artifact; strip_realworld_test.go pins that.)
func TestAHeicPhotoIsRefusedInWordsAPersonCanActOn(t *testing.T) {
	rig := uploadRig(t)
	heic := append([]byte{0, 0, 0, 0x18}, []byte("ftypheic\x00\x00\x00\x00mif1heic")...)
	for _, name := range []string{"IMG_0001.HEIC", "photo.heif", "photo.avif"} {
		code, out := postImage(t, name, heic, "s1")
		assert.Equal(t, http.StatusBadRequest, code, name)
		msg := strings.ToLower(heicReply(out))
		assert.Contains(t, msg, "heic", name)
		assert.Contains(t, msg, "jpeg", name)
		assert.NotContains(t, msg, ".tiff", "%s: not the list of extensions", name)
	}
	entries, _ := os.ReadDir(filepath.Join(rig.coralDir, "uploads"))
	assert.Empty(t, entries, "nothing was saved")
}

func heicReply(m map[string]any) string {
	b, _ := json.Marshal(m)
	return string(b)
}
