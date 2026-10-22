//go:build !windows

package agent

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/secfile"
)

// An npm-installed CLI is a "#!/usr/bin/env node" script; the macOS app's minimal PATH has no node, so env fails ("env: node: No such file or directory"). The CLI runs with its own
// node, put at the END of PATH for that one child, from a folder no other user can write; with no usable node, the error says so.

const minimalPath = "/usr/bin:/bin"

// npmLayout builds <prefix>/lib/node_modules/pkg/cli.js (with the given first line), <prefix>/bin/claude linking to it and, when withNode, <prefix>/bin/node (a script that
// answers with a version, so the CLI's own run can be checked). Folders are 0755, as npm and Homebrew make them.
func npmLayout(t *testing.T, shebang string, withNode bool) (prefix, cli string) {
	t.Helper()
	prefix = t.TempDir()
	pkg := filepath.Join(prefix, "lib", "node_modules", "pkg")
	require.NoError(t, os.MkdirAll(pkg, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(prefix, "bin"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pkg, "cli.js"), []byte(shebang+"\nconsole.log('claude 1.2.3')\n"), 0o755))
	cli = filepath.Join(prefix, "bin", "claude")
	require.NoError(t, os.Symlink(filepath.Join("..", "lib", "node_modules", "pkg", "cli.js"), cli))
	if withNode {
		require.NoError(t, os.WriteFile(filepath.Join(prefix, "bin", "node"), []byte("#!/bin/sh\necho \"claude 1.2.3 (run by its own node)\"\n"), 0o755))
	}
	return prefix, cli
}

// minimalEnv is the app's environment: PATH holds only /usr/bin and /bin, after checking that neither has a node (a machine with one skips instead of passing for nothing).
func minimalEnv(t *testing.T) []string {
	t.Helper()
	t.Setenv("PATH", minimalPath)
	if _, err := exec.LookPath("node"); err == nil {
		t.Skip("this machine has a node in /usr/bin or /bin")
	}
	return []string{"PATH=" + minimalPath, "HOME=" + t.TempDir()}
}

func real(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	require.NoError(t, err)
	return r
}

func TestCLIEnvAddsTheCLIsOwnNodeAtTheEndOfTheChildsPathOnly(t *testing.T) {
	env := minimalEnv(t)
	prefix, cli := npmLayout(t, "#!/usr/bin/env node", true)

	got := CLIEnv(env, cli)

	assert.Equal(t, minimalPath, os.Getenv("PATH"), "the process PATH is never changed: this is per child")
	assert.Equal(t, "PATH="+minimalPath+string(os.PathListSeparator)+filepath.Join(prefix, "bin"), pathLine(got), "appended, never first")
	assert.Equal(t, "HOME="+env[1][5:], got[0], "the rest of the environment is kept")
	// A child with that environment runs the CLI: env finds that node.
	cmd := exec.Command(cli, "--version")
	cmd.Env = got
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	assert.Contains(t, string(out), "run by its own node")
	// and one without it does not (the failure being fixed)
	cmd = exec.Command(cli, "--version")
	cmd.Env = env
	out, err = cmd.CombinedOutput()
	assert.Error(t, err)
	assert.Contains(t, string(out), "node")
}

func pathLine(env []string) string {
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			return kv
		}
	}
	return ""
}

func TestNodeDirForFindsTheNodeInTheLayoutsNpmMakes(t *testing.T) {
	env := minimalEnv(t)
	prefix, _ := npmLayout(t, "#!/usr/bin/env node", true)
	// linked from another folder: the node is only in <prefix>/bin, found from where the link points (<prefix>/lib/node_modules/...)
	link := filepath.Join(t.TempDir(), "claude")
	require.NoError(t, os.Symlink(filepath.Join(prefix, "lib", "node_modules", "pkg", "cli.js"), link))
	dir, err := NodeDirFor(link, env)
	require.NoError(t, err)
	assert.Equal(t, real(t, filepath.Join(prefix, "bin")), real(t, dir))

	// a standalone install: the script and its node share a folder
	home := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(home, "claude-cli"), []byte("#!/usr/bin/env node\n"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, "node"), []byte("#!/bin/sh\n"), 0o755))
	link2 := filepath.Join(t.TempDir(), "claude")
	require.NoError(t, os.Symlink(filepath.Join(home, "claude-cli"), link2))
	dir, err = NodeDirFor(link2, env)
	require.NoError(t, err)
	assert.Equal(t, real(t, home), real(t, dir))
}

