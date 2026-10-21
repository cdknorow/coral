package routes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/pkg/tunnel"
)

// The artifact store (#3553, from the #3551 audit): streamed uploads, image metadata by the
// bytes, the hourly and store caps, the Operator-only delete, and Cache-Control.

const testDashboardHeader = "X-Test-Dashboard"

// artifactRig is a server with the artifact routes on its own data folder. A request with
// testDashboardHeader acts as this computer's dashboard; one with testAgentHeader as an agent.
type artifactRig struct {
	t        *testing.T
	srv      *httptest.Server
	h        *SessionsHandler
	ss       *store.SessionStore
	coralDir string
}

func newArtifactRig(t *testing.T) *artifactRig {
	t.Helper()
	cfg := config.Load(t.TempDir())
	_, h, _, ss := setupSessionsTestServerWithConfig(t, cfg)
	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: "s1", AgentName: "a1", AgentType: "codex", WorkingDir: t.TempDir()}))
	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: "s2", AgentName: "a2", AgentType: "codex", WorkingDir: t.TempDir()}))
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler { // what server.buildHeader does to every /api/ answer
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			w.Header().Set("X-Coral-Build", "testbuild0001")
			next.ServeHTTP(w, req)
		})
	})
	ident := testIdentity(h.db)
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
	h.RegisterAgentArtifacts(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return &artifactRig{t: t, srv: srv, h: h, ss: ss, coralDir: cfg.CoralDir()}
}

// upload sends data as session's agent (its token); it returns the status and the JSON body.
func (rig *artifactRig) upload(session, name, mediaType string, body io.Reader) (int, map[string]any) {
	code, out, _ := rig.uploadH(session, name, mediaType, body)
	return code, out
}

// uploadH is upload, also returning the response headers (for Retry-After on a 429).
func (rig *artifactRig) uploadH(session, name, mediaType string, body io.Reader) (int, map[string]any, http.Header) {
	req, err := http.NewRequest(http.MethodPost, rig.srv.URL+"/api/agent/artifacts?session_id="+session, body)
	require.NoError(rig.t, err)
	req.Header.Set("X-Artifact-Name", name)
	if mediaType != "" {
		req.Header.Set("X-Artifact-Media-Type", mediaType)
	}
	resp, err := agentClient.Do(req)
	require.NoError(rig.t, err)
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out, resp.Header
}

