package testguard

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The relaunch test seam (#5452) is a compile-time difference only: the relaunchfixture build tag turns the relaunch routes on and puts a fake updater behind them. These tests
// hold, in the default build, that nothing of it can be compiled into a build without the tag: every file that reads CORAL_RELAUNCH_FIXTURE_DIR or names the fixture types
// carries the tag, and no tier of a release build (default, dev, beta, dropboxers) compiles a fixture file into any of the programs the seam touches.

var fixtureMarkers = []string{"CORAL_RELAUNCH_FIXTURE_DIR", "fixtureUpdater", "CheckRelaunchFixture", "\"+relaunchfixture\""}

func TestEveryFileOfTheRelaunchSeamCarriesItsBuildTag(t *testing.T) {
	root := filepath.Join("..", "..") // coral-go
	found := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); path != root && (strings.HasPrefix(n, ".") || n == "node_modules" || n == "testdata" || n == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil // the seam's own tests are tagged files too, but the rule is about what a build compiles
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		src := string(b)
		for _, m := range fixtureMarkers {
			if strings.Contains(src, m) {
				found++
				head := src
				if i := strings.Index(head, "\npackage "); i >= 0 {
					head = head[:i]
				}
				assert.Regexp(t, `(?m)^//go:build [^\n]*\brelaunchfixture\b`, head, "%s mentions %s and must carry the relaunchfixture build tag", path, m)
				assert.NotRegexp(t, `(?m)^//go:build [^\n]*!relaunchfixture`, head, "%s is the default build's file and must not name the seam", path)
				break
			}
		}
		return nil
	})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, found, 4, "the seam's files were found")
}

// seamFiles are the files that exist only in a relaunchfixture build.
var seamFiles = map[string]bool{
	"relaunch_fixture.go": true, "relaunch_feature_fixture.go": true, "relaunch_fixture_unix.go": true, "relaunch_fixture_windows.go": true,
	"relaunch_fixture_on.go": true, "flavor_relaunchfixture.go": true,
}

func TestNoTierOfAReleaseBuildCompilesTheRelaunchSeam(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go tool")
	}
	for _, tags := range []string{"", "dev", "beta", "dropboxers", "dev,fts5", "sqlcipher,fts5"} {
		cmd := exec.Command("go", "list", "-tags", tags, "-f", "{{.ImportPath}} {{join .GoFiles \" \"}} {{join .TestGoFiles \" \"}}", "./cmd/coral", "./internal/config", "./internal/server/routes")
		cmd.Dir = filepath.Join("..", "..")
		out, err := cmd.Output()
		require.NoError(t, err, "tags=%q", tags)
		for _, file := range strings.Fields(string(out)) {
			assert.False(t, seamFiles[file], "tags=%q compiles the seam's file %s", tags, file)
		}
	}
	// and with the tag, the files are there (the check above could pass because nothing is ever found)
	cmd := exec.Command("go", "list", "-tags", "relaunchfixture", "-f", "{{join .GoFiles \" \"}}", "./internal/server/routes")
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.Output()
	require.NoError(t, err)
	assert.Contains(t, string(out), "relaunch_fixture.go")
	assert.Contains(t, string(out), "relaunch_feature_fixture.go")
}