// Codex Test's controls (#5899 round 1): a folder another user can write is never used, an arbitrary folder above the CLI is not searched, and with none the error says node.
func TestNodeDirForRefusesAFolderOthersCanWrite(t *testing.T) {
	env := minimalEnv(t)
	groupIsNotMine(t) // a group of several: its write bit counts
	for name, unsafe := range map[string]func(t *testing.T, prefix string){
		"the node's folder is world-writable (0777)": func(t *testing.T, prefix string) { require.NoError(t, os.Chmod(filepath.Join(prefix, "bin"), 0o777)) },
		"the node's folder is group-writable":        func(t *testing.T, prefix string) { require.NoError(t, os.Chmod(filepath.Join(prefix, "bin"), 0o775)) },
		"the node file is writable by others": func(t *testing.T, prefix string) {
			require.NoError(t, os.Chmod(filepath.Join(prefix, "bin", "node"), 0o777))
		},
		"a parent folder is group-writable": func(t *testing.T, prefix string) { require.NoError(t, os.Chmod(prefix, 0o775)) },
	} {
		prefix, cli := npmLayout(t, "#!/usr/bin/env node", true)
		unsafe(t, prefix)
		dir, err := NodeDirFor(cli, env)
		assert.Empty(t, dir, name)
		var nf *NodeNotFoundError
		require.True(t, errors.As(err, &nf), name+": %v", err)
		assert.Contains(t, nf.Error(), "node not found")
		assert.Contains(t, nf.Reason, "written by others", name)
		assert.Equal(t, env, CLIEnv(env, cli), name+": the environment is left as it is")
	}
}

func TestNodeDirForRefusesANodeThatIsALinkIntoAnUnsafeFolder(t *testing.T) {
	env := minimalEnv(t)
	prefix, cli := npmLayout(t, "#!/usr/bin/env node", false)
	cellar := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(cellar, "node"), []byte("#!/bin/sh\n"), 0o755))
	require.NoError(t, os.Symlink(filepath.Join(cellar, "node"), filepath.Join(prefix, "bin", "node"))) // the Homebrew shape: bin/node -> Cellar/.../bin/node
	dir, err := NodeDirFor(cli, env)
	require.NoError(t, err, "a link into a safe folder is accepted")
	assert.Equal(t, real(t, filepath.Join(prefix, "bin")), real(t, dir))

	require.NoError(t, os.Chmod(cellar, 0o777)) // where the link really points can be replaced by anyone
	_, err = NodeDirFor(cli, env)
	var nf *NodeNotFoundError
	require.True(t, errors.As(err, &nf), "%v", err)
	assert.Contains(t, nf.Reason, "written by others")
}

func TestNodeDirForDoesNotSearchFoldersAboveTheCLI(t *testing.T) {
	env := minimalEnv(t)
	// a node in some bin folder two levels above a CLI that is neither beside it nor in a lib/node_modules package: not Node's layout, not used
	top := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(top, "bin"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(top, "bin", "node"), []byte("#!/bin/sh\n"), 0o755))
	deep := filepath.Join(top, "a", "b")
	require.NoError(t, os.MkdirAll(deep, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(deep, "claude"), []byte("#!/usr/bin/env node\n"), 0o755))
	dir, err := NodeDirFor(filepath.Join(deep, "claude"), env)
	assert.Empty(t, dir)
	var nf *NodeNotFoundError
	assert.True(t, errors.As(err, &nf), "%v", err)
}

