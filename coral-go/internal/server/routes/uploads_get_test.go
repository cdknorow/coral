package routes

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/linktest"
)

// A 1x1 PNG, a JPEG, a GIF and a WebP header: what the sniffer must recognise by content.
var (
	testPNG  = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89")
	testJPEG = []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00\x01\x01\x00\x00\x01\x00\x01\x00\x00\xff\xd9")
	testGIF  = []byte("GIF89a\x01\x00\x01\x00\x80\x00\x00\x00\x00\x00\xff\xff\xff!\xf9\x04\x01\x00\x00\x00\x00,\x00\x00\x00\x00\x01\x00\x01\x00\x00\x02\x02D\x01\x00;")
	testWEBP = []byte("RIFF\x1a\x00\x00\x00WEBPVP8L\x0d\x00\x00\x00/\x00\x00\x00\x10\x07\x10\x11\x11\x88\x88\xfe\x07\x00")
)

func getUploadRouter(t *testing.T) (http.Handler, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "uploads")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	old := uploadDir
	uploadDir = dir
	t.Cleanup(func() { uploadDir = old })
	r := chi.NewRouter()
	r.Get("/api/upload/{name}", GetUpload)
	return r, dir
}

func getUpload(h http.Handler, rawName string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/upload/"+rawName, nil)
	h.ServeHTTP(rec, req)
	return rec
}

// #5028: an uploaded image is served by its base name, as what its bytes are, nosniff and inline.
func TestGetUploadServesAnImageByItsBytes(t *testing.T) {
	h, dir := getUploadRouter(t)
	for name, c := range map[string]struct {
		body []byte
		ct   string
	}{
		"screenshot_a.png": {testPNG, "image/png"},
		"photo.jpg":        {testJPEG, "image/jpeg"},
		"anim.gif":         {testGIF, "image/gif"},
		"pic.webp":         {testWEBP, "image/webp"},
		"no_extension":     {testPNG, "image/png"}, // the type is the content's, not the name's
		"wrong.txt":        {testPNG, "image/png"},
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), c.body, 0o600))
		rec := getUpload(h, name)
		require.Equal(t, http.StatusOK, rec.Code, name)
		assert.Equal(t, c.ct, rec.Header().Get("Content-Type"), name)
		assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"), name)
		assert.Contains(t, rec.Header().Get("Content-Disposition"), "inline", name)
		assert.Contains(t, rec.Header().Get("Content-Security-Policy"), "sandbox", name)
		assert.Equal(t, "private, no-cache", rec.Header().Get("Cache-Control"), name)
		assert.Equal(t, c.body, rec.Body.Bytes(), name)
	}
}

// Content-Disposition carries a plain name: a name with quotes, spaces or non-ASCII is not copied into the header as it is.
func TestGetUploadDispositionNameIsPlain(t *testing.T) {
	// A name with a quote, a space, a semicolon and a non-ASCII letter: the quote cannot exist in a Windows file name, so there the name has the rest.
	name, want := `a"b c;d é.png`, `inline; filename=a_b_c_d___.png`
	if runtime.GOOS == "windows" {
		name, want = `a b;c é.png`, `inline; filename=a_b_c___.png`
	}
	h, dir := getUploadRouter(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), testPNG, 0o600))
	rec := getUpload(h, url.PathEscape(name))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, want, rec.Header().Get("Content-Disposition"))
}

// Only an image, by its bytes: SVG (a stored-XSS risk), HTML, text, a bare name with an image extension but other content, BMP and TIFF are refused.
func TestGetUploadRefusesWhatIsNotAnAllowedImage(t *testing.T) {
	h, dir := getUploadRouter(t)
	for name, body := range map[string]string{
		"x.svg":  `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`,
		"y.png":  `<html><script>alert(1)</script></html>`,
		"z.jpg":  "plain text",
		"w.bmp":  "BM" + string(make([]byte, 60)),
		"v.tiff": "II*\x00" + string(make([]byte, 60)),
		"empty":  "",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
		rec := getUpload(h, name)
		assert.Equal(t, http.StatusUnsupportedMediaType, rec.Code, name)
		assert.NotContains(t, rec.Body.String(), "alert", name)
	}
}

