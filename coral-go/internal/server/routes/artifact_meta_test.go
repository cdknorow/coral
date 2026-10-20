package routes

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/pkg/tunnel"
)

// GET /api/artifacts/{id}/meta (#5465): the facts of an artifact without its bytes.

func getArtifactMeta(t *testing.T, rig *artifactRig, id string, tunneled bool) (int, http.Header, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, rig.srv.URL+"/api/artifacts/"+id+"/meta", nil)
	require.NoError(t, err)
	req.Header.Set(testDashboardHeader, "1")
	if tunneled {
		req.Header.Set(tunnel.ViaHeader, "x")
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, resp.Header, out
}

func TestArtifactMetaAnswersWithTheFactsOnly(t *testing.T) {
	rig := uploadRig(t)
	const content = "hello, stored bytes"
	code, up := rig.upload("s1", "note.txt", "text/plain", strings.NewReader(content))
	require.Equal(t, http.StatusCreated, code, up)
	id := strings.TrimPrefix(up["uri"].(string), "coral://artifacts/")

	for _, tunneled := range []bool{false, true} {
		code, h, out := getArtifactMeta(t, rig, id, tunneled)
		require.Equal(t, http.StatusOK, code)
		assert.Equal(t, map[string]any{"id": id, "size": float64(len(content)), "media_type": "text/plain", "name": "note.txt"}, out)
		assert.Equal(t, "no-store", h.Get("Cache-Control"))
		assert.Empty(t, h.Get("X-Coral-Build"))
	}
}

func TestArtifactMetaOmitsAnEmptyNameAndNamesAnUnparseableTypeOctetStream(t *testing.T) {
	rig := uploadRig(t)
	_, meta, err := storeArtifact(rig.coralDir, "s1", "", "///", []byte("abc"))
	require.NoError(t, err)
	id := strings.TrimPrefix(meta.Digest, "sha256:")
	code, _, out := getArtifactMeta(t, rig, id, false)
	require.Equal(t, http.StatusOK, code)
	_, hasName := out["name"]
	assert.False(t, hasName, "no name: the key is absent")
	assert.Equal(t, "application/octet-stream", out["media_type"])
}

func TestArtifactMetaIs404ForAnAbsentArtifact(t *testing.T) {
	rig := uploadRig(t)
	code, _, _ := getArtifactMeta(t, rig, strings.Repeat("a", 64), false)
	assert.Equal(t, http.StatusNotFound, code)
}

// The owner's switch hides a pasted image from the Hub for the meta read exactly as for the content read.
func TestArtifactMetaFollowsThePasteSwitch(t *testing.T) {
	rig := uploadRig(t)
	_, out := postImage(t, "p.png", cleanPNG(t), "s1")
	id := artifactOf(out)["id"].(string)
	code, _, _ := getArtifactMeta(t, rig, id, true)
	require.Equal(t, http.StatusOK, code, "on by default")
	require.NoError(t, rig.ss.SetSetting(context.Background(), SharePastedImagesSetting, "false"))
	code, _, _ = getArtifactMeta(t, rig, id, true)
	assert.Equal(t, http.StatusNotFound, code, "off: a tunneled meta read is 404, as the content read is")
	assert.Equal(t, http.StatusNotFound, getArtifact(t, rig, id, true))
	code, _, _ = getArtifactMeta(t, rig, id, false)
	assert.Equal(t, http.StatusOK, code, "off: this computer still sees it")
}

// An id is exactly 64 lower-case hex digits: anything else is 400 on both reads.
func TestArtifactIdIsExactly64LowerCaseHex(t *testing.T) {
	rig := uploadRig(t)
	_, meta, err := storeArtifact(rig.coralDir, "s1", "a.txt", "text/plain", []byte("abc"))
	require.NoError(t, err)
	id := strings.TrimPrefix(meta.Digest, "sha256:")
	assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]{64}$`), id)

	_, up := postImage(t, "p.png", cleanPNG(t), "s1")
	assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]{64}$`), artifactOf(up)["id"])

	for _, bad := range []string{strings.ToUpper(id), id[:63], id + "0", "sha256:" + id, strings.Repeat("g", 64), "x"} {
		code, _, _ := getArtifactMeta(t, rig, bad, false)
		assert.Equal(t, http.StatusBadRequest, code, "meta "+bad)
		assert.Equal(t, http.StatusBadRequest, getArtifact(t, rig, bad, false), "content "+bad)
	}
}

func TestArtifactMetaRefusesADamagedCopy(t *testing.T) {
	rig := uploadRig(t)
	_, meta, err := storeArtifact(rig.coralDir, "s1", "a.txt", "text/plain", []byte("abcdef"))
	require.NoError(t, err)
	id := strings.TrimPrefix(meta.Digest, "sha256:")
	require.NoError(t, os.WriteFile(filepath.Join(rig.coralDir, "artifacts", "objects", id), []byte("abc"), 0o600))
	code, _, out := getArtifactMeta(t, rig, id, false)
	assert.Equal(t, http.StatusInternalServerError, code)
	assert.Equal(t, "artifact_corrupt", out["code"])
}
