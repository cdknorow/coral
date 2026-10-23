package stripstatic

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/pkg/frontend"
)

var realStatic = filepath.Join("..", "..", "pkg", "frontend", "static")

// The overlay ships exactly what the server serves: every replaced file is MinifyAsset of its source, and every JS and CSS file the overlay leaves alone is one MinifyAsset leaves as it is (so a file cannot
// be forgotten or changed by something else). Runs in the default test set: it is the proof that the release's bytes are the served bytes.
func TestOverlayIsMinifyAssetFileByFile(t *testing.T) {
	out := t.TempDir()
	res, err := Generate(realStatic, out)
	require.NoError(t, err)
	require.NoError(t, Validate(res.Overlay, realStatic))
	raw, err := os.ReadFile(res.Overlay)
	require.NoError(t, err)
	var ov overlayJSON
	require.NoError(t, json.Unmarshal(raw, &ov))
	abs, _ := filepath.Abs(realStatic)
	replaced := map[string]bool{}
	for src, dst := range ov.Replace {
		rel, err := filepath.Rel(abs, src)
		require.NoError(t, err)
		rel = filepath.ToSlash(rel)
		replaced[rel] = true
		source, err := os.ReadFile(src)
		require.NoError(t, err)
		got, err := os.ReadFile(dst)
		require.NoError(t, err)
		assert.Equal(t, string(frontend.MinifyAsset(rel, source)), string(got), "%s is not what the server serves", rel)
		assert.Less(t, len(got), len(source), "%s did not shrink", rel)
	}
	require.NoError(t, filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(abs, p)
		rel = filepath.ToSlash(rel)
		if replaced[rel] {
			return nil
		}
		data, _ := os.ReadFile(p)
		assert.Equal(t, string(data), string(frontend.MinifyAsset(rel, data)), "%s is minified by the server but not in the overlay", rel)
		assert.False(t, minified(rel) && len(frontend.MinifyAsset(rel, data)) < len(data), "%s", rel)
		return nil
	}))
	assert.Equal(t, res.Files, len(ov.Replace))
	assert.Greater(t, res.SourceBytes-res.StrippedBytes, int64(500_000), "the strip saves hundreds of KB: %+v", res)
	// everything it wrote is inside its own folder
	for _, dst := range ov.Replace {
		assert.True(t, strings.HasPrefix(dst, out), dst)
	}
}

func writeOverlay(t *testing.T, dir string, replace map[string]string) string {
	t.Helper()
	b, err := json.Marshal(overlayJSON{Replace: replace})
	require.NoError(t, err)
	p := filepath.Join(dir, File)
	require.NoError(t, os.WriteFile(p, b, 0o600))
	return p
}

// An overlay can replace any file the compiler reads, Go sources included: Validate must refuse every key but a JS or CSS file of the static tree, and every target outside the overlay's folder (Security, #6208).
func TestValidateRefusesAnythingButStaticJSAndCSS(t *testing.T) {
	abs, _ := filepath.Abs(realStatic)
	dir := t.TempDir()
	target := filepath.Join(dir, "x.js")
	require.NoError(t, os.WriteFile(target, []byte("x"), 0o600))
	goFile, _ := filepath.Abs(filepath.Join("..", "..", "internal", "stripstatic", "stripstatic.go"))
	other := filepath.Join(t.TempDir(), "elsewhere.js")
	require.NoError(t, os.WriteFile(other, []byte("x"), 0o600))
	ok := filepath.Join(abs, "app.js")
	for name, replace := range map[string]map[string]string{
		"a Go file":                 {goFile: target},
		"a Go file in the frontend": {filepath.Join(abs, "..", "frontend.go"): target},
		"a dotdot key":              {filepath.Join(abs, "sub", "..", "..", "frontend.go"): target},
		"a relative key":            {"app.js": target},
		"outside the static tree":   {filepath.Join(filepath.Dir(abs), "app.js"): target},
		"an html file":              {filepath.Join(abs, "x.html"): target},
		"vendor":                    {filepath.Join(abs, "vendor", "marked.min.js"): target},
		"agent docs":                {filepath.Join(abs, "agent_docs", "x.js"): target},
		"a target elsewhere":        {ok: other},
		"a relative target":         {ok: "x.js"},
		"a dotdot target":           {ok: filepath.Join(dir, "..", "x.js")},
		"a target that is not":      {ok: filepath.Join(dir, "missing.js")},
		"nothing":                   {},
	} {
		p := writeOverlay(t, dir, replace)
		assert.Error(t, Validate(p, realStatic), name)
	}
	assert.NoError(t, Validate(writeOverlay(t, dir, map[string]string{ok: target}), realStatic), "a JS file of the static tree to a file in the overlay's folder is fine")
}

func TestGenerateRefusesToShipNothingOrWithMinifyOff(t *testing.T) {
	src := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(src, "a.js"), []byte("a=1;"), 0o600)) // already as small as it gets
	_, err := Generate(src, t.TempDir())
	assert.ErrorContains(t, err, "nothing under")
	t.Setenv("CORAL_NO_MINIFY", "1")
	_, err = Generate(realStatic, t.TempDir())
	assert.ErrorContains(t, err, "CORAL_NO_MINIFY")
}
