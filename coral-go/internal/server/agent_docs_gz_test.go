package server

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/server/routes"
)

var docFixture = map[string]string{
	"README.md":   "# Coral docs\n\nStart here.\n",
	"board.md":    "# Board\n\n" + strings.Repeat("The board is where agents talk. ", 40) + "\n",
	"auth.md":     "# Auth\n\nTokens.\n",
	"notadoc.png": "binary-ish",
}

func gz(s string) []byte {
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	zw.Write([]byte(s))
	zw.Close()
	return b.Bytes()
}

// plainFS holds the docs as before #6814; gzFS holds the same docs as tools/sync-agent-docs.sh now writes them.
func plainFS() fstest.MapFS {
	m := fstest.MapFS{}
	for n, c := range docFixture {
		m[n] = &fstest.MapFile{Data: []byte(c)}
	}
	return m
}

func gzFS() fstest.MapFS {
	m := fstest.MapFS{}
	for n, c := range docFixture {
		if strings.HasSuffix(n, ".md") {
			m[n+".gz"] = &fstest.MapFile{Data: gz(c)}
		} else {
			m[n] = &fstest.MapFile{Data: []byte(c)}
		}
	}
	return m
}

func docsServer(t *testing.T, fsys routes.AgentDocsFS) *httptest.Server {
	h := routes.NewAgentDocsHandler(fsys)
	r := chi.NewRouter()
	r.Get("/api/agent-docs", h.List)
	r.Get("/api/agent-docs/all", h.GetAll)
	r.Get("/api/agent-docs/{name}", h.Get)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

// The digest and the startup line are of the plain text: the same docs hash the same whether they are stored gzipped or not, and the count is of docs.
func TestAgentDocsDigestIsTheSameForGzippedDocs(t *testing.T) {
	nPlain, shaPlain := agentDocsDigest(plainFS())
	nGz, shaGz := agentDocsDigest(newGzDocsFS(gzFS()))
	assert.Equal(t, nPlain, nGz)
	assert.Equal(t, shaPlain, shaGz, "unzipped, the docs are the same bytes")
	assert.Contains(t, agentDocsStartupLine(newGzDocsFS(gzFS())), "agent docs 4 files, sha "+shaPlain)

	// a plain copy through the adapter is read as it is
	nOld, shaOld := agentDocsDigest(newGzDocsFS(plainFS()))
	assert.Equal(t, nPlain, nOld)
	assert.Equal(t, shaPlain, shaOld)
}

func docsGet(t *testing.T, url string, acceptEncoding string) (*http.Response, []byte) {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Accept-Encoding", acceptEncoding) // set explicitly: Go's client then does not unzip for us
	resp, err := http.DefaultTransport.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

// /api/agent-docs/all: a client that accepts gzip gets the stored gzip, one that does not gets the plain text; both are the same text, which is what the plain copy served before.
func TestAgentDocsAllServesGzipOrPlainByAcceptEncoding(t *testing.T) {
	plain := docsServer(t, plainFS())
	zipped := docsServer(t, newGzDocsFS(gzFS()))
	_, want := docsGet(t, plain.URL+"/api/agent-docs/all", "identity")
	require.NotEmpty(t, want)

	resp, body := docsGet(t, zipped.URL+"/api/agent-docs/all", "gzip, deflate")
	assert.Equal(t, "gzip", resp.Header.Get("Content-Encoding"))
	assert.Contains(t, resp.Header.Get("Vary"), "Accept-Encoding")
	zr, err := gzip.NewReader(bytes.NewReader(body))
	require.NoError(t, err)
	got, err := io.ReadAll(zr)
	require.NoError(t, err)
	assert.Equal(t, string(want), string(got), "the concatenated gzip members unzip to the plain text, README first")
	assert.Less(t, len(body), len(want), "smaller on the wire")

	for _, ae := range []string{"identity", "", "br", "gzip;q=0"} {
		resp, body := docsGet(t, zipped.URL+"/api/agent-docs/all", ae)
		assert.Empty(t, resp.Header.Get("Content-Encoding"), "Accept-Encoding %q", ae)
		assert.Equal(t, string(want), string(body), "Accept-Encoding %q: unzipped for the client", ae)
		assert.Contains(t, resp.Header.Get("Vary"), "Accept-Encoding")
	}
}

// One doc and the list read through the adapter: names are the plain ones, not .md.gz.
func TestAgentDocsListAndGetUnzipTheStoredDocs(t *testing.T) {
	srv := docsServer(t, newGzDocsFS(gzFS()))
	_, list := docsGet(t, srv.URL+"/api/agent-docs", "identity")
	assert.Contains(t, string(list), `"name":"board"`)
	assert.NotContains(t, string(list), ".gz")
	_, one := docsGet(t, srv.URL+"/api/agent-docs/auth", "gzip")
	assert.Contains(t, string(one), `# Auth`)
	resp, _ := docsGet(t, srv.URL+"/api/agent-docs/missing", "identity")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// A plain copy (an old sync) is served as before even to a client that accepts gzip: GzipAll declines, the plain path runs.
func TestAgentDocsAllFallsBackForAPlainCopy(t *testing.T) {
	srv := docsServer(t, newGzDocsFS(plainFS()))
	resp, body := docsGet(t, srv.URL+"/api/agent-docs/all", "gzip")
	assert.Empty(t, resp.Header.Get("Content-Encoding"))
	assert.Contains(t, string(body), "# Coral docs")
}