func (rig *artifactRig) del(id string, header map[string]string) (int, map[string]any) {
	req, err := http.NewRequest(http.MethodDelete, rig.srv.URL+"/api/artifacts/"+id, nil)
	require.NoError(rig.t, err)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(rig.t, err)
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (rig *artifactRig) get(id string) (int, http.Header, []byte) {
	resp, err := http.Get(rig.srv.URL + "/api/artifacts/" + id)
	require.NoError(rig.t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, b
}

func (rig *artifactRig) object(id string) string {
	return filepath.Join(rig.coralDir, "artifacts", "objects", id)
}

func idOf(uri any) string { return strings.TrimPrefix(fmt.Sprint(uri), "coral://artifacts/") }

func sha(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func TestArtifactUploadStreamsAndKeepsTheID(t *testing.T) {
	rig := newArtifactRig(t)
	body := bytes.Repeat([]byte("streamed artifact line\n"), 50_000) // ~1.1 MB
	code, out := rig.upload("s1", "log.txt", "text/plain", bytes.NewReader(body))
	require.Equal(t, http.StatusCreated, code, out)
	require.Equal(t, sha(body), idOf(out["uri"]), "a non-image's id is the hash of what was sent")
	require.EqualValues(t, len(body), out["size"])
	require.Equal(t, false, out["stripped"])
	stored, err := os.ReadFile(rig.object(sha(body)))
	require.NoError(t, err)
	require.Equal(t, body, stored)
	// No temp file left behind.
	ents, _ := os.ReadDir(filepath.Dir(rig.object("x")))
	for _, e := range ents {
		assert.False(t, strings.HasPrefix(e.Name(), artifactUploadTempP), e.Name())
	}
	_, hdr, _ := rig.get(sha(body))
	require.Equal(t, "private, max-age=31536000, immutable", hdr.Get("Cache-Control"), "no shared cache keeps a deletable artifact")
}

// #5312: a year-cached artifact answer must not carry the build id of the server that sent it, or a Preview after an update reads an old id from the browser cache
// and the dashboard says "Coral was updated" for good.
func TestArtifactAnswerNamesNoBuild(t *testing.T) {
	rig := newArtifactRig(t)
	body := []byte("a note\n")
	code, out, upHdr := rig.uploadH("s1", "n.txt", "text/plain", bytes.NewReader(body))
	require.Equal(t, http.StatusCreated, code, out)
	require.Equal(t, "testbuild0001", upHdr.Get("X-Coral-Build"), "the rig's middleware does name the build on an ordinary answer")
	status, hdr, got := rig.get(sha(body))
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, body, got)
	require.Contains(t, hdr.Get("Cache-Control"), "immutable")
	_, has := hdr["X-Coral-Build"]
	require.False(t, has, "an immutable answer names no build")
}

func TestArtifactUploadTooLarge(t *testing.T) {
	rig := newArtifactRig(t)
	code, out := rig.upload("s1", "big.bin", "", io.LimitReader(zeroReader{}, maxAgentArtifactSize+1))
	require.Equal(t, http.StatusRequestEntityTooLarge, code)
	require.Equal(t, "artifact_too_large", out["code"])
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

func exifJPEG(t *testing.T) []byte {
	j := jpegBytes(t)
	app1 := append([]byte{0xFF, 0xE1, 0, 0}, []byte("Exif\x00\x00GPS-LAT-51.5")...)
	binary.BigEndian.PutUint16(app1[2:], uint16(len(app1)-2))
	return append(append([]byte{0xFF, 0xD8}, app1...), j[2:]...)
}

func TestArtifactImageMetadataByTheBytes(t *testing.T) {
	rig := newArtifactRig(t)
	img := exifJPEG(t)
	// Labelled as anything but an image: the bytes decide.
	code, out := rig.upload("s1", "photo.bin", "application/octet-stream", bytes.NewReader(img))
	require.Equal(t, http.StatusCreated, code, out)
	require.Equal(t, true, out["stripped"])
	id := idOf(out["uri"])
	require.NotEqual(t, sha(img), id, "the id is the hash of the STORED bytes")
	stored, err := os.ReadFile(rig.object(id))
	require.NoError(t, err)
	require.Equal(t, sha(stored), id)
	require.NotContains(t, string(stored), "GPS-LAT", "EXIF removed")
	require.EqualValues(t, len(stored), out["size"], "the response gives the stored size")
	require.Less(t, len(stored), len(img))

	// Formats the server can't strip are refused when they carry metadata.
	webp := webpLossless(4, 4, append([]byte("EXIF\x04\x00\x00\x00"), "GPS!"...))
	code, out = rig.upload("s1", "a.webp", "", bytes.NewReader(webp))
	require.Equal(t, http.StatusBadRequest, code)
	require.Equal(t, "image_metadata", out["code"])
	heic := append([]byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00mif1heic"), []byte("....infe....Exif....GPS")...)
	code, out = rig.upload("s1", "a.heic", "", bytes.NewReader(heic))
	require.Equal(t, http.StatusBadRequest, code, out)
	require.Equal(t, "image_metadata", out["code"])
	tiff := []byte("II*\x00\x08\x00\x00\x00\x01\x00\x25\x88\x04\x00\x01\x00\x00\x00\x1a\x00\x00\x00\x00\x00\x00\x00") // one IFD entry: the GPS IFD tag (0x8825)
	code, out = rig.upload("s1", "a.tif", "", bytes.NewReader(tiff))
	require.Equal(t, http.StatusBadRequest, code, out)
	// A plain PNG (nothing to strip) keeps its id.
	p := pngBytes(t, 4, 4)
	code, out = rig.upload("s1", "p.png", "", bytes.NewReader(p))
	require.Equal(t, http.StatusCreated, code, out)
	require.Equal(t, sha(p), idOf(out["uri"]))
}

func TestArtifactHourlyCap(t *testing.T) {
	t.Parallel() // no shared state: these ran one after another (#5622)
	rig := newArtifactRig(t)
	for i := 0; i < uploadWindowCount; i++ {
		code, out := rig.upload("s1", "n.txt", "", strings.NewReader(fmt.Sprintf("note %d", i)))
		require.Equal(t, http.StatusCreated, code, out)
	}
	// The hourly count cap is a rate limit: 429 Too Many Requests with a Retry-After (#4007).
	code, out, hdr := rig.uploadH("s1", "n.txt", "", strings.NewReader("one too many"))
	require.Equal(t, http.StatusTooManyRequests, code)
	require.Equal(t, "artifact_quota", out["code"])
	require.Equal(t, "hourly", out["limit"])
	require.NotEmpty(t, hdr.Get("Retry-After"), "a 429 count cap carries Retry-After")
	code, _ = rig.upload("s2", "n.txt", "", strings.NewReader("another agent"))
	require.Equal(t, http.StatusCreated, code, "the cap is per agent session")

	// A refused upload's bytes count too (a client can't probe for free); the hourly byte
	// window is a rate limit too (429).
	st := storeFor(rig.coralDir)
	st.mu.Lock()
	st.windows["session:s2"].bytes = uploadWindowBytes - 10
	st.mu.Unlock()
	code, out = rig.upload("s2", "big.txt", "", strings.NewReader(strings.Repeat("x", 100)))
	require.Equal(t, http.StatusTooManyRequests, code, out)
	require.Equal(t, "hourly", out["limit"])
}

func TestArtifactStoreCapAndDeleteFreesIt(t *testing.T) {
	rig := newArtifactRig(t)
	require.NoError(t, rig.ss.SetSetting(context.Background(), ArtifactStoreMaxBytesSetting, "1000"))
	a := bytes.Repeat([]byte("a"), 600)
	code, out := rig.upload("s1", "a.txt", "", bytes.NewReader(a))
	require.Equal(t, http.StatusCreated, code, out)
	code, out = rig.upload("s1", "b.txt", "", bytes.NewReader(bytes.Repeat([]byte("b"), 600)))
	require.Equal(t, http.StatusRequestEntityTooLarge, code)
	require.Equal(t, "store", out["limit"])
	require.Contains(t, out["message"], "ask the Operator to delete old artifacts")
	code, _ = rig.upload("s1", "a-again.txt", "", bytes.NewReader(a))
	require.Equal(t, http.StatusCreated, code, "a duplicate is free, even at the cap")

	code, out = rig.del(sha(a), map[string]string{testDashboardHeader: "1"})
	require.Equal(t, http.StatusOK, code, out)
	require.EqualValues(t, 600, out["bytes"])
	code, _ = rig.upload("s1", "b.txt", "", bytes.NewReader(bytes.Repeat([]byte("b"), 600)))
	require.Equal(t, http.StatusCreated, code, "a delete frees quota")

	resp, err := http.Get(rig.srv.URL + "/api/system/artifact-store")
	require.NoError(t, err)
	var st map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&st))
	resp.Body.Close()
	require.EqualValues(t, 600, st["bytes"])
	require.EqualValues(t, 1, st["objects"])
	require.EqualValues(t, 1000, st["max_bytes"])
}

// #4602: with no limit set the store has one, 20 GiB: a new upload is refused at it (nothing is deleted), a duplicate is still free,
// the refusal says how to raise it, and an explicit 0 turns the limit off.
func TestArtifactStoreHasADefaultCap(t *testing.T) {
	rig := newArtifactRig(t)
	code, out := rig.upload("s1", "first.txt", "", strings.NewReader("first"))
	require.Equal(t, http.StatusCreated, code, out)
	st := storeFor(rig.coralDir)
	b, _, max := st.size()
	require.EqualValues(t, 20<<30, max, "nothing set: the default limit")
	st.added(max - b) // the store is exactly at its limit
	code, out = rig.upload("s1", "second.txt", "", strings.NewReader("second"))
	require.Equal(t, http.StatusRequestEntityTooLarge, code, out)
	require.Equal(t, "store", out["limit"])
	require.Contains(t, out["message"], ArtifactStoreMaxBytesSetting, "the refusal names the setting that raises it")
	code, _ = rig.upload("s1", "first-again.txt", "", strings.NewReader("first"))
	require.Equal(t, http.StatusCreated, code, "bytes already stored are free at the limit")

	require.NoError(t, rig.ss.SetSetting(context.Background(), ArtifactStoreMaxBytesSetting, "0"))
	code, out = rig.upload("s1", "second.txt", "", strings.NewReader("second"))
	require.Equal(t, http.StatusCreated, code, "0 is the Operator's no limit: %v", out)
}

func TestArtifactDeleteIsTheOperators(t *testing.T) {
	rig := newArtifactRig(t)
	data := []byte("delete me")
	id := sha(data)
	code, _ := rig.upload("s1", "d.txt", "", bytes.NewReader(data))
	require.Equal(t, http.StatusCreated, code)
	code, _ = rig.upload("s2", "d2.txt", "", bytes.NewReader(data)) // a second agent's record of it
	require.Equal(t, http.StatusCreated, code)

	for name, hdr := range map[string]map[string]string{
		"an agent's token":       {testAgentHeader: "s1"},
		"a token-less script":    {},
		"Coral Hub (the tunnel)": {tunnel.ViaHeader: "x"},
	} {
		code, out := rig.del(id, hdr)
		assert.Equal(t, http.StatusForbidden, code, name)
		want := "operator_only"
		if hdr[tunnel.ViaHeader] != "" {
			want = "owner_only" // through the hub only the machine's owner may delete (#5575)
		}
		assert.Equal(t, want, out["code"], name)
	}
	_, err := os.Stat(rig.object(id))
	require.NoError(t, err, "still there after the refusals")

	code, _ = rig.del("not-hex", map[string]string{testDashboardHeader: "1"})
	require.Equal(t, http.StatusBadRequest, code)
	code, out := rig.del(id, map[string]string{testDashboardHeader: "1"})
	require.Equal(t, http.StatusOK, code, out)
	for _, p := range []string{rig.object(id), rig.object(id) + ".json",
		filepath.Join(rig.coralDir, "artifacts", "uploads", "s1", id+".json"), filepath.Join(rig.coralDir, "artifacts", "uploads", "s2", id+".json")} {
		_, err := os.Stat(p)
		assert.True(t, os.IsNotExist(err), "removed: %s", p)
	}
	code, _, _ = rig.get(id)
	require.Equal(t, http.StatusNotFound, code)
	code, _ = rig.del(id, map[string]string{testDashboardHeader: "1"})
	require.Equal(t, http.StatusNotFound, code)
}

// Uploads and deletes of one digest, at the same time: the store is never left half done (an
// object without its sidecar, or a sidecar without its object), and the size stays exact.
func TestArtifactUploadDeleteRace(t *testing.T) {
	t.Parallel() // no shared state: these ran one after another (#5622)
	rig := newArtifactRig(t)
	data := []byte("raced artifact")
	id := sha(data)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); rig.upload("s1", "r.txt", "", bytes.NewReader(data)) }()
		go func() { defer wg.Done(); rig.del(id, map[string]string{testDashboardHeader: "1"}) }()
	}
	wg.Wait()
	_, objErr := os.Stat(rig.object(id))
	_, sideErr := os.Stat(rig.object(id) + ".json")
	require.Equal(t, os.IsNotExist(objErr), os.IsNotExist(sideErr), "object and sidecar together, or neither")
	b, n, _ := storeFor(rig.coralDir).size()
	if objErr == nil {
		require.EqualValues(t, len(data), b)
		require.Equal(t, 1, n)
	} else {
		require.Zero(t, b)
		require.Zero(t, n)
	}
}

