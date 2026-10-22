package routes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/pkg/tunnel"
)

// Pasted and attached images are artifacts (#5284): the upload route stores a copy, the agent's file and the artifact are the same stripped bytes, an SVG is a path only, and what is
// shared with Coral Hub is the owner's to switch off.

// jpegWith builds a small JPEG whose EXIF carries an Orientation and a secret that stands for GPS, camera and thumbnail data.
func jpegWith(orientation uint16, secret string) []byte {
	exif := []byte("Exif\x00\x00MM\x00\x2A\x00\x00\x00\x08")
	exif = append(exif, 0x00, 0x02)                                                                                          // two entries
	exif = append(exif, 0x01, 0x12, 0x00, 0x03, 0x00, 0x00, 0x00, 0x01, byte(orientation>>8), byte(orientation), 0x00, 0x00) // Orientation
	exif = append(exif, 0x88, 0x25, 0x00, 0x04, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x40)                              // a GPS pointer
	exif = append(exif, 0x00, 0x00, 0x00, 0x00)
	exif = append(exif, secret...)
	var b bytes.Buffer
	b.Write([]byte{0xFF, 0xD8})
	seg := func(marker byte, payload []byte) {
		b.Write([]byte{0xFF, marker, byte((len(payload) + 2) >> 8), byte(len(payload) + 2)})
		b.Write(payload)
	}
	seg(0xE1, exif)
	seg(0xFE, []byte("a comment with a path /home/someone/x")) // COM
	seg(0xC0, []byte{8, 0, 1, 0, 1, 1, 1, 0x11, 0})
	seg(0xDA, []byte{1, 1, 0, 0, 0x3F, 0})
	b.Write([]byte{0x12, 0x34, 0xFF, 0xD9})
	return b.Bytes()
}

func webpWithEXIF() []byte {
	body := append([]byte("WEBP"), []byte("EXIF")...)
	body = binary.LittleEndian.AppendUint32(body, 4)
	body = append(body, 1, 2, 3, 4)
	out := append([]byte("RIFF"), binary.LittleEndian.AppendUint32(nil, uint32(len(body)))...)
	return append(out, body...)
}

func cleanPNG(t *testing.T) []byte {
	var b bytes.Buffer
	require.NoError(t, png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 2))))
	return b.Bytes()
}

// uploadRig points the package-level upload handler at the rig's data folder and sessions, and puts everything back.
func uploadRig(t *testing.T) *artifactRig {
	t.Helper()
	rig := newArtifactRig(t)
	oldDir, oldCoral, oldCheck := uploadDir, uploadCoralDir, uploadSessionLive
	t.Cleanup(func() { uploadDir, uploadCoralDir, uploadSessionLive = oldDir, oldCoral, oldCheck })
	InitUploadDir(rig.coralDir)
	SetUploadSessionCheck(func(ctx context.Context, sid string) bool {
		ls, err := rig.ss.GetLiveSession(ctx, sid)
		return err == nil && ls != nil
	})
	return rig
}

func postImage(t *testing.T, name string, data []byte, session string) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", name)
	require.NoError(t, err)
	_, err = fw.Write(data)
	require.NoError(t, err)
	if session != "" {
		require.NoError(t, mw.WriteField("session_id", session))
	}
	require.NoError(t, mw.Close())
	req := httptest.NewRequest(http.MethodPost, "/api/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	UploadFile(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func artifactOf(out map[string]any) map[string]any {
	a, _ := out["artifact"].(map[string]any)
	return a
}

// The agent's file and the artifact are the same bytes, stripped of GPS and camera data, with the Orientation kept; the artifact's id is the file's SHA-256; the record says who and how.
func TestUploadStoresAStrippedArtifactTheAgentsFileMatches(t *testing.T) {
	rig := uploadRig(t)
	orig := jpegWith(6, "GPS-SECRET-LAT-LON Make-Phone")
	code, out := postImage(t, "photo.jpg", orig, "s1")
	require.Equal(t, http.StatusOK, code, out)
	art := artifactOf(out)
	require.NotNil(t, art, "an artifact: %v", out)
	file, err := os.ReadFile(out["path"].(string))
	require.NoError(t, err)
	sum := sha256.Sum256(file)
	assert.Equal(t, hex.EncodeToString(sum[:]), art["id"], "the artifact id is the SHA-256 of the file the agent holds")
	assert.Equal(t, "coral://artifacts/"+art["id"].(string), art["uri"])
	assert.Equal(t, "image/jpeg", art["media_type"])
	assert.Equal(t, true, art["inline"])
	assert.NotContains(t, string(file), "GPS-SECRET", "the agent's file has no GPS or camera data")
	assert.NotContains(t, string(file), "/home/someone", "nor the comment")
	assert.NotEqual(t, orig, file)
	i := bytes.Index(file, []byte("Exif\x00\x00"))
	require.GreaterOrEqual(t, i, 0, "an EXIF segment with the orientation stays")
	assert.Equal(t, 6, jpegExifOrientation(file[i:i+30]), "a portrait photo is not on its side")
	stored, err := os.ReadFile(filepath.Join(rig.coralDir, "artifacts", "objects", art["id"].(string)))
	require.NoError(t, err)
	assert.Equal(t, file, stored)
	rec, ok := readUploadRecord(rig.coralDir, "s1", art["id"].(string))
	require.True(t, ok)
	assert.Equal(t, "owner", rec.UploadedBy)
	assert.Equal(t, sourcePaste, rec.Source)
}

