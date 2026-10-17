package routes

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4443 (Security's #4438 INFO 4): a download's length comes from the stored file, and a sidecar that disagrees with
// the file (a damaged store) is refused with artifact_corrupt instead of being sent with a length that doesn't match.
func TestArtifactDownloadLengthIsTheFiles(t *testing.T) {
	iso := t.TempDir()
	for _, k := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(k, iso)
	}
	t.Setenv("HOME", filepath.Join(iso, "home"))
	t.Setenv("USERPROFILE", filepath.Join(iso, "home"))
	t.Setenv("CORAL_DATA_DIR", filepath.Join(iso, "data"))
	rig := newArtifactRig(t)
	const content = "hello, stored bytes"
	code, out := rig.upload("s1", "note.txt", "text/plain", strings.NewReader(content))
	require.Equal(t, http.StatusCreated, code, out)
	id := strings.TrimPrefix(out["uri"].(string), "coral://artifacts/")

	// A normal download, whole and by range, is as before.
	code, h, body := rig.get(id)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, content, string(body))
	assert.Equal(t, "19", h.Get("Content-Length"))
	req, _ := http.NewRequest(http.MethodGet, rig.srv.URL+"/api/artifacts/"+id, nil)
	req.Header.Set("Range", "bytes=0-4")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	part, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, http.StatusPartialContent, resp.StatusCode)
	assert.Equal(t, "hello", string(part))
	assert.Equal(t, "5", resp.Header.Get("Content-Length"))

	object := filepath.Join(rig.coralDir, "artifacts", "objects", id)
	refused := func(why string) {
		t.Helper()
		code, h, body := rig.get(id)
		assert.Equal(t, http.StatusInternalServerError, code, why)
		var e map[string]string
		require.NoError(t, json.Unmarshal(body, &e), why)
		assert.Equal(t, "artifact_corrupt", e["code"], why)
		assert.NotContains(t, string(body), content, "%s: none of the bytes are sent", why)
		assert.NotEqual(t, "19", h.Get("Content-Length"), why)
	}

	// The sidecar claims another size.
	sidecar, err := os.ReadFile(object + ".json")
	require.NoError(t, err)
	var meta map[string]any
	require.NoError(t, json.Unmarshal(sidecar, &meta))
	meta["size"] = 999
	bad, _ := json.Marshal(meta)
	require.NoError(t, os.WriteFile(object+".json", bad, 0o600))
	refused("a sidecar larger than the file")

	// The file was cut short under a correct sidecar.
	require.NoError(t, os.WriteFile(object+".json", sidecar, 0o600))
	code, _, _ = rig.get(id)
	require.Equal(t, http.StatusOK, code, "the sidecar put back: served again")
	require.NoError(t, os.WriteFile(object, []byte("hello"), 0o600))
	refused("a file shorter than its sidecar")
}
