package gitutil

import (
	"bytes"
	"context"
	"github.com/subgentic/coral/internal/agentenv"
	"os"
	"os/exec"
	"strings"

	"github.com/subgentic/coral/internal/executil"
)

// Git in folders agents can write to (#3579). A repository's own config can name commands
// for git to run: an fsmonitor hook, hooks, an external diff, textconv and filter drivers.
// Coral runs git in agents' folders, as the user, outside the agent CLI's permission
// prompts, so every git Coral runs goes through Command, which turns all of them off:
//
//   - core.fsmonitor=false, core.hooksPath=<a path with no hooks in it>
//   - diff.external=; --no-ext-diff and --no-textconv after diff, show and log, and
//     --no-textconv after blame and grep
//   - every clean, smudge and process command of every filter driver the repository's
//     config names, emptied (git then copies the file as it is), with that driver's
//     required=false so an emptied required driver doesn't fail the command. The exception
//     is Git LFS's own standard driver, word for word, which runs the git-lfs on the PATH.
//   - submodules aren't looked into (diff.ignoreSubmodules=all, submodule.recurse=false,
//     status.submoduleSummary=false): a submodule has its own config, with filter drivers
//     the overrides above don't name, and git runs them when it checks a submodule
//   - core.pager=cat, core.sshCommand=ssh, protocol.ext.allow=never,
//     log.showSignature=false (a signature check runs gpg.program)
//   - GIT_TERMINAL_PROMPT=0 and GIT_OPTIONAL_LOCKS=0 in its environment
//
// Not neutralised: merge drivers (merge.<name>.driver). Coral never merges, rebases or
// cherry-picks; a caller that starts to must add them.
//
// TestGitRunsOnlyThroughCommand fails when code runs git any other way, or asks git for
// textconv or filters (--textconv, --filters).

// Command returns a git command run in dir (git -C dir, when dir isn't ""), with a
// repository's command-running config turned off.
func Command(ctx context.Context, dir string, args ...string) *exec.Cmd {
	cmd := executil.Command(ctx, "git", Args(ctx, dir, args...)...)
	cmd.Env = append(agentenv.Filter(os.Environ()), Env()...) // Coral's own CORAL_* settings never reach git (#25 H3)
	return cmd
}

// Args is the argument list Command runs git with, for a caller that has to build the
// exec.Cmd itself (its own environment, say); it must add Env() to that environment.
func Args(ctx context.Context, dir string, args ...string) []string {
	full := safeConfig()
	if !filterFree[subcommand(args)] {
		full = append(full, filterOverrides(ctx, dir)...)
	}
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
		"-c", "diff.ignoreSubmodules=all",
		"-c", "submodule.recurse=false",
		"-c", "status.submoduleSummary=false",
	}
}

// withDiffGuards adds --no-ext-diff and --no-textconv right after a diff, show or log
// subcommand, and --no-textconv after blame or grep (the subcommand is the first argument
// that isn't an option).
func withDiffGuards(args []string) []string {
	for i, a := range args {
		if strings.HasPrefix(a, "-") {
			if a == "-C" || a == "-c" {
				return args // a global option with a value: the caller built its own; leave it
			}
			continue
		}
		var guards []string
		switch a {
		case "diff", "show", "log":
			guards = []string{"--no-ext-diff", "--no-textconv"}
		case "blame", "grep":
			guards = []string{"--no-textconv"}
		default:
			return args
		}
		out := make([]string, 0, len(args)+len(guards))
		out = append(out, args[:i+1]...)
		out = append(out, guards...)
		return append(out, args[i+1:]...)
	}
	return args
}

// filterFree are the subcommands that never run a filter driver: they read the object store,
// refs and config, never a file in the working tree, which is where clean and smudge run. For
// them the extra git that reads the filter config is not run: it doubled every git Coral runs
// (#3847: about 240 of the 600 git processes in 6.5 minutes with 40 agents). status, diff, show,
// ls-files (-m can hash a racily clean file), cat-file and worktree (add checks files out) keep
// the overrides.
var filterFree = map[string]bool{
	"rev-parse": true, "rev-list": true, "log": true, "remote": true, "config": true, "merge-base": true,
}

// subcommand is git's subcommand in args: the first argument that isn't an option, or "" when
// the caller passes its own -c or -C first.
func subcommand(args []string) string {
	for _, a := range args {
		if a == "-C" || a == "-c" {
			return ""
		}
		if !strings.HasPrefix(a, "-") {
			return a
		}
	}
	return ""
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
	cmd.Env = append(agentenv.Filter(os.Environ()), Env()...) // Coral's own CORAL_* settings never reach git (#25 H3)
	out, err := cmd.Output()
	if err != nil {
		return nil // exit 1: no filter is configured
	}
	var over []string
	seen := map[string]bool{}
	for _, entry := range bytes.Split(out, []byte{0}) {
		key, value, _ := strings.Cut(string(entry), "\n")
		lower := strings.ToLower(key)
		dot := strings.LastIndexByte(key, '.')
		if dot <= len("filter.") {
			continue
		}
		switch lower[dot+1:] {
		case "clean", "smudge", "process":
		default:
			continue
		}
		if want, ok := lfsDriver[lower]; ok && value == want {
			continue
		}
		if !seen[lower] {
			seen[lower] = true
			over = append(over, "-c", key+"=")
		}
		// An emptied command of a required driver fails the command ("clean filter
		// failed"): the driver is no longer required.
		if required := key[:dot] + ".required"; !seen[strings.ToLower(required)] {
			seen[strings.ToLower(required)] = true
			over = append(over, "-c", required+"=false")
		}
	}
	return over
}