func TestNodeDirForSaysNodeIsMissing(t *testing.T) {
	env := minimalEnv(t)
	_, cli := npmLayout(t, "#!/usr/bin/env node", false)
	dir, err := NodeDirFor(cli, env)
	assert.Empty(t, dir)
	var nf *NodeNotFoundError
	require.True(t, errors.As(err, &nf))
	assert.Contains(t, err.Error(), "node not found")
	assert.Contains(t, err.Error(), cli)
	assert.Contains(t, nf.Reason, "none was found")
}

func TestNodeDirForChangesNothingWhenItShouldNot(t *testing.T) {
	env := minimalEnv(t)
	cases := map[string]func(t *testing.T) string{
		"a script that needs another interpreter": func(t *testing.T) string { _, cli := npmLayout(t, "#!/bin/sh", true); return cli },
		"a node script with an absolute node":     func(t *testing.T) string { _, cli := npmLayout(t, "#!/opt/somewhere/bin/node", true); return cli },
		"an env script of another program":        func(t *testing.T) string { _, cli := npmLayout(t, "#!/usr/bin/env python3", true); return cli },
		"a binary (no shebang)": func(t *testing.T) string {
			p := filepath.Join(t.TempDir(), "claude")
			require.NoError(t, os.WriteFile(p, []byte("\x7fELF\x02\x01\x01\x00 not a script"), 0o755))
			return p
		},
		"a missing file": func(t *testing.T) string { return filepath.Join(t.TempDir(), "nope") },
		"an empty path":  func(t *testing.T) string { return "" },
	}
	for name, make := range cases {
		dir, err := NodeDirFor(make(t), env)
		assert.NoError(t, err, name)
		assert.Empty(t, dir, name)
	}
	// a PATH that already has a node: that one is used
	own := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(own, "node"), []byte("#!/bin/sh\n"), 0o755))
	_, cli := npmLayout(t, "#!/usr/bin/env node", true)
	withNode := []string{"PATH=" + minimalPath + string(os.PathListSeparator) + own}
	dir, err := NodeDirFor(cli, withNode)
	assert.NoError(t, err)
	assert.Empty(t, dir)
	assert.Equal(t, withNode, CLIEnv(withNode, cli))
}

func TestNeedsEnvNodeShebangForms(t *testing.T) {
	for line, want := range map[string]bool{
		"#!/usr/bin/env node":           true,
		"#!/usr/bin/env -S node --flag": true,
		"#!/usr/bin/env FOO=1 node":     true,
		"#!/bin/env node":               true,
		"#!/usr/bin/env nodejs":         false,
		"#!/usr/bin/node":               false,
		"#!/usr/bin/env":                false,
		"/usr/bin/env node":             false,
		"":                              false,
	} {
		p := filepath.Join(t.TempDir(), "f")
		require.NoError(t, os.WriteFile(p, []byte(line+"\nrest\n"), 0o755))
		assert.Equal(t, want, needsEnvNode(p), "%q", line)
	}
}

// The agent's launch line: its own shell adds the node folder when it has no node, says "node not found" when there is none, and leaves a shell that has a node alone.
func TestNodePathPrefixInTheAgentsLaunchLine(t *testing.T) {
	minimalEnv(t)
	prefix, cli := npmLayout(t, "#!/usr/bin/env node", true)
	run := func(path, line string) (string, error) {
		cmd := exec.Command("/bin/sh", "-c", line)
		cmd.Env = []string{"PATH=" + path}
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	out, err := run(minimalPath, WrapWithCLINode(cli+" --version", cli))
	require.NoError(t, err, out)
	assert.Contains(t, out, "run by its own node")

	// the shell has a node of its own (a login shell's nvm): the line adds nothing
	own := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(own, "node"), []byte("#!/bin/sh\necho \"the shell's own node\"\n"), 0o755))
	out, err = run(minimalPath+string(os.PathListSeparator)+own, WrapWithCLINode(cli+" --version", cli))
	require.NoError(t, err, out)
	assert.Contains(t, out, "the shell's own node")
	_ = prefix

	// no node anywhere: the terminal says so, plainly, and nothing runs
	_, cli2 := npmLayout(t, "#!/usr/bin/env node", false)
	out, err = run(minimalPath, WrapWithCLINode(cli2+" --version", cli2))
	assert.Error(t, err)
	assert.Contains(t, out, "node not found")
	assert.NotContains(t, out, "No such file")

	// nothing needed: the line is the line
	assert.Equal(t, "x --version", WrapWithCLINode("x --version", ""))
}

