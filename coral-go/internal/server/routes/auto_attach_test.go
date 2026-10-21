package routes

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	setreg "github.com/subgentic/coral/internal/settings"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/linktest"
)

var coralLinkRe = regexp.MustCompile(`coral://artifacts/([0-9a-f]{64})`)

type attachRig struct {
	t        *testing.T
	wd, git  string
	coralDir string
	a        *AutoAttacher
	on       bool
}

// newAttachRig: a git root holding the agent's working directory (wd = git/app).
func newAttachRig(t *testing.T) *attachRig {
	t.Helper()
	base := realPath(t.TempDir())
	rig := &attachRig{t: t, git: filepath.Join(base, "repo"), coralDir: filepath.Join(base, "coral"), on: true}
	rig.wd = filepath.Join(rig.git, "app")
	require.NoError(t, os.MkdirAll(rig.wd, 0o755))
	rig.a = NewAutoAttacher(rig.coralDir, func(context.Context) bool { return rig.on },
		func(context.Context, string) (string, string) { return rig.wd, rig.git })
	return rig
}

func (rig *attachRig) write(rel string, data []byte) string {
	p := filepath.Join(rig.wd, rel)
	if filepath.IsAbs(rel) {
		p = rel
	}
	require.NoError(rig.t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(rig.t, os.WriteFile(p, data, 0o644))
	return p
}

func (rig *attachRig) post(content string) string {
	return rig.a.Rewrite(context.Background(), "team", "sid-dev", "claude-dev", content)
}

// stored returns the artifact bytes and metadata a rewritten post links to.
func (rig *attachRig) stored(post string) ([]byte, agentArtifactMeta) {
	m := coralLinkRe.FindStringSubmatch(post)
	require.NotNil(rig.t, m, "no artifact link in %q", post)
	obj := filepath.Join(rig.coralDir, "artifacts", "objects", m[1])
	data, err := os.ReadFile(obj)
	require.NoError(rig.t, err)
	var meta agentArtifactMeta
	b, err := os.ReadFile(obj + ".json")
	require.NoError(rig.t, err)
	require.NoError(rig.t, json.Unmarshal(b, &meta))
	return data, meta
}

func pngBytes(t *testing.T, w, h int) []byte {
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, image.NewGray(image.Rect(0, 0, w, h))))
	return buf.Bytes()
}

func jpegBytes(t *testing.T) []byte {
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, image.NewGray(image.Rect(0, 0, 4, 4)), nil))
	return buf.Bytes()
}

// webpLossless is a minimal VP8L header (enough for the type and size checks).
func webpLossless(w, h int, extra ...[]byte) []byte {
	vp8l := []byte{0x2f, 0, 0, 0, 0}
	binary.LittleEndian.PutUint32(vp8l[1:], uint32(w-1)|uint32(h-1)<<14)
	chunk := func(id string, data []byte) []byte {
		c := append([]byte(id), 0, 0, 0, 0)
		binary.LittleEndian.PutUint32(c[4:], uint32(len(data)))
		c = append(c, data...)
		if len(data)%2 == 1 {
			c = append(c, 0)
		}
		return c
	}
	body := append([]byte("WEBP"), chunk("VP8L", append(vp8l, make([]byte, 20)...))...)
	for _, e := range extra {
		body = append(body, e...)
	}
	out := append([]byte("RIFF"), 0, 0, 0, 0)
	binary.LittleEndian.PutUint32(out[4:], uint32(len(body)))
	return append(out, body...)
}

func unchanged(t *testing.T, rig *attachRig, post, why string) {
	t.Helper()
	assert.Equal(t, post, rig.post(post), why)
}