// A name is a base name: separators, dot segments, encoded forms, device names and hidden names are 400 and read nothing.
func TestGetUploadRefusesNamesThatAreNotUploadNames(t *testing.T) {
	h, dir := getUploadRouter(t)
	secret := filepath.Join(filepath.Dir(dir), "secret.png")
	require.NoError(t, os.WriteFile(secret, testPNG, 0o600)) // an image outside the folder
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".hidden.png"), testPNG, 0o600))
	for _, raw := range []string{
		"..", "%2e%2e", "../secret.png", "..%2Fsecret.png", "%2e%2e%2fsecret.png", "..%5Csecret.png", "%2e%2e%5csecret.png",
		"a%2Fb.png", "a%5Cb.png", "a%00b.png", "%00", ".hidden.png", "NUL.png", "con", "name.", "name%20",
		url.PathEscape(secret),
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/upload/x", nil)
		req.URL.Path = "/api/upload/" + raw
		req.URL.RawPath = "/api/upload/" + raw
		// The router decodes the segment as the server does; a name that does not match a route at all is as refused as one the handler refuses.
		h.ServeHTTP(rec, req)
		assert.Contains(t, []int{http.StatusBadRequest, http.StatusNotFound}, rec.Code, raw)
		assert.NotEqual(t, http.StatusOK, rec.Code, raw)
		assert.NotContains(t, rec.Header().Get("Content-Type"), "image/", raw)
	}
}

func TestGetUploadMissingIsNotFoundAndAFolderIsRefused(t *testing.T) {
	h, dir := getUploadRouter(t)
	assert.Equal(t, http.StatusNotFound, getUpload(h, "nothing.png").Code)
	require.NoError(t, os.Mkdir(filepath.Join(dir, "adir.png"), 0o700))
	assert.Equal(t, http.StatusBadRequest, getUpload(h, "adir.png").Code, "a folder is not an upload")
}

func TestGetUploadDoesNotFollowASymlinkOut(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	h, dir := getUploadRouter(t)
	outside := filepath.Join(filepath.Dir(dir), "outside.png")
	require.NoError(t, os.WriteFile(outside, testPNG, 0o600))
	linktest.Symlink(t, outside, filepath.Join(dir, "link.png"))
	linktest.Symlink(t, filepath.Dir(dir), filepath.Join(dir, "up"))
	for _, raw := range []string{"link.png", "up%2Foutside.png"} {
		rec := getUpload(h, raw)
		assert.NotEqual(t, http.StatusOK, rec.Code, raw)
		assert.NotContains(t, rec.Header().Get("Content-Type"), "image/", raw)
	}
}

func TestGetUploadWithNoUploadsFolderIsNotFound(t *testing.T) {
	r := chi.NewRouter()
	r.Get("/api/upload/{name}", GetUpload)
	old := uploadDir
	uploadDir = filepath.Join(t.TempDir(), "never-made")
	t.Cleanup(func() { uploadDir = old })
	assert.Equal(t, http.StatusNotFound, getUpload(r, "a.png").Code)
}

// A name no file system could hold is refused before it is decoded or looked up.
func TestGetUploadRefusesAnOverLongName(t *testing.T) {
	h, _ := getUploadRouter(t)
	assert.Equal(t, http.StatusBadRequest, getUpload(h, strings.Repeat("a", 256)+".png").Code)
	assert.Equal(t, http.StatusBadRequest, getUpload(h, strings.Repeat("a", 256*3+1)).Code)
	assert.Equal(t, http.StatusNotFound, getUpload(h, strings.Repeat("a", 251)+".png").Code, "255 bytes is still a name")
}