// FindCLI is what every caller that runs an agent CLI uses (#3169): through it the probes get the error, and the launch gets the CLI.
func TestFindCLIReportsAMissingNodeAndFindCLIForLaunchDoesNot(t *testing.T) {
	minimalEnv(t)
	_, cli := npmLayout(t, "#!/usr/bin/env node", false)
	settings := map[string]string{CLIPathSettingKey("claude"): cli}
	_, err := FindCLI(settings, "claude")
	var nf *NodeNotFoundError
	require.True(t, errors.As(err, &nf), "%v", err)
	got, err := FindCLIForLaunch(settings, "claude")
	require.NoError(t, err, "a launch's shell may have a node Coral's process lacks")
	assert.Equal(t, cli, got)

	_, cli2 := npmLayout(t, "#!/usr/bin/env node", true)
	got, err = FindCLI(map[string]string{CLIPathSettingKey("claude"): cli2}, "claude")
	require.NoError(t, err)
	assert.Equal(t, cli2, got)
	assert.Equal(t, minimalPath, os.Getenv("PATH"), "and nothing was changed process-wide")
}

func TestCLIVersionRunsAnNpmCLIUnderAMinimalPath(t *testing.T) {
	minimalEnv(t)
	_, cli := npmLayout(t, "#!/usr/bin/env node", true)
	v := CLIVersion(map[string]string{CLIPathSettingKey("claude"): cli}, "claude")
	assert.Equal(t, "1.2.3", v)
	assert.Equal(t, minimalPath, os.Getenv("PATH"))
}

// secfile.PathWriters is the one check (its tests cover owners, modes, every link on the way and access lists): a node it names a writer for is refused, and one it cannot examine is too.
func TestNodeDirForRefusesWhatPathWritersNamesOrCannotExamine(t *testing.T) {
	env := minimalEnv(t)
	_, cli := npmLayout(t, "#!/usr/bin/env node", true)
	dir, err := NodeDirFor(cli, env)
	require.NoError(t, err)
	assert.NotEmpty(t, dir)

	old := pathWriters
	t.Cleanup(func() { pathWriters = old })
	pathWriters = func(string) ([]secfile.Writer, error) {
		return []secfile.Writer{{Text: "everyone can write /somewhere", Path: "/somewhere"}}, nil
	}
	_, err = NodeDirFor(cli, env)
	var nf *NodeNotFoundError
	require.True(t, errors.As(err, &nf), "%v", err)
	assert.Contains(t, nf.Reason, "written by others")
	assert.Contains(t, nf.Reason, "everyone can write /somewhere")

	pathWriters = func(string) ([]secfile.Writer, error) { return nil, errors.New("the access list could not be read") }
	_, err = NodeDirFor(cli, env)
	require.True(t, errors.As(err, &nf), "%v", err)
	assert.Contains(t, nf.Reason, "could not be checked")
}

// Codex Test's controls (#5899 round 2), end to end through NodeDirFor: a link in the middle of the chain that anyone can replace, and an access list on the node's folder.
func TestNodeDirForRefusesALinkChainThroughAFolderAnyoneCanWrite(t *testing.T) {
	env := minimalEnv(t)
	base := t.TempDir()
	safe, target, bad := filepath.Join(base, "safe"), filepath.Join(base, "target"), filepath.Join(base, "bad")
	for _, d := range []string{safe, target, bad} {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(safe, "codex"), []byte("#!/usr/bin/env node\n"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(target, "node"), []byte("#!/bin/sh\n"), 0o755))
	require.NoError(t, os.Chmod(bad, 0o777))
	require.NoError(t, os.Symlink(target, filepath.Join(bad, "bridge")))
	require.NoError(t, os.Symlink(filepath.Join(bad, "bridge", "node"), filepath.Join(safe, "node"))) // safe/node -> bad/bridge/node -> target/node

	dir, err := NodeDirFor(filepath.Join(safe, "codex"), env)
	assert.Empty(t, dir, "safe and target are closed, but anyone can replace bad/bridge")
	var nf *NodeNotFoundError
	require.True(t, errors.As(err, &nf), "%v", err)
	assert.Contains(t, nf.Reason, "bad")
}