func TestAutoAttachLinksAndRewrites(t *testing.T) {
	rig := newAttachRig(t)
	rig.write("shots/run 1.png", pngBytes(t, 8, 8))
	rig.write("report.md", []byte("# Report\nall good\n"))

	out := rig.post("See ![the run](<shots/run 1.png>) and [the report](report.md).")
	assert.Regexp(t, `^See !\[the run\]\(coral://artifacts/[0-9a-f]{64}\) and \[the report\]\(coral://artifacts/[0-9a-f]{64}\)\.$`, out)
	links := coralLinkRe.FindAllStringSubmatch(out, -1)
	require.Len(t, links, 2)
	_, meta := rig.stored("x coral://artifacts/" + links[1][1])
	assert.Equal(t, "text/markdown", meta.MediaType)
	assert.Equal(t, "report.md", meta.Name)
	// The upload record lists it in the agent's Artifacts tab.
	_, err := os.Stat(filepath.Join(rig.coralDir, "artifacts", "uploads", "sid-dev", links[0][1]+".json"))
	assert.NoError(t, err)

	// The same file twice in one post: one artifact, both links rewritten.
	out = rig.post("![a](report.md) [b](report.md) [c](./report.md)")
	got := coralLinkRe.FindAllString(out, -1)
	require.Len(t, got, 3)
	assert.Equal(t, got[0], got[1])
	assert.Equal(t, got[0], got[2])

	// Bare paths in prose and in code spans are discussion, not links: untouched.
	unchanged(t, rig, "I updated report.md and `shots/run 1.png`, see "+filepath.Join(rig.wd, "report.md"), "bare paths")
	// URLs, other schemes, ~ and anchors are never read.
	unchanged(t, rig, "[a](https://example.test/x.png) [b](coral://artifacts/abc.png) [c](~/x.png) [d](#x.md) [e](file:report.md)", "not local paths")
}