// The Orientation tag is the only EXIF the stripper keeps, and an upright photo keeps none.
func TestJPEGStripKeepsOnlyTheOrientation(t *testing.T) {
	out, err := stripJPEG(jpegWith(8, "SECRET-GPS"))
	require.NoError(t, err)
	assert.NotContains(t, string(out), "SECRET-GPS")
	assert.Equal(t, 1, bytes.Count(out, []byte("Exif\x00\x00")))
	i := bytes.Index(out, []byte("Exif\x00\x00"))
	assert.Equal(t, 8, jpegExifOrientation(out[i:i+30]))
	upright, err := stripJPEG(jpegWith(1, "SECRET-GPS"))
	require.NoError(t, err)
	assert.NotContains(t, string(upright), "Exif", "an upright photo keeps no EXIF at all")
	assert.Equal(t, 0, jpegExifOrientation([]byte("not exif")))
}

// A PNG has no metadata to speak of: the artifact is the file, and a BMP is an artifact that is a chip, not a thumbnail.
func TestUploadPNGAndBMPAreArtifacts(t *testing.T) {
	uploadRig(t)
	pngBytes := cleanPNG(t)
	_, out := postImage(t, "shot.png", pngBytes, "s1")
	art := artifactOf(out)
	require.NotNil(t, art, out)
	assert.Equal(t, true, art["inline"])
	sum := sha256.Sum256(pngBytes)
	assert.Equal(t, hex.EncodeToString(sum[:]), art["id"])
	bmp := append([]byte("BM"), bytes.Repeat([]byte{0}, 40)...)
	_, out = postImage(t, "pic.bmp", bmp, "s1")
	art = artifactOf(out)
	require.NotNil(t, art, out)
	assert.Equal(t, "image/bmp", art["media_type"])
	assert.Equal(t, false, art["inline"], "a BMP is a chip with a download, not a thumbnail")
}

// An image the store cannot strip safely (a WebP with EXIF) gets no artifact, and the upload still gives the agent its path.
func TestUploadWebPWithMetadataHasNoArtifactButSavesTheFile(t *testing.T) {
	rig := uploadRig(t)
	code, out := postImage(t, "w.webp", webpWithEXIF(), "s1")
	require.Equal(t, http.StatusOK, code)
	assert.Nil(t, artifactOf(out))
	assert.Contains(t, out["artifact_error"], "camera, location")
	_, err := os.Stat(out["path"].(string))
	assert.NoError(t, err, "the agent's file is saved")
	assert.NoDirExists(t, filepath.Join(rig.coralDir, "artifacts", "uploads", "s1"), "no record")
}

// SVG is a path for the agent only: never an artifact (a byte-exact copy cannot be stripped and can carry script), and never served by GET /api/upload/{name}.
func TestUploadSVGIsAPathOnly(t *testing.T) {
	rig := uploadRig(t)
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	code, out := postImage(t, "x.svg", svg, "s1")
	require.Equal(t, http.StatusOK, code, out)
	assert.Nil(t, artifactOf(out))
	assert.Contains(t, out["artifact_error"], "SVG")
	file, err := os.ReadFile(out["path"].(string))
	require.NoError(t, err)
	assert.Equal(t, svg, file)
	entries, _ := os.ReadDir(filepath.Join(rig.coralDir, "artifacts", "objects"))
	assert.Empty(t, entries, "nothing in the artifact store")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/upload/"+filepath.Base(out["path"].(string)), nil)
	GetUpload(rec, withURLParam(req, "name", filepath.Base(out["path"].(string))))
	assert.Equal(t, http.StatusUnsupportedMediaType, rec.Code, "the upload route never serves it")
}