// groupIsNotMine answers "not the user's alone" for every group, as on a machine with a second account.
func groupIsNotMine(t *testing.T) {
	t.Helper()
	old := groupAlone
	t.Cleanup(func() { groupAlone = old })
	groupAlone = func(uint32) (bool, error) { return false, nil }
}

// Homebrew's folders are the user's own and group-writable for admin (#5899, Homebrew's installer): on a Mac where admin is the user alone that bit grants nobody anything and the node is
// used; everywhere else the refusal stands, and says what to do.
func TestNodeDirForAcceptsAGroupWriteBitOnlyWhenTheGroupIsTheUsersAlone(t *testing.T) {
	env := minimalEnv(t)
	group := func(t *testing.T, alone bool, err error) (string, string) {
		t.Helper()
		old := groupAlone
		t.Cleanup(func() { groupAlone = old })
		groupAlone = func(uint32) (bool, error) { return alone, err }
		prefix, cli := npmLayout(t, "#!/usr/bin/env node", true)
		require.NoError(t, os.Chmod(filepath.Join(prefix, "bin"), 0o775)) // user-owned, group-writable
		return prefix, cli
	}

	t.Run("the group is the user's alone: accepted", func(t *testing.T) {
		prefix, cli := group(t, true, nil)
		dir, err := NodeDirFor(cli, env)
		require.NoError(t, err)
		assert.Equal(t, real(t, filepath.Join(prefix, "bin")), real(t, dir))
	})
	t.Run("the group has other members: refused, with the folder, the group and the chmod", func(t *testing.T) {
		prefix, cli := group(t, false, nil)
		_, err := NodeDirFor(cli, env)
		var nf *NodeNotFoundError
		require.True(t, errors.As(err, &nf), "%v", err)
		bin := real(t, filepath.Join(prefix, "bin")) // the walk names the folder after its links are resolved
		msg := nf.Error()
		assert.Contains(t, nf.Reason, "its group (")
		assert.Contains(t, nf.Reason, bin)
		assert.Contains(t, msg, "chmod g-w '"+bin+"'", "the fix names the folder")
		assert.Contains(t, msg, "PATH has node's folder")
		assert.NotContains(t, msg, "Install Node.js", "the person has node")
	})
	t.Run("a world write bit is refused whatever the group is", func(t *testing.T) {
		prefix, cli := group(t, true, nil)
		require.NoError(t, os.Chmod(filepath.Join(prefix, "bin"), 0o777))
		_, err := NodeDirFor(cli, env)
		var nf *NodeNotFoundError
		require.True(t, errors.As(err, &nf), "%v", err)
		assert.Contains(t, nf.Reason, "everyone can write in")
		assert.Contains(t, nf.Error(), "Ask whoever owns those folders", "chmod g-w would not help: the fix says so")
		assert.NotContains(t, nf.Error(), "chmod g-w")
	})
	t.Run("a group that could not be checked is refused", func(t *testing.T) {
		_, cli := group(t, true, errors.New("dscl timed out"))
		_, err := NodeDirFor(cli, env)
		var nf *NodeNotFoundError
		require.True(t, errors.As(err, &nf), "%v", err)
		assert.Contains(t, nf.Reason, "could not be checked")
		assert.Contains(t, nf.Reason, "dscl timed out")
	})
}

func TestNodeNotFoundKeepsTheInstallAdviceOnlyWhenThereIsNoNode(t *testing.T) {
	env := minimalEnv(t)
	_, cli := npmLayout(t, "#!/usr/bin/env node", false)
	_, err := NodeDirFor(cli, env)
	var nf *NodeNotFoundError
	require.True(t, errors.As(err, &nf), "%v", err)
	assert.Contains(t, nf.Error(), "Install Node.js")
	assert.Contains(t, nf.Error(), "PATH has node's folder")
}
