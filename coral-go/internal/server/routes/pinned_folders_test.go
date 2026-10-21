package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
	"github.com/subgentic/coral/pkg/tunnel"
)

// #3630: pinned folders in the Agents list.

type pinRig struct {
	t   *testing.T
	srv *httptest.Server
	ss  *store.SessionStore
}

func newPinRig(t *testing.T) *pinRig {
	t.Helper()
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "pins.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	cfg := &config.Config{LogDir: t.TempDir()}
	cfg.UseCoralDir(t.TempDir())
	h := NewSystemHandler(db, cfg)
	r := chi.NewRouter()
	ident := testIdentity(db)
	r.Use(func(next http.Handler) http.Handler {
		withIdent := ident(next)
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.Header.Get(testDashboardHeader) != "" {
				next.ServeHTTP(w, req.WithContext(context.WithValue(req.Context(), callerKindKey{}, callerDashCookie)))
				return
			}
			withIdent.ServeHTTP(w, req)
		})
	})
	r.Get("/api/pinned-folders", h.ListPinnedFolders)
	r.Post("/api/pinned-folders", h.PinFolder)
	r.Delete("/api/pinned-folders", h.UnpinFolder)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return &pinRig{t: t, srv: srv, ss: store.NewSessionStore(db)}
}

var pinAsDashboard = map[string]string{testDashboardHeader: "1"}

func (p *pinRig) do(method, path string, hdr map[string]string) (int, map[string]any) {
	p.t.Helper()
	var body *bytes.Reader
	if path != "" {
		b, _ := json.Marshal(map[string]string{"path": path})
		body = bytes.NewReader(b)
	} else {
		body = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, p.srv.URL+"/api/pinned-folders", body)
	require.NoError(p.t, err)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(p.t, err)
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (p *pinRig) list() []map[string]any {
	p.t.Helper()
	code, out := p.do(http.MethodGet, "", nil)
	require.Equal(p.t, http.StatusOK, code)
	var got []map[string]any
	for _, f := range out["folders"].([]any) {
		got = append(got, f.(map[string]any))
	}
	return got
}

func TestPinnedFoldersAreTheOperators(t *testing.T) {
	rig := newPinRig(t)
	dir := t.TempDir()
	for name, hdr := range map[string]map[string]string{
		"an agent's token":         {testAgentHeader: "s1"},
		"a token-less script":      {},
		"a forged Coral Hub stamp": {tunnel.ViaHeader: "x"},
	} {
		code, out := rig.do(http.MethodPost, dir, hdr)
		assert.Equal(t, http.StatusForbidden, code, name)
		assert.Equal(t, "operator_only", out["code"], name)
	}
	assert.Empty(t, rig.list(), "nothing pinned by the refused callers")

	code, out := rig.do(http.MethodPost, dir, pinAsDashboard)
	require.Equal(t, http.StatusOK, code, out)
	assert.Equal(t, filepath.Clean(dir), out["path"])
	assert.Equal(t, filepath.Base(dir), out["name"])
	assert.Equal(t, true, out["exists"])

	code, out = rig.do(http.MethodDelete, dir, map[string]string{testAgentHeader: "s1"})
	assert.Equal(t, http.StatusForbidden, code, "an agent can't unpin either")
	assert.Equal(t, "operator_only", out["code"])
	assert.Len(t, rig.list(), 1)
}

func TestPinFolderChecksThePathLikeALaunch(t *testing.T) {
	rig := newPinRig(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	require.NoError(t, os.MkdirAll(filepath.Join(home, "proj"), 0o700))
	file := filepath.Join(t.TempDir(), "a-file")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))

	for _, bad := range []string{"", "   ", "relative/dir", "./x", filepath.Join(t.TempDir(), "missing"), file,
		`\\host\share\x`, "/tmp/a\nb", "/tmp/\x7f", "/" + string(bytes.Repeat([]byte("a"), 5000))} {
		code, out := rig.do(http.MethodPost, bad, pinAsDashboard)
		assert.Equal(t, http.StatusBadRequest, code, "%q: %v", bad, out)
	}
	assert.Empty(t, rig.list())

	// ~ is the home folder; the stored path is clean and absolute, and pinning twice keeps one.
	code, out := rig.do(http.MethodPost, "~/proj/../proj/", pinAsDashboard)
	require.Equal(t, http.StatusOK, code, out)
	assert.Equal(t, filepath.Join(home, "proj"), out["path"])
	code, _ = rig.do(http.MethodPost, filepath.Join(home, "proj"), pinAsDashboard)
	require.Equal(t, http.StatusOK, code)
	got := rig.list()
	require.Len(t, got, 1, "pinning a pinned folder changes nothing")
	assert.Equal(t, "proj", got[0]["name"])
}

func TestUnpinFolderEvenWhenItIsGone(t *testing.T) {
	rig := newPinRig(t)
	gone := filepath.Join(t.TempDir(), "gone")
	require.NoError(t, os.Mkdir(gone, 0o700))
	keep := t.TempDir()
	for _, d := range []string{gone, keep} {
		code, out := rig.do(http.MethodPost, d, pinAsDashboard)
		require.Equal(t, http.StatusOK, code, out)
	}
	require.NoError(t, os.Remove(gone))
	got := rig.list()
	require.Len(t, got, 2)
	exists := map[string]bool{}
	for _, f := range got {
		exists[f["path"].(string)] = f["exists"].(bool)
	}
	assert.False(t, exists[gone], "a pinned folder that was removed is listed as missing")
	assert.True(t, exists[keep])

	code, out := rig.do(http.MethodDelete, gone, pinAsDashboard)
	require.Equal(t, http.StatusOK, code, out)
	assert.Equal(t, true, out["unpinned"])
	code, out = rig.do(http.MethodDelete, gone, pinAsDashboard)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, false, out["unpinned"], "unpinning again reports nothing to unpin")
	got = rig.list()
	require.Len(t, got, 1)
	assert.Equal(t, keep, got[0]["path"])
}

func TestPinnedFoldersAreCapped(t *testing.T) {
	rig := newPinRig(t)
	ctx := context.Background()
	for i := 0; i < store.MaxPinnedFolders; i++ {
		require.NoError(t, rig.ss.PinFolder(ctx, fmt.Sprintf("/pinned/%03d", i)))
	}
	code, out := rig.do(http.MethodPost, t.TempDir(), pinAsDashboard)
	assert.Equal(t, http.StatusConflict, code, out)
	// Re-pinning one already pinned is fine at the cap.
	require.NoError(t, rig.ss.PinFolder(ctx, "/pinned/000"))
	assert.Len(t, rig.list(), store.MaxPinnedFolders)
}

func TestPinnedFoldersThroughCoralHub(t *testing.T) {
	assert.Equal(t, tunnel.TierView, tunnel.Classify(http.MethodGet, "/api/pinned-folders", ""), "reading the pins is View")
	assert.Equal(t, tunnel.TierLaunch, tunnel.Classify(http.MethodPost, "/api/pinned-folders", ""), "pinning is Launch")
	assert.Equal(t, tunnel.TierLaunch, tunnel.Classify(http.MethodDelete, "/api/pinned-folders", ""), "unpinning is Launch")
}
