package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The default port differs by platform (defaults_unix.go, defaults_windows.go: Windows avoids svchost's 8420), and the docs a first-time person reads say
// which is which: a Windows reader who follows "Open http://localhost:8420" gets nothing (#6004's W8). The test reads the two constants from their source,
// so a change of either, or a doc that stops saying so, fails here.
func TestTheDocsSayWhichPortEachPlatformListensOn(t *testing.T) {
	re := regexp.MustCompile(`const DefaultPort = (\d+)`)
	port := func(file string) string {
		b, err := os.ReadFile(file)
		require.NoError(t, err)
		m := re.FindSubmatch(b)
		require.NotNil(t, m, file)
		return string(m[1])
	}
	unix, windows := port("defaults_unix.go"), port("defaults_windows.go")
	require.NotEqual(t, unix, windows, "if the platforms agree, the Windows notes in the docs are not needed: remove them with this test")
	for _, doc := range []string{filepath.Join("..", "..", "..", "README.md"), filepath.Join("..", "..", "agent_docs", "quickstart.md")} {
		b, err := os.ReadFile(doc)
		require.NoError(t, err, doc)
		text := strings.ReplaceAll(string(b), "\r\n", "\n")
		assert.Contains(t, text, "localhost:"+unix, "%s names the default port", doc)
		assert.Regexp(t, `(?is)windows.{0,160}`+windows+`|`+windows+`.{0,160}windows`, text, "%s says Windows listens on %s", doc, windows)
	}
}