// Not an image, no session, an unknown session: the file is kept for the agent, with the reason.
func TestUploadWithoutAnArtifactSaysWhy(t *testing.T) {
	rig := uploadRig(t)
	code, out := postImage(t, "fake.png", []byte("not an image at all"), "s1")
	require.Equal(t, http.StatusOK, code)
	assert.Nil(t, artifactOf(out))
	assert.Contains(t, out["artifact_error"], "not a picture")
	_, out = postImage(t, "a.png", cleanPNG(t), "")
	assert.Nil(t, artifactOf(out))
	assert.Contains(t, out["artifact_error"], "not known")
	_, out = postImage(t, "b.png", cleanPNG(t), "no-such-session")
	assert.Nil(t, artifactOf(out))
	assert.Contains(t, out["artifact_error"], "not running here")
	assert.NoDirExists(t, filepath.Join(rig.coralDir, "artifacts", "uploads", "no-such-session"), "a stream of uploads cannot create folders for sessions that do not exist")
	_, out = postImage(t, "c.png", cleanPNG(t), "../../etc")
	assert.Nil(t, artifactOf(out), "an id that is not a safe name names nothing")
}

// The dashboard's hourly window is its own: used up, the artifact step stops (the upload does not), and an agent's own window is untouched; no field of the request names the bucket.
func TestDashboardBucketIsSeparateFromAnAgents(t *testing.T) {
	rig := uploadRig(t)
	st := storeFor(rig.coralDir)
	for i := 0; i < uploadWindowCount; i++ {
		st.admitUpload(dashboardUploadBucket)
	}
	code, out := postImage(t, "d.png", cleanPNG(t), "s1")
	require.Equal(t, http.StatusOK, code)
	assert.Nil(t, artifactOf(out))
	assert.Contains(t, out["artifact_error"], "hourly limit")
	ok, _ := st.admitUpload("session:s1")
	assert.True(t, ok, "an agent's own allowance is not spent by the dashboard")
	assert.NotContains(t, dashboardUploadBucket, "s1")
}

// An agent token is refused for the routes that make and remove shared images: they are the Operator's.
func TestPastedImageRoutesAreOperatorOnly(t *testing.T) {
	for _, key := range []string{"POST /api/upload", "DELETE /api/shared-images/{id}"} {
		_, ok := operatorOnlyRoutes[key]
		assert.True(t, ok, key+" is in the operator-only table")
	}
}

func getArtifact(t *testing.T, rig *artifactRig, id string, tunneled bool) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, rig.srv.URL+"/api/artifacts/"+id+"?v=2", nil)
	require.NoError(t, err)
	req.Header.Set(testDashboardHeader, "1")
	if tunneled {
		setHubOwner(t, req)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	return resp.StatusCode
}

// Removing one session's shared copy never takes another's: two agents given the same bytes share one object, which goes with the last record.
func TestRemovingASharedCopyKeepsTheObjectForTheOtherAgent(t *testing.T) {
	rig := uploadRig(t)
	data := cleanPNG(t)
	_, o1 := postImage(t, "a.png", data, "s1")
	_, o2 := postImage(t, "b.png", data, "s2")
	id := artifactOf(o1)["id"].(string)
	require.Equal(t, id, artifactOf(o2)["id"], "the same bytes are one object")
	del := func(sid string, hdr map[string]string) int {
		req, err := http.NewRequest(http.MethodDelete, rig.srv.URL+"/api/shared-images/"+id+"?session_id="+sid, nil)
		require.NoError(t, err)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		return resp.StatusCode
	}
	dash := map[string]string{testDashboardHeader: "1"}
	assert.Equal(t, http.StatusForbidden, del("s1", map[string]string{testAgentHeader: "s1"}), "an agent cannot")
	assert.Equal(t, http.StatusForbidden, del("s1", map[string]string{testDashboardHeader: "1", tunnel.ViaHeader: "x"}), "nor the Hub")
	require.Equal(t, http.StatusOK, del("s1", dash))
	assert.Equal(t, http.StatusOK, getArtifact(t, rig, id, false), "s2's copy is still there")
	_, ok := readUploadRecord(rig.coralDir, "s1", id)
	assert.False(t, ok)
	assert.Equal(t, http.StatusNotFound, del("s1", dash), "removing it twice finds nothing")
	require.Equal(t, http.StatusOK, del("s2", dash))
	assert.Equal(t, http.StatusNotFound, getArtifact(t, rig, id, false), "the last record takes the object")
}

// An artifact an agent uploaded itself is not a pasted image: the removal refuses it.
func TestRemovingASharedCopyRefusesAnAgentsOwnUpload(t *testing.T) {
	rig := uploadRig(t)
	id, _, err := storeArtifact(rig.coralDir, "s1", "mine.png", "image/png", cleanPNG(t))
	require.NoError(t, err)
	removed, _ := rig.h.removePastedCopy(context.Background(), "s1", id)
	assert.False(t, removed)
	assert.Equal(t, http.StatusOK, getArtifact(t, rig, id, false))
}

