package gitutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// hostileRepo makes a repository whose config names a command for every way git can be told
// to run one during what Coral runs (#3579). Each command appends its name to the marker.
func hostileRepo(t *testing.T) (repo, marker string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the marker commands are shell scripts")
	}
	if !Available() {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	repo = filepath.Join(root, "repo")
	marker = filepath.Join(root, "marker")
	script := func(name string) string {
		p := filepath.Join(root, name+".sh")
		// Filters and textconv must still pass the content through.
		body := "#!/bin/sh\necho " + name + " >> '" + marker + "'\ncat\n"
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+root)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git("init", "-q", "-b", "main")
	git("config", "user.email", "t@example.invalid")
	git("config", "user.name", "t")
	os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("*.txt filter=evil diff=evil\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("hi\n"), 0o644)
	git("add", "-A")
	git("commit", "-qm", "init")
	// A signed-looking commit: log.showSignature makes git run gpg.program on it.
	tree := strings.TrimSpace(gitOut(t, repo, "rev-parse", "HEAD^{tree}"))
	parent := strings.TrimSpace(gitOut(t, repo, "rev-parse", "HEAD"))
	commit := "tree " + tree + "\nparent " + parent +
		"\nauthor t <t@example.invalid> 1700000000 +0000\ncommitter t <t@example.invalid> 1700000000 +0000\n" +
		"gpgsig -----BEGIN PGP SIGNATURE-----\n \n iQEzBAABCAAdFiEE\n -----END PGP SIGNATURE-----\n\nsigned\n"
	obj := filepath.Join(root, "commit.txt")
	os.WriteFile(obj, []byte(commit), 0o644)
	signed := strings.TrimSpace(gitOut(t, repo, "hash-object", "-t", "commit", "-w", obj))
	git("update-ref", "refs/heads/main", signed)
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("hi\nmore\n"), 0o644)

	for key, name := range map[string]string{
		"core.fsmonitor":      "fsmonitor",
		"filter.evil.clean":   "clean",
		"filter.evil.smudge":  "smudge",
		"diff.evil.textconv":  "textconv",
		"diff.evil.command":   "diffcommand",
		"diff.external":       "external",
		"gpg.program":         "gpg",
		"core.pager":          "pager",
		"core.sshCommand":     "ssh",
		"filter.lfs.process":  "lfsprocess", // not git-lfs's own driver: emptied too
		"filter.other.clean":  "otherclean",
		"filter.Mixed.smudge": "mixedsmudge",
	} {
		git("config", key, script(name))
	}
	git("config", "log.showSignature", "true")
	hooks := filepath.Join(repo, ".git", "hooks")
	for _, h := range []string{"post-checkout", "reference-transaction", "post-index-change", "pre-auto-gc"} {
		os.WriteFile(filepath.Join(hooks, h), []byte("#!/bin/sh\necho hook-"+h+" >> '"+marker+"'\n"), 0o755)
	}
	return repo, marker
}

