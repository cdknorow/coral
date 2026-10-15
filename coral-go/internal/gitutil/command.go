package gitutil

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"

	"github.com/cdknorow/coral/internal/executil"
)

// Git in folders agents can write to (#3579). A repository's own config can name commands
// for git to run: an fsmonitor hook, hooks, an external diff, textconv and filter drivers.
// Coral runs git in agents' folders, as the user, outside the agent CLI's permission
// prompts, so every git Coral runs goes through Command, which turns all of them off:
//
//   - core.fsmonitor=false, core.hooksPath=<a path with no hooks in it>
//   - diff.external= and, for diff and show, --no-ext-diff and --no-textconv
//   - every clean, smudge and process command of every filter driver the repository's
//     config names, emptied (git then copies the file as it is). The exception is Git
//     LFS's own standard driver, word for word, which runs the git-lfs on the PATH.
//   - core.pager=cat, core.sshCommand=ssh, protocol.ext.allow=never,
//     log.showSignature=false (a signature check runs gpg.program)
//   - GIT_TERMINAL_PROMPT=0 and GIT_OPTIONAL_LOCKS=0 in its environment
//
// TestGitRunsOnlyThroughCommand fails when code runs git any other way.

// Command returns a git command run in dir (git -C dir, when dir isn't ""), with a
// repository's command-running config turned off.
func Command(ctx context.Context, dir string, args ...string) *exec.Cmd {
	cmd := executil.Command(ctx, "git", Args(ctx, dir, args...)...)
	cmd.Env = append(os.Environ(), Env()...)
	return cmd
}

// Args is the argument list Command runs git with, for a caller that has to build the
// exec.Cmd itself (its own environment, say); it must add Env() to that environment.
func Args(ctx context.Context, dir string, args ...string) []string {
	full := append(safeConfig(), filterOverrides(ctx, dir)...)
	if dir != "" {
		full = append(full, "-C", dir)
	}
	return append(full, withDiffGuards(args)...)
}

// Env is what Command adds to git's environment.
func Env() []string {
	return []string{"GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0"}
}

func safeConfig() []string {
	return []string{
		"-c", "core.fsmonitor=false",
		"-c", "core.hooksPath=" + noHooksPath(),
		"-c", "diff.external=",
		"-c", "core.pager=cat",
		"-c", "core.sshCommand=ssh",
		"-c", "protocol.ext.allow=never",
		"-c", "log.showSignature=false",
	}
}

// withDiffGuards adds --no-ext-diff and --no-textconv right after a diff or show
// subcommand (the first argument that isn't an option).
func withDiffGuards(args []string) []string {
	for i, a := range args {
		if strings.HasPrefix(a, "-") {
			if a == "-C" || a == "-c" {
				return args // a global option with a value: the caller built its own; leave it
			}
			continue
		}
		if a != "diff" && a != "show" {
			return args
		}
		out := make([]string, 0, len(args)+2)
		out = append(out, args[:i+1]...)
		out = append(out, "--no-ext-diff", "--no-textconv")
		return append(out, args[i+1:]...)
	}
	return args
}

// lfsDriver is Git LFS's standard filter driver, as 'git lfs install' writes it.
var lfsDriver = map[string]string{
	"filter.lfs.clean":   "git-lfs clean -- %f",
	"filter.lfs.smudge":  "git-lfs smudge -- %f",
	"filter.lfs.process": "git-lfs filter-process",
}

// filterOverrides empties every filter command the config in dir names. Reading config runs
// nothing. If it can't be read, every git command after it fails the same way, so nothing
// runs a filter either.
func filterOverrides(ctx context.Context, dir string) []string {
	args := []string{"-c", "core.fsmonitor=false"}
	if dir != "" {
		args = append(args, "-C", dir)
	}
	args = append(args, "config", "--null", "--get-regexp", `^filter\.`)
	cmd := executil.Command(ctx, "git", args...)
	cmd.Env = append(os.Environ(), Env()...)
	out, err := cmd.Output()
	if err != nil {
		return nil // exit 1: no filter is configured
	}
	var over []string
	seen := map[string]bool{}
	for _, entry := range bytes.Split(out, []byte{0}) {
		key, value, _ := strings.Cut(string(entry), "\n")
		lower := strings.ToLower(key)
		if !strings.HasSuffix(lower, ".clean") && !strings.HasSuffix(lower, ".smudge") && !strings.HasSuffix(lower, ".process") {
			continue
		}
		if want, ok := lfsDriver[lower]; ok && value == want {
			continue
		}
		if !seen[lower] {
			seen[lower] = true
			over = append(over, "-c", key+"=")
		}
	}
	return over
}