// The size is measured from the objects only, and a crash's temp file is removed.
func TestArtifactStoreMeasure(t *testing.T) {
	dir := t.TempDir()
	objects := filepath.Join(dir, "artifacts", "objects")
	require.NoError(t, os.MkdirAll(objects, 0o700))
	obj := bytes.Repeat([]byte("o"), 300)
	require.NoError(t, os.WriteFile(filepath.Join(objects, sha(obj)), obj, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(objects, sha(obj)+".json"), []byte(`{"name":"o"}`), 0o600))
	stale := filepath.Join(objects, artifactUploadTempP+"crash")
	fresh := filepath.Join(objects, artifactUploadTempP+"live")
	require.NoError(t, os.WriteFile(stale, make([]byte, 5000), 0o600))
	require.NoError(t, os.WriteFile(fresh, make([]byte, 5000), 0o600))
	staleAtomic := filepath.Join(objects, artifactAtomicTempP+"crash") // #3552's atomic writes
	require.NoError(t, os.WriteFile(staleAtomic, make([]byte, 5000), 0o600))
	old := time.Now().Add(-2 * time.Hour)
	require.NoError(t, os.Chtimes(stale, old, old))
	require.NoError(t, os.Chtimes(staleAtomic, old, old))
	b, n, _ := storeFor(dir).size()
	require.EqualValues(t, 300, b, "only objects count")
	require.Equal(t, 1, n)
	_, err := os.Stat(stale)
	require.True(t, os.IsNotExist(err), "a crash's temp file is removed")
	_, err = os.Stat(staleAtomic)
	require.True(t, os.IsNotExist(err), "a crash's atomic-write temp file is removed")
	_, err = os.Stat(fresh)
	require.NoError(t, err, "a live upload's temp file is left alone")
}