func gitOut(t *testing.T, repo string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

func ran(marker string) string {
	b, _ := os.ReadFile(marker)
	return strings.Join(strings.Fields(string(b)), ",")
}

// What Coral runs, through Command: none of the repository's commands run.
func TestCommandRunsNoRepositoryCommand(t *testing.T) {
	repo, marker := hostileRepo(t)
	ctx := context.Background()
	wt := filepath.Join(t.TempDir(), "wt")
	steps := [][]string{
		{"status", "--porcelain", "-z", "--untracked-files=normal"},
		{"diff", "HEAD", "--numstat", "-z"},
		{"diff", "--binary", "HEAD", "--"},
		{"--literal-pathspecs", "diff", "HEAD", "--", "a.txt"},
		{"diff", "--no-index", "--", os.DevNull, "a.txt"},
		{"log", "-1", "--format=%H|%s|%aI"},
		{"show", "HEAD:a.txt"},
		{"ls-files", "--cached", "--others", "--exclude-standard"},
		{"rev-parse", "--abbrev-ref", "HEAD"},
		{"worktree", "add", "-b", "coral-x", wt, "main"},
		{"worktree", "remove", "--force", wt},
		{"branch", "-D", "coral-x"},
	}
	for _, args := range steps {
		os.Remove(marker)
		out, err := Command(ctx, repo, args...).CombinedOutput()
		if err != nil && !(args[0] == "diff" && args[1] == "--no-index") { // --no-index exits 1 on a difference
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		if got := ran(marker); got != "" {
			t.Errorf("git %v ran the repository's %s", args, got)
		}
	}
	// The worktree was checked out with the file's content as it is (no smudge filter).
	if _, err := os.Stat(wt); err == nil {
		t.Errorf("worktree remove left %s", wt)
	}
}

// The control: the same repository runs its commands under a plain git, so the test above
// proves something.
func TestHostileRepoRunsCommandsWithoutTheGuard(t *testing.T) {
	repo, marker := hostileRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"status", "--porcelain"}, "fsmonitor"},
		{[]string{"diff", "HEAD"}, "clean"},
		{[]string{"log", "-1"}, "gpg"},
		{[]string{"worktree", "add", "-b", "coral-y", wt, "main"}, "hook-post-checkout"},
	} {
		os.Remove(marker)
		exec.Command("git", append([]string{"-C", repo}, c.args...)...).Run()
		if got := ran(marker); !strings.Contains(got, c.want) {
			t.Errorf("plain git %v ran %q; the rig expects %s to run", c.args, got, c.want)
		}
	}
}

func TestFilterOverrides(t *testing.T) {
	repo, _ := hostileRepo(t)
	got := filterOverrides(context.Background(), repo)
	for _, want := range []string{"filter.evil.clean=", "filter.evil.smudge=", "filter.lfs.process=", "filter.other.clean=", "filter.Mixed.smudge="} {
		if !slices.Contains(got, want) {
			t.Errorf("no %s in %v", want, got)
		}
	}
	// Git LFS's own driver, word for word, is left to run.
	exec.Command("git", "-C", repo, "config", "filter.lfs.process", "git-lfs filter-process").Run()
	exec.Command("git", "-C", repo, "config", "filter.lfs.smudge", "git-lfs smudge -- %f").Run()
	got = filterOverrides(context.Background(), repo)
	for _, k := range []string{"filter.lfs.process=", "filter.lfs.smudge="} {
		if slices.Contains(got, k) {
			t.Errorf("git-lfs's own driver was emptied: %v", got)
		}
	}
}

func TestDiffGuards(t *testing.T) {
	for _, c := range []struct{ in, want []string }{
		{[]string{"diff", "HEAD"}, []string{"diff", "--no-ext-diff", "--no-textconv", "HEAD"}},
		{[]string{"--literal-pathspecs", "diff", "x"}, []string{"--literal-pathspecs", "diff", "--no-ext-diff", "--no-textconv", "x"}},
		{[]string{"show", "HEAD:a"}, []string{"show", "--no-ext-diff", "--no-textconv", "HEAD:a"}},
		{[]string{"log", "-1"}, []string{"log", "-1"}},
		{[]string{"rev-parse", "diff"}, []string{"rev-parse", "diff"}},
	} {
		if got := withDiffGuards(c.in); !slices.Equal(got, c.want) {
			t.Errorf("withDiffGuards(%v) = %v, want %v", c.in, got, c.want)
		}
	}
	args := Args(context.Background(), "", "status")
	for _, want := range []string{"core.fsmonitor=false", "core.hooksPath=" + noHooksPath(), "diff.external=", "core.pager=cat", "core.sshCommand=ssh", "protocol.ext.allow=never", "log.showSignature=false"} {
		if !slices.Contains(args, want) {
			t.Errorf("Args lacks -c %s: %v", want, args)
		}
	}
	if env := Env(); !slices.Contains(env, "GIT_TERMINAL_PROMPT=0") || !slices.Contains(env, "GIT_OPTIONAL_LOCKS=0") {
		t.Errorf("Env = %v", env)
	}
}