func TestAutoAttachBounds(t *testing.T) {
	rig := newAttachRig(t)
	outside := realPath(t.TempDir())
	rig.write(filepath.Join(outside, "out.png"), pngBytes(t, 4, 4))
	linktest.Symlink(t, filepath.Join(outside, "out.png"), filepath.Join(rig.wd, "link.png"))
	rig.write(".hidden/a.png", pngBytes(t, 4, 4))
	rig.write(".a.png", pngBytes(t, 4, 4))
	rig.write("my-secret.png", pngBytes(t, 4, 4))
	rig.write("api_token.md", []byte("x"))
	rig.write("id_rsa.png", pngBytes(t, 4, 4))
	rig.write(filepath.Join(rig.git, "docs", "plan.md"), []byte("# plan"))
	rig.write(filepath.Join(rig.git, "docs", "diagram.png"), pngBytes(t, 4, 4))
	old := rig.write("old.png", pngBytes(t, 4, 4))
	require.NoError(t, os.Chtimes(old, time.Now().Add(-25*time.Hour), time.Now().Add(-25*time.Hour)))
	rig.write("jpeg-named.png", jpegBytes(t))
	rig.write("png-named.gif", pngBytes(t, 4, 4))
	rig.write("drawing.png", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`))
	rig.write("huge.png", append(pngBytes(t, 4, 4), make([]byte, attachMaxImageBytes)...))
	rig.write("wide.png", pngBytes(t, attachMaxSide+1, 1))
	rig.write("latin1.md", []byte("caf\xe9"))
	rig.write("big.md", bytes.Repeat([]byte("a"), attachMaxMarkdown+1))
	rig.write("meta.webp", webpLossless(4, 4, append([]byte("EXIF\x04\x00\x00\x00"), "GPS!"...)))

	for _, c := range []struct{ link, why string }{
		{"![x](" + filepath.Join(outside, "out.png") + ")", "outside the roots"},
		{"![x](link.png)", "a symlink out of the roots"},
		{"![x](../../" + filepath.Base(outside) + "/out.png)", "dot-dot out of the roots"},
		{"![x](.hidden/a.png)", "a dot-directory"},
		{"![x](.a.png)", "a dotfile"},
		{"![x](my-secret.png)", "a secret-looking name"},
		{"[x](api_token.md)", "a token-looking name"},
		{"![x](id_rsa.png)", "sensitivePath"},
		{"[x](../docs/plan.md)", "Markdown outside the working directory (git root only)"},
		{"![x](old.png)", "not modified in the last day"},
		{"![x](jpeg-named.png)", "magic bytes don't match the extension"},
		{"![x](png-named.gif)", "PNG bytes under a GIF name"},
		{"![x](drawing.png)", "SVG (text) under an image name"},
		{"![x](huge.png)", "over the image size cap"},
		{"![x](wide.png)", "over the pixel cap"},
		{"[x](latin1.md)", "Markdown that isn't UTF-8"},
		{"[x](big.md)", "over the Markdown size cap"},
		{"![x](meta.webp)", "WebP with an EXIF chunk"},
		{"![x](missing.png)", "no such file"},
	} {
		unchanged(t, rig, "see "+c.link, c.why)
	}
	// Images may come from the git root, Markdown may not.
	assert.Regexp(t, coralLinkRe, rig.post("![x](../docs/diagram.png)"), "an image in the git root")
	// A plain WebP is fine.
	rig.write("ok.webp", webpLossless(4, 4))
	assert.Regexp(t, coralLinkRe, rig.post("![x](ok.webp)"))
	// GIF too.
	var g bytes.Buffer
	require.NoError(t, gif.Encode(&g, image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.Black, color.White}), nil))
	rig.write("a.gif", g.Bytes())
	assert.Regexp(t, coralLinkRe, rig.post("![x](a.gif)"))
}

func TestAutoAttachRaces(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks: Unix")
	}
	t.Cleanup(func() { attachTestHook = nil })
	rig := newAttachRig(t)
	outside := realPath(t.TempDir())
	secret := rig.write(filepath.Join(outside, "shot.png"), pngBytes(t, 6, 6))

	// The file is swapped for a symlink after the checks, before the open: O_NOFOLLOW.
	p := rig.write("shot.png", pngBytes(t, 4, 4))
	attachTestHook = func(stage, path string) {
		if stage == "before_open" {
			require.NoError(t, os.Remove(path))
			require.NoError(t, os.Symlink(secret, path))
		}
	}
	unchanged(t, rig, "![x](shot.png)", "a symlink swapped in before the open")

	// A parent directory is swapped for a symlink out of the roots: the open itself refuses it
	// (#3601, W8: opened through a root held on the attach root), so the outside file is never
	// read; the re-resolve after the read stays as a second check.
	require.NoError(t, os.Remove(p))
	rig.write("sub/shot.png", pngBytes(t, 4, 4))
	readOutside := false
	attachTestHook = func(stage, path string) {
		if stage == "before_open" {
			require.NoError(t, os.Rename(filepath.Join(rig.wd, "sub"), filepath.Join(rig.wd, "sub-moved")))
			require.NoError(t, os.Symlink(outside, filepath.Join(rig.wd, "sub")))
		}
		if stage == "after_stat" {
			readOutside = true
		}
	}
	unchanged(t, rig, "![x](sub/shot.png)", "a parent directory swapped for a symlink")
	assert.False(t, readOutside, "the swapped folder's file was opened")

	// The file grows while it is read (Markdown: still valid after the append).
	rig.write("grow.md", []byte("# log\n"))
	attachTestHook = func(stage, path string) {
		if stage == "after_stat" {
			f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
			require.NoError(t, err)
			_, _ = f.WriteString("more lines\n")
			f.Close()
		}
	}
	unchanged(t, rig, "[x](grow.md)", "a file that grew while read")
	attachTestHook = nil
	assert.Regexp(t, coralLinkRe, rig.post("[x](grow.md)"), "the same file, settled")
}

func TestAutoAttachStripsMetadata(t *testing.T) {
	rig := newAttachRig(t)
	j := jpegBytes(t)
	app1 := append([]byte{0xFF, 0xE1, 0, 0}, []byte("Exif\x00\x00GPS-LAT-51.5")...)
	binary.BigEndian.PutUint16(app1[2:], uint16(len(app1)-2))
	app13 := append([]byte{0xFF, 0xED, 0, 0}, []byte("Photoshop 3.0\x00IPTC-BYLINE")...)
	binary.BigEndian.PutUint16(app13[2:], uint16(len(app13)-2))
	com := append([]byte{0xFF, 0xFE, 0, 0}, []byte("COMMENT-BY-CAMERA")...)
	binary.BigEndian.PutUint16(com[2:], uint16(len(com)-2))
	withExif := append(append(append(append([]byte{0xFF, 0xD8}, app1...), app13...), com...), j[2:]...)
	rig.write("photo.jpg", withExif)
	data, meta := rig.stored(rig.post("![x](photo.jpg)"))
	assert.Equal(t, "image/jpeg", meta.MediaType)
	assert.NotContains(t, string(data), "GPS-LAT", "APP1 (EXIF/GPS) dropped")
	assert.NotContains(t, string(data), "IPTC-BYLINE", "APP13 (IPTC) dropped")
	assert.NotContains(t, string(data), "COMMENT-BY-CAMERA", "COM dropped")
	_, err := jpeg.DecodeConfig(bytes.NewReader(data))
	assert.NoError(t, err, "still a JPEG")

	p := pngBytes(t, 4, 4)
	text := []byte{0, 0, 0, 0}
	binary.BigEndian.PutUint32(text, uint32(len("Comment\x00GPS-LAT-51.5")))
	text = append(append(text, "tEXt"...), "Comment\x00GPS-LAT-51.5"...)
	text = append(text, 0, 0, 0, 0) // CRC (not checked by the decoder config)
	priv := []byte{0, 0, 0, 0}
	binary.BigEndian.PutUint32(priv, uint32(len("OWNER-SECRET")))
	priv = append(append(append(priv, "prVt"...), "OWNER-SECRET"...), 0, 0, 0, 0)
	withText := append(append(append(append([]byte{}, p[:33]...), text...), priv...), p[33:]...)
	rig.write("plot.png", withText)
	data, _ = rig.stored(rig.post("![x](plot.png)"))
	assert.NotContains(t, string(data), "GPS-LAT", "tEXt dropped")
	assert.NotContains(t, string(data), "OWNER-SECRET", "a private chunk dropped")
	_, err = png.DecodeConfig(bytes.NewReader(data))
	assert.NoError(t, err, "still a PNG")
}

func TestAutoAttachCapsAndSwitch(t *testing.T) {
	rig := newAttachRig(t)
	var links []string
	for i := 0; i < attachMaxPerMessage+1; i++ {
		name := "f" + string(rune('a'+i)) + ".png"
		rig.write(name, pngBytes(t, 4+i, 4))
		links = append(links, "![x]("+name+")")
	}
	out := rig.post(strings.Join(links, " "))
	assert.Len(t, coralLinkRe.FindAllString(out, -1), attachMaxPerMessage, "at most %d files a message", attachMaxPerMessage)
	assert.Contains(t, out, links[attachMaxPerMessage], "the rest stay as written")

	// The switch off: nothing is read.
	rig.on = false
	unchanged(t, rig, "![x](fa.png)", "the Operator's switch is off")
	rig.on = true

	// The agent's hourly cap counts refused attempts too, then one warning line.
	rig2 := newAttachRig(t)
	for i := 0; i < attachWindowAttempts; i++ {
		rig2.post("![x](missing.png)")
	}
	rig2.write("ok.png", pngBytes(t, 4, 4))
	out = rig2.post("![x](ok.png)")
	assert.NotRegexp(t, coralLinkRe, out)
	assert.Contains(t, out, "auto-attach is paused")
	assert.NotContains(t, rig2.post("![x](ok.png)"), "auto-attach is paused", "one warning")
	rig2.a.now = func() time.Time { return time.Now().Add(attachWindow + time.Minute) }
	assert.Regexp(t, coralLinkRe, rig2.post("![x](ok.png)"), "a new hour")

	// The per-Coral total, persisted.
	rig3 := newAttachRig(t)
	rig3.write("ok.png", pngBytes(t, 4, 4))
	today := time.Now().UTC().Format("2006-01-02")
	rig3.a.days = map[string]int64{today: attachTotalBytes - 10}
	unchanged(t, rig3, "![x](ok.png)", "the Coral-wide cap")
	rig3.a.days = map[string]int64{}
	assert.Regexp(t, coralLinkRe, rig3.post("![x](ok.png)"))
	again := NewAutoAttacher(rig3.coralDir, nil, nil)
	again.mu.Lock()
	assert.Greater(t, again.windowTotalLocked(), int64(0), "the window survives a restart")
	again.mu.Unlock()

	// A rolling 7 days, not a lifetime (#3554): what was attached 8 days ago no longer counts.
	rig5 := newAttachRig(t)
	rig5.write("ok.png", pngBytes(t, 5, 5))
	rig5.a.days = map[string]int64{time.Now().UTC().AddDate(0, 0, -8).Format("2006-01-02"): attachTotalBytes}
	assert.Regexp(t, coralLinkRe, rig5.post("![x](ok.png)"), "the full cap 8 days ago is out of the window")
	rig5.a.days = map[string]int64{time.Now().UTC().AddDate(0, 0, -6).Format("2006-01-02"): attachTotalBytes}
	rig5.write("ok2.png", pngBytes(t, 6, 6))
	unchanged(t, rig5, "![x](ok2.png)", "6 days ago is still in the window")

	// The file from before #3554 (a lifetime number) starts an empty window.
	rig6 := newAttachRig(t)
	require.NoError(t, os.MkdirAll(filepath.Join(rig6.coralDir, "artifacts"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(rig6.coralDir, "artifacts", "auto-attach-bytes"), []byte("2147483648"), 0o600))
	old := NewAutoAttacher(rig6.coralDir, nil, nil)
	old.mu.Lock()
	assert.Zero(t, old.windowTotalLocked())
	old.mu.Unlock()

	// Storage fails: the link is left as written.
	rig4 := newAttachRig(t)
	rig4.write("ok.png", pngBytes(t, 4, 4))
	require.NoError(t, os.WriteFile(rig4.coralDir, []byte("not a dir"), 0o644))
	unchanged(t, rig4, "![x](ok.png)", "storage failed")
}

func TestAutoAttachNames(t *testing.T) {
	rig := newAttachRig(t)
	rig.write("x](evil.test).md", []byte("# x"))
	rig.write("@Orchestrator.md", []byte("# y"))
	out := rig.post("[a](<x](evil.test).md>) [b](@Orchestrator.md)")
	links := coralLinkRe.FindAllStringSubmatch(out, -1)
	require.Len(t, links, 2, out)
	assert.Equal(t, "[a](coral://artifacts/"+links[0][1]+") [b](coral://artifacts/"+links[1][1]+")", out, "only the destinations change")
	_, m1 := rig.stored("coral://artifacts/" + links[0][1])
	_, m2 := rig.stored("coral://artifacts/" + links[1][1])
	assert.Equal(t, "x__evil.test_.md", m1.Name)
	assert.Equal(t, "_Orchestrator.md", m2.Name)
	assert.Equal(t, strings.Repeat("a", 64), attachName(strings.Repeat("a", 100)))
	assert.Equal(t, "x__https___evil.test_.md", attachName("x](https://evil.test).md"))

	// Windows separators are never read outside Windows (a literal backslash name included).
	if runtime.GOOS != "windows" {
		rig.write(`dir\shot.png`, pngBytes(t, 4, 4))
		unchanged(t, rig, `![x](dir\shot.png)`, "a backslash path")
		unchanged(t, rig, `![x](..\..\shot.png)`, "a backslash traversal")
	}
}

// Security's SHOULD 1: a shared or shallow root is never used: the system temp
// directories themselves and anything with fewer than two path components. A per-project
// folder inside the temp directory still works.
func TestAutoAttachRefusesSharedRoots(t *testing.T) {
	tmp := realPath(os.TempDir())
	assert.False(t, attachRootAllowed(tmp), "the temp dir itself")
	if runtime.GOOS != "windows" {
		assert.False(t, attachRootAllowed("/tmp"))
		assert.False(t, attachRootAllowed(realPath("/tmp")))
		assert.False(t, attachRootAllowed("/var/tmp"))
		assert.False(t, attachRootAllowed("/opt"), "one component")
		assert.True(t, attachRootAllowed("/opt/project"))
	}
	assert.True(t, attachRootAllowed(filepath.Join(tmp, "project")), "a folder inside it")

	// A session working in the temp root itself attaches nothing, even a fresh image there.
	f, err := os.CreateTemp("", "shot-*.png")
	require.NoError(t, err)
	t.Cleanup(func() { os.Remove(f.Name()) })
	_, err = f.Write(pngBytes(t, 4, 4))
	require.NoError(t, err)
	f.Close()
	coralDir := t.TempDir()
	a := NewAutoAttacher(coralDir, nil, func(context.Context, string) (string, string) { return os.TempDir(), "" })
	post := "![x](" + filepath.Base(f.Name()) + ")"
	assert.Equal(t, post, a.Rewrite(context.Background(), "team", "sid", "claude-sid", post), "the temp root")
	// The same file from a working dir one level down (a per-project folder) is fine.
	rig := newAttachRig(t)
	rig.write("shot.png", pngBytes(t, 4, 4))
	assert.Regexp(t, coralLinkRe, rig.post("![x](shot.png)"))
}

func TestAutoAttachForgetsEndedWindows(t *testing.T) {
	rig := newAttachRig(t)
	start := time.Now()
	rig.a.now = func() time.Time { return start }
	for _, s := range []string{"a", "b", "c"} {
		rig.a.admit(s)
	}
	require.Len(t, rig.a.windows, 3)
	rig.a.now = func() time.Time { return start.Add(attachWindow + time.Second) }
	rig.a.admit("d")
	assert.Len(t, rig.a.windows, 1, "ended windows are forgotten")
}

// coral-hub Security's #3515 v2 SHOULDs: JPEG keeps a whitelist of segments and ends at
// the EOI; PNG ends at IEND; GIF drops comment and application extensions and ends at the
// trailer. Each output still decodes in full.
func TestAutoAttachImageWhitelists(t *testing.T) {
	rig := newAttachRig(t)
	seg := func(marker byte, payload string) []byte {
		b := []byte{0xFF, marker, 0, 0}
		binary.BigEndian.PutUint16(b[2:], uint16(len(payload)+2))
		return append(b, payload...)
	}
	j := jpegBytes(t)
	var withAll []byte
	withAll = append(withAll, 0xFF, 0xD8)
	withAll = append(withAll, seg(0xE2, "ICC_PROFILE\x00\x01\x01ICC-KEPT")...)
	withAll = append(withAll, seg(0xE2, "MPF\x00MPF-THUMB")...)
	withAll = append(withAll, seg(0xEB, "JP\x00\x00C2PA-MANIFEST")...)
	withAll = append(withAll, seg(0xEF, "APP15-DATA")...)
	withAll = append(withAll, j[2:]...)
	withAll = append(withAll, "APPENDED-ZIP-AFTER-EOI"...)
	rig.write("w.jpg", withAll)
	data, _ := rig.stored(rig.post("![x](w.jpg)"))
	for _, gone := range []string{"MPF-THUMB", "C2PA-MANIFEST", "APP15-DATA", "APPENDED-ZIP"} {
		assert.NotContains(t, string(data), gone)
	}
	assert.Contains(t, string(data), "ICC-KEPT", "an ICC profile is kept")
	assert.Equal(t, []byte{0xFF, 0xD9}, data[len(data)-2:], "ends at the EOI")
	_, err := jpeg.Decode(bytes.NewReader(data))
	assert.NoError(t, err)

	// A well-formed chunk after IEND (an IDAT, which the allow-list would otherwise keep).
	after := []byte{0, 0, 0, 0}
	binary.BigEndian.PutUint32(after, uint32(len("AFTER-IEND-PAYLOAD")))
	after = append(append(append(after, "IDAT"...), "AFTER-IEND-PAYLOAD"...), 0, 0, 0, 0)
	p := append(pngBytes(t, 4, 4), after...)
	rig.write("after.png", p)
	data, _ = rig.stored(rig.post("![x](after.png)"))
	assert.NotContains(t, string(data), "AFTER-IEND")
	_, err = png.Decode(bytes.NewReader(data))
	assert.NoError(t, err)

	var g bytes.Buffer
	pal := image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.Black, color.White})
	require.NoError(t, gif.EncodeAll(&g, &gif.GIF{Image: []*image.Paletted{pal, pal}, Delay: []int{1, 1}, LoopCount: 0}))
	gb := g.Bytes()
	require.Contains(t, string(gb), "NETSCAPE2.0", "the encoder wrote an application extension")
	comment := []byte("\x21\xFE\x0bGIF-COMMENT\x00")
	withC := append(append(append([]byte{}, gb[:len(gb)-1]...), comment...), 0x3B)
	withC = append(withC, "GIF-TRAILING"...)
	rig.write("anim.gif", withC)
	data, _ = rig.stored(rig.post("![x](anim.gif)"))
	for _, gone := range []string{"GIF-COMMENT", "NETSCAPE2.0", "GIF-TRAILING"} {
		assert.NotContains(t, string(data), gone)
	}
	out, err := gif.DecodeAll(bytes.NewReader(data))
	require.NoError(t, err)
	assert.Len(t, out.Image, 2, "both frames kept")

	// No EOI, no IEND, no trailer: refused.
	rig.write("cut.jpg", j[:len(j)-2])
	unchanged(t, rig, "![x](cut.jpg)", "a JPEG without its EOI")
	pp := pngBytes(t, 4, 4)
	rig.write("cut.png", pp[:len(pp)-12])
	unchanged(t, rig, "![x](cut.png)", "a PNG without IEND")
	rig.write("cut.gif", gb[:len(gb)-1])
	unchanged(t, rig, "![x](cut.gif)", "a GIF without its trailer")
}

// #3563: the switch is off unless set to a true value; a JSON true is stored as "True".
func TestAutoAttachOnlyWhenTurnedOn(t *testing.T) {
	for v, want := range map[string]bool{
		"": false, " ": false, "false": false, "False": false, "0": false, "off": false, "disabled": false,
		"true": true, "True": true, "1": true, "on": true, "yes": true, "enabled": false, // a value that is no boolean word stays off (#5147)
	} {
		assert.Equal(t, want, setreg.Bool(map[string]string{AutoAttachSetting: v}, AutoAttachSetting), "%q", v)
	}
}