// The uploads retention removes the paste records older than its cutoff, and each object last; a newer one and an agent's own upload stay.
func TestPurgePastedImagesFollowsTheCutoff(t *testing.T) {
	rig := uploadRig(t)
	_, o1 := postImage(t, "old.png", cleanPNG(t), "s1")
	id := artifactOf(o1)["id"].(string)
	// the record's time is in the past
	rec, ok := readUploadRecord(rig.coralDir, "s1", id)
	require.True(t, ok)
	rec.CreatedAt = time.Now().Add(-40 * 24 * time.Hour).UTC().Format(time.RFC3339)
	b, _ := json.Marshal(rec)
	require.NoError(t, os.WriteFile(filepath.Join(uploadsDir(rig.coralDir, "s1"), id+".json"), b, 0o600))
	fresh := image.NewRGBA(image.Rect(0, 0, 3, 3))
	var fb bytes.Buffer
	require.NoError(t, png.Encode(&fb, fresh))
	_, o2 := postImage(t, "new.png", fb.Bytes(), "s1")
	freshID := artifactOf(o2)["id"].(string)
	ownID, _, err := storeArtifact(rig.coralDir, "s1", "own.txt", "text/plain", []byte("an agent's own file"))
	require.NoError(t, err)
	records, blobs := rig.h.PurgePastedImages(context.Background(), time.Now().Add(-30*24*time.Hour))
	assert.Equal(t, 1, records)
	assert.Equal(t, 1, blobs)
	assert.Equal(t, http.StatusNotFound, getArtifact(t, rig, id, false), "the old one is gone")
	assert.Equal(t, http.StatusOK, getArtifact(t, rig, freshID, false), "the new one stays")
	assert.Equal(t, http.StatusOK, getArtifact(t, rig, ownID, false), "an agent's own upload is never touched")
}

// A task result and a cross-team file cannot name a pasted image: it is the Operator's, not the agent's upload, so the retention can never leave one dangling.
func TestAPastedImageIsNotAnAgentsUploadForATaskResult(t *testing.T) {
	h, coralDir, _ := vetEnv(t)
	d := writeUpload(t, coralDir, vetSessionX, "an image's bytes", "p.md", afterJoin())
	_, ok := h.uploadRecordOf(vetSessionX, d)
	require.True(t, ok, "an agent's own record counts")
	rec, _ := readUploadRecord(coralDir, vetSessionX, d)
	rec.Source = sourcePaste
	rec.UploadedBy = uploadedByOwner
	b, _ := json.Marshal(rec)
	require.NoError(t, os.WriteFile(filepath.Join(uploadsDir(coralDir, vetSessionX), d+".json"), b, 0o600))
	h.uploads = uploadDirCache{}
	_, ok = h.uploadRecordOf(vetSessionX, d)
	assert.False(t, ok, "a pasted image's record does not count")
}

func TestUploadRecordsOfAgentsHaveNoPasteFields(t *testing.T) {
	rig := uploadRig(t)
	id, _, err := storeArtifact(rig.coralDir, "s1", "a.txt", "text/plain", []byte("x"))
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join(uploadsDir(rig.coralDir, "s1"), id+".json"))
	require.NoError(t, err)
	assert.False(t, strings.Contains(string(data), "uploaded_by") || strings.Contains(string(data), "source"), string(data))
	var rec agentUploadRecord
	require.NoError(t, json.Unmarshal(data, &rec))
	assert.Empty(t, rec.Source)
	_ = store.LiveSession{}
}

// The answer says whether the object went too: removing one agent's shared copy of bytes another agent was also given leaves the object ("artifact_removed": false), and removing the last record takes it (true). A
// suite that pastes the same few images into one server over and over (pasted_image_artifact_browser did) sees false when another session's leftover record holds the same bytes.
func TestRemovingASharedCopyAnswersWhetherTheObjectWentToo(t *testing.T) {
	rig := uploadRig(t)
	data := cleanPNG(t)
	_, o1 := postImage(t, "a.png", data, "s1")
	_, o2 := postImage(t, "b.png", data, "s2")
	id := artifactOf(o1)["id"].(string)
	require.Equal(t, id, artifactOf(o2)["id"])
	del := func(sid string) (int, map[string]any) {
		req, err := http.NewRequest(http.MethodDelete, rig.srv.URL+"/api/shared-images/"+id+"?session_id="+sid, nil)
		require.NoError(t, err)
		req.Header.Set(testDashboardHeader, "1")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	code, out := del("s1")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, map[string]any{"removed": true, "artifact_removed": false}, out, "s2 still holds the same bytes: the object stays")
	assert.Equal(t, http.StatusOK, getArtifact(t, rig, id, false))
	code, out = del("s2")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, map[string]any{"removed": true, "artifact_removed": true}, out, "the last record takes the object")
	assert.Equal(t, http.StatusNotFound, getArtifact(t, rig, id, false))
}
