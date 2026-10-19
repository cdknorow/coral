package routes

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #5021: POST /api/upload refuses a file over maxFileSize and stores nothing (the branch after the read, which no test reached).
// Mutation: drop the length check after the read: the oversized file is written to the upload folder.
func TestUploadOverTheSizeLimitIsRefusedAndNotStored(t *testing.T) {
	oldDir, oldMax := uploadDir, maxFileSize
	t.Cleanup(func() { uploadDir, maxFileSize = oldDir, oldMax })
	InitUploadDir(t.TempDir())
	maxFileSize = 64
	post := func(size int) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, err := mw.CreateFormFile("file", "pic.png")
		require.NoError(t, err)
		_, err = fw.Write(bytes.Repeat([]byte{'x'}, size))
		require.NoError(t, err)
		require.NoError(t, mw.Close())
		req := httptest.NewRequest(http.MethodPost, "/api/upload", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		rec := httptest.NewRecorder()
		UploadFile(rec, req)
		return rec
	}
	count := func() int {
		entries, _ := os.ReadDir(uploadDir)
		n := 0
		for _, e := range entries {
			if !e.IsDir() && filepath.Ext(e.Name()) == ".png" {
				n++
			}
		}
		return n
	}
	rec := post(65)
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "File too large")
	assert.Zero(t, count(), "nothing was stored")
	rec = post(64)
	assert.Equal(t, http.StatusOK, rec.Code, "a file at the limit is accepted: "+rec.Body.String())
	assert.Equal(t, 1, count())
}
