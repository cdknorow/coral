package routes

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The versions of the bundled themes shipped before the accessibility pass: the current ones with
// the old --text-muted back.
var oldTextMuted = map[string][2]string{"Dark": {"#8e8e95", "#626268"}, "Obsidian": {"#868692", "#52525b"}}

func oldBundledTheme(t *testing.T, name string) string {
	t.Helper()
	sw := oldTextMuted[name]
	cur := bundledThemes[name]
	require.Equal(t, 1, strings.Count(cur, `"--text-muted": "`+sw[0]+`"`), name)
	return strings.Replace(cur, `"--text-muted": "`+sw[0]+`"`, `"--text-muted": "`+sw[1]+`"`, 1)
}

// Seeding updates a bundled theme the user never edited (byte for byte a shipped version) and
// leaves one the user changed alone; a missing one is written, a current one stays as it is.
func TestSeedBundledThemesUpdatesOnlyUneditedShippedVersions(t *testing.T) {
	dir := t.TempDir()
	h := &ThemesHandler{themesDir: dir}
	require.NoError(t, os.MkdirAll(dir, 0755))

	oldDark := oldBundledTheme(t, "Dark")
	assert.True(t, wasShippedVersion("Dark", []byte(oldDark)), "the recorded hash is the old Dark")
	assert.True(t, wasShippedVersion("Obsidian", []byte(oldBundledTheme(t, "Obsidian"))), "and the old Obsidian")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dark.json"), []byte(oldDark), 0644))
	edited := strings.Replace(oldBundledTheme(t, "Obsidian"), `"--accent": "#a78bfa"`, `"--accent": "#ff00ff"`, 1)
	require.NotEqual(t, oldBundledTheme(t, "Obsidian"), edited, "the edit applied")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Obsidian.json"), []byte(edited), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "GhostV3.json"), []byte(bundledThemes["GhostV3"]), 0644))

	h.SeedBundledThemes()

	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(dir, name+".json"))
		require.NoError(t, err)
		return string(b)
	}
	assert.Equal(t, bundledThemes["Dark"], read("Dark"), "an unedited old Dark is updated")
	assert.Equal(t, edited, read("Obsidian"), "a theme the user edited is never touched")
	assert.Equal(t, bundledThemes["GhostV3"], read("GhostV3"), "a current one stays")

	require.NoError(t, os.Remove(filepath.Join(dir, "Dark.json")))
	h.SeedBundledThemes()
	assert.Equal(t, bundledThemes["Dark"], read("Dark"), "a missing one is written")
}

// Dark's and Obsidian's muted text is at least 4.5:1 on their panels (WCAG AA).
func TestBundledThemesMutedTextContrast(t *testing.T) {
	lum := func(hex string) float64 {
		var c [3]float64
		for i := 0; i < 3; i++ {
			v, err := strconv.ParseUint(hex[1+2*i:3+2*i], 16, 8)
			require.NoError(t, err, hex)
			x := float64(v) / 255
			if x <= 0.03928 {
				c[i] = x / 12.92
			} else {
				c[i] = math.Pow((x+0.055)/1.055, 2.4)
			}
		}
		return 0.2126*c[0] + 0.7152*c[1] + 0.0722*c[2]
	}
	ratio := func(a, b string) float64 {
		x, y := lum(a), lum(b)
		if y > x {
			x, y = y, x
		}
		return (x + 0.05) / (y + 0.05)
	}
	for _, name := range []string{"Dark", "Obsidian"} {
		var th struct {
			Variables map[string]string `json:"variables"`
		}
		require.NoError(t, json.Unmarshal([]byte(bundledThemes[name]), &th), name)
		muted := th.Variables["--text-muted"]
		for _, bg := range []string{"--bg-primary", "--bg-secondary", "--bg-tertiary", "--bg-elevated"} {
			r := ratio(muted, th.Variables[bg])
			assert.GreaterOrEqualf(t, r, 4.5, "%s: --text-muted %s on %s %s is %.2f:1", name, muted, bg, th.Variables[bg], r)
		}
	}
}