func TestSniffImageFamily(t *testing.T) {
	for want, data := range map[string][]byte{
		"image/jpeg": {0xFF, 0xD8, 0xFF, 0xE0},
		"image/png":  []byte("\x89PNG\r\n\x1a\nxxxx"),
		"image/gif":  []byte("GIF89a......"),
		"image/webp": []byte("RIFF\x00\x00\x00\x00WEBPVP8L"),
		"image/tiff": []byte("MM\x00*\x00\x00\x00\x08"),
		"image/heif": []byte("\x00\x00\x00\x18ftypavif"),
		"":           []byte("plain text, not an image"),
	} {
		assert.Equal(t, want, sniffImageFamily(data), want)
	}
}

// A streamed upload follows #3552: the same bytes uploaded again under another name keep the
// first writer's sidecar (and so the name viewers get); the second uploader's record has its own,
// and the files are 0600.
func TestArtifactStreamedUploadFirstWriterWins(t *testing.T) {
	rig := newArtifactRig(t)
	body := []byte("the same bytes, twice " + t.Name())
	st, first := rig.upload("s1", "first.txt", "text/plain", bytes.NewReader(body))
	require.Equal(t, http.StatusCreated, st, first)
	st, second := rig.upload("s2", "second.md", "text/markdown", bytes.NewReader(body))
	require.Equal(t, http.StatusCreated, st, second)
	require.Equal(t, first["uri"], second["uri"])
	require.Equal(t, "first.txt", second["name"])
	require.Equal(t, "text/plain", second["media_type"])
	id := idOf(second["uri"])
	var side agentArtifactMeta
	raw, err := os.ReadFile(rig.object(id) + ".json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &side))
	require.Equal(t, "first.txt", side.Name)
	if runtime.GOOS != "windows" { // Windows reports 0666; folder privacy there is the data-dir ACL (#3637), not the mode.
		for _, p := range []string{rig.object(id), rig.object(id) + ".json"} {
			info, err := os.Stat(p)
			require.NoError(t, err)
			require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), p)
		}
	}
}

