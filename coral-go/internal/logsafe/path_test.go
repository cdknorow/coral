package logsafe

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTheHomeFolderIsWrittenAsATildeAndNothingElseIs(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "people", "someone")
	for in, want := range map[string]string{
		home:                                 "~",
		filepath.Join(home, "go", "bin"):     filepath.Join("~", "go", "bin"),
		home + "-other":                      home + "-other", // a sibling that starts with the same letters is not the home folder
		filepath.Join("/", "usr", "bin"):     filepath.Join("/", "usr", "bin"),
		"":                                   "",
		filepath.Join(home, ".local", "bin"): filepath.Join("~", ".local", "bin"),
		strings.TrimSuffix(home, "someone"):  strings.TrimSuffix(home, "someone"), // a parent of the home folder stays
	} {
		assert.Equal(t, want, HomeRelative(in, home), in)
	}
	assert.Equal(t, "x", HomeRelative("x", ""), "no home folder known: unchanged")
}

func TestThePathSummaryCountsEveryEntryAndNamesNoHomeFolder(t *testing.T) {
	home := filepath.Join(t.TempDir(), "profile-folder-marker")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	sep := string(filepath.ListSeparator)
	t.Setenv("PATH", strings.Join([]string{filepath.Join(home, "go", "bin"), "", filepath.Join("/", "usr", "bin"), home}, sep))
	assert.Equal(t, 3, Count(), "an empty entry is not a folder")
	s := PathSummary()
	assert.Contains(t, s, "path_entries=3")
	assert.NotContains(t, s, "profile-folder-marker", "the home folder is written as ~")
	assert.Contains(t, s, "~")
	assert.Contains(t, s, filepath.Join("/", "usr", "bin"), "other folders are named")
}

func TestAnEmptyPathIsZeroEntries(t *testing.T) {
	t.Setenv("PATH", "")
	assert.Equal(t, 0, Count())
	assert.Equal(t, "path_entries=0 path=", PathSummary())
}

// The census: no line of Coral's own Go code (tests apart) writes the PATH into a log. The one place it is logged is logsafe.PathSummary, at server start; a new log line that reads the PATH
// fails here (#6018).
func TestNoLogLineCarriesThePath(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	var bad []string
	require.NoError(t, filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for n := 1; sc.Scan(); n++ {
			l := sc.Text()
			if !strings.Contains(l, `Getenv("PATH")`) {
				continue
			}
			t := strings.TrimSpace(l)
			if strings.HasPrefix(t, "//") {
				continue
			}
			if strings.Contains(l, "log.") || strings.Contains(l, "slog.") || strings.Contains(l, `"path",`) || strings.Contains(l, "path=%") || strings.Contains(l, "Printf") {
				rel, _ := filepath.Rel(root, p)
				bad = append(bad, rel+":"+itoa(n)+": "+t)
			}
		}
		return sc.Err()
	}))
	assert.Empty(t, bad, "a log line reads the PATH: use logsafe.Count (or PathSummary once at server start)")
}
