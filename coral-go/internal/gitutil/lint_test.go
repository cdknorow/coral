package gitutil

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Every git Coral runs goes through Command or Args (#3579). This fails when code starts
// git another way: exec.Command("git", ...), exec.CommandContext(ctx, "git", ...) or
// executil.Command(ctx, "git", ...), or when code asks git for textconv or filters.
// (A runner given git as a variable, like board.runBoundedCommand, builds its arguments
// with Args.)
var rawGit = regexp.MustCompile(`(?:exec\.Command|exec\.CommandContext|executil\.Command)\((?:[^()"]*,\s*)?"git"`)

// textconvAsked: an argument that asks git to run textconv or filter drivers (cat-file
// --textconv or --filters, diff/log/show --textconv). Command can't turn those off.
var textconvAsked = regexp.MustCompile(`"--(?:textconv|filters)(?:=[^"]*)?"`)

// allowedRawGit: the helper itself.
var allowedRawGit = map[string]bool{
	"internal/gitutil/command.go": true,
}

func TestGitRunsOnlyThroughCommand(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("module root not found at %s", root)
	}
	var bad []string
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".")) && p != root {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if allowedRawGit[rel] {
			return nil
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(src), "\n") {
			if lintFlags(line) {
				bad = append(bad, rel+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
			}
		}
		return nil
	})
	if len(bad) > 0 {
		t.Errorf("git started without gitutil.Command (a repository's config could run commands):\n%s", strings.Join(bad, "\n"))
	}
}

// lintFlags: the line starts git without the helper, or asks it for textconv or filters.
func lintFlags(line string) bool {
	return rawGit.MatchString(line) || textconvAsked.MatchString(line)
}

func TestRawGitPatternCatchesTheForms(t *testing.T) {
	for _, line := range []string{
		`cmd := exec.Command("git", "status")`,
		`exec.CommandContext(ctx, "git", "-C", dir, "diff")`,
		`out, err := executil.Command(ctx, "git", args...).Output()`,
	} {
		if !lintFlags(line) {
			t.Errorf("not caught: %s", line)
		}
	}
	if lintFlags(`gitutil.Command(ctx, dir, "status")`) {
		t.Error("the helper itself is flagged")
	}
	for _, line := range []string{
		`gitutil.Command(ctx, dir, "cat-file", "--textconv", rev)`,
		`gitutil.Command(ctx, dir, "cat-file", "--filters", "--path=x", rev)`,
		`args = append(args, "--textconv")`,
	} {
		if !lintFlags(line) {
			t.Errorf("not caught: %s", line)
		}
	}
	if lintFlags(`out = append(out, "--no-ext-diff", "--no-textconv")`) {
		t.Error("--no-textconv is flagged")
	}
}