// tiffWith builds a little-endian TIFF whose IFDs hold the given tags (one SHORT value each).
func tiffWith(ifds ...[]uint16) []byte {
	out := []byte("II*\x00\x08\x00\x00\x00")
	for i, tags := range ifds {
		out = binary.LittleEndian.AppendUint16(out, uint16(len(tags)))
		for _, tag := range tags {
			out = binary.LittleEndian.AppendUint16(out, tag)
			out = append(out, 3, 0, 1, 0, 0, 0, 1, 0, 0, 0) // SHORT, count 1, value 1
		}
		next := uint32(0)
		if i < len(ifds)-1 {
			next = uint32(len(out) + 4)
		}
		out = binary.LittleEndian.AppendUint32(out, next)
	}
	return out
}

// Security's #3553 review (SHOULD 1): a TIFF with any tag beyond the pixel data is refused, in
// any IFD of the chain; one with only pixel tags is stored.
func TestTIFFOnlyPixelTags(t *testing.T) {
	pixels := []uint16{0x0100, 0x0101, 0x0102, 0x0103, 0x0106, 0x0111, 0x0115, 0x0116, 0x0117, 0x011A, 0x011B, 0x0128}
	assert.False(t, tiffHasMetadata(tiffWith(pixels)), "pixel tags only")
	assert.False(t, tiffHasMetadata(tiffWith(pixels, pixels)), "two pages, pixel tags only")
	for _, tag := range []uint16{0x010E, 0x010F, 0x0110, 0x0131, 0x0132, 0x013B, 0x013C, 0x8298, 0x83BB, 0x8649, 0x8769, 0x8825, 0x02BC} {
		assert.True(t, tiffHasMetadata(tiffWith(append(append([]uint16{}, pixels...), tag))), "tag 0x%04X in the first IFD", tag)
		assert.True(t, tiffHasMetadata(tiffWith(pixels, []uint16{0x0100, tag})), "tag 0x%04X in the second IFD", tag)
	}
	loop := tiffWith(pixels)
	binary.LittleEndian.PutUint32(loop[len(loop)-4:], 8) // the next IFD points back at the first
	assert.True(t, tiffHasMetadata(loop), "an IFD loop is unreadable")

	rig := newArtifactRig(t)
	code, out := rig.upload("s1", "clean.tif", "", bytes.NewReader(tiffWith(pixels)))
	require.Equal(t, http.StatusCreated, code, out)
	code, out = rig.upload("s1", "artist.tif", "", bytes.NewReader(tiffWith(append(append([]uint16{}, pixels...), 0x013B))))
	require.Equal(t, http.StatusBadRequest, code, out)
	require.Equal(t, "image_metadata", out["code"])
}

// Security's #3553 review (SHOULD 2): when the sidecar can't be created, the object just added is
// removed again, so nothing unservable stays behind or counts against the store.
func TestArtifactSidecarFailureLeavesNoObject(t *testing.T) {
	rig := newArtifactRig(t)
	body := []byte("no sidecar for these bytes " + t.Name())
	id := sha(body)
	require.NoError(t, os.MkdirAll(rig.object(id)+".json", 0o700)) // a folder where the sidecar goes
	before, objects, _ := storeFor(rig.coralDir).size()
	code, _ := rig.upload("s1", "x.txt", "text/plain", bytes.NewReader(body))
	require.Equal(t, http.StatusInternalServerError, code)
	_, err := os.Stat(rig.object(id))
	require.True(t, os.IsNotExist(err), "the object is removed")
	after, objectsAfter, _ := storeFor(rig.coralDir).size()
	require.Equal(t, before, after)
	require.Equal(t, objects, objectsAfter)
}

// QA's #3553 nit: sizes are rounded down and shown in bytes under 1 KiB, so "500 B of 600 B",
// not "1 KiB of 1 KiB".
func TestHumanBytes(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 500: "500 B", 1023: "1023 B", 1024: "1.0 KiB", 1535: "1.4 KiB",
		(10 << 30) - 1: "9.9 GiB", 10 << 30: "10.0 GiB", 3 << 20: "3.0 MiB"} {
		assert.Equal(t, want, humanBytes(n), n)
	}
}
