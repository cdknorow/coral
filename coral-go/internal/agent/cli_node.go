package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// An agent CLI installed with npm (claude, codex, pi) is a script that starts with "#!/usr/bin/env node". The macOS app started from Finder has a minimal PATH
// (/usr/bin:/bin:...), so env finds no node: "env: node: No such file or directory", whether Coral probes the CLI (--version, the sign-in check, the model list) or an agent
// launches it. The node that installed the CLI is beside it, so the CLI runs with its own node: the folder is put at the END of PATH for that one child (never the
// process, and never first, so nothing already found is shadowed), and only from a folder no other user can write (trustedNodeDir).

// NodeNotFoundError is a CLI that needs node, with no node on PATH and none beside it that is safe to use.
type NodeNotFoundError struct {
	CLI    string // the CLI's path
	Reason string // why no node was used: none found, or the one found is in a folder others can write
}

func (e *NodeNotFoundError) Error() string {
	return fmt.Sprintf("node not found: %s is a node script and needs node, but %s. Install Node.js, or put its folder on the PATH Coral runs with.", e.CLI, e.Reason)
}

// NodeDirFor is the folder to put at the end of PATH so the CLI at cliPath runs with its own node, for a child that gets env: "" with a nil error when nothing is needed
// (Windows, whose npm shims name their node; a CLI that is not an "env node" script; a node already on env's PATH), a *NodeNotFoundError when the CLI needs node and no node
// that is safe to use is on PATH or beside it.
func NodeDirFor(cliPath string, env []string) (string, error) {
	if runtime.GOOS == "windows" || cliPath == "" {
		return "", nil
	}
	real, err := filepath.EvalSymlinks(cliPath)
	if err != nil || !needsEnvNode(real) {
		return "", nil
	}
	if nodeOnPath(pathOf(env)) {
		return "", nil
	}
	dir, why := nodeDirFor(cliPath, real)
	if dir == "" {
		return "", &NodeNotFoundError{CLI: cliPath, Reason: why}
	}
	return dir, nil
}

// CLIEnv is env for a child that runs the CLI at cliPath: with its own node's folder at the end of PATH when NodeDirFor says one is needed, else env as it is (a missing node
// is NodeDirFor's error for the callers that report it; the child then fails as before).
func CLIEnv(env []string, cliPath string) []string {
	dir, err := NodeDirFor(cliPath, env)
	if err != nil || dir == "" {
		return env
	}
	path := pathOf(env)
	if path == "" {
		path = dir
	} else {
		path += string(os.PathListSeparator) + dir
	}
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if len(kv) >= 5 && strings.EqualFold(kv[:5], "PATH=") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "PATH="+path)
}

// NodePathPrefix is the shell prefix for an agent's launch line (like WrapWithBundlePath): when the CLI needs a node that Coral's PATH lacks and one is found beside it, the shell
// adds that folder at the end of ITS PATH, but only if the shell has no node of its own (a login shell may have put nvm's on it); when none is found or safe, the shell without a
// node of its own prints the NodeNotFoundError and stops. "" when nothing is needed, and on Windows. A tmux server started earlier keeps the PATH it had: this is per launch line,
// run by the agent's own shell, so it does not depend on what that server inherited.
func NodePathPrefix(cliPath string) string {
	dir, err := NodeDirFor(cliPath, os.Environ())
	if err != nil {
		// Coral found none, but the agent's shell may have one (a login shell's nvm): only when it has none the terminal says so, plainly, instead of "env: node: No such file".
		return fmt.Sprintf(`{ command -v node >/dev/null 2>&1 || { echo %s; false; }; } && `, singleQuote("Coral: "+err.Error()))
	}
	if dir == "" {
		return ""
	}
	return fmt.Sprintf(`{ command -v node >/dev/null 2>&1 || export PATH="$PATH":%s; } && `, singleQuote(dir))
}

// WrapWithCLINode is cmd with NodePathPrefix(cliPath) in front.
func WrapWithCLINode(cmd, cliPath string) string { return NodePathPrefix(cliPath) + cmd }

func pathOf(env []string) string {
	path := ""
	for _, kv := range env {
		if len(kv) >= 5 && strings.EqualFold(kv[:5], "PATH=") {
			path = kv[5:]
		}
	}
	return path
}

// nodeOnPath reports whether a node that can run is in one of the folders of path (an empty entry is the current folder, which is never searched).
func nodeOnPath(path string) bool {
	for _, d := range filepath.SplitList(path) {
		if d == "" || !filepath.IsAbs(d) {
			continue
		}
		if st, err := os.Stat(filepath.Join(d, "node")); err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0 {
			return true
		}
	}
	return false
}

// needsEnvNode reports whether the file starts with a "#!" line that runs node through env ("#!/usr/bin/env node", "#!/usr/bin/env -S node --flag").
func needsEnvNode(file string) bool {
	f, err := os.Open(file)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 256) // a binary has no newline for a long way: only its first bytes are read
	n, _ := f.Read(head)
	line, _, _ := strings.Cut(string(head[:n]), "\n")
	if !strings.HasPrefix(line, "#!") {
		return false
	}
	fields := strings.Fields(line[2:])
	if len(fields) < 2 || filepath.Base(fields[0]) != "env" {
		return false
	}
	for _, a := range fields[1:] {
		if strings.HasPrefix(a, "-") || strings.Contains(a, "=") {
			continue // an env option or a variable assignment
		}
		return a == "node"
	}
	return false
}

// nodeDirFor is the folder of the node that installed the CLI, from the layouts npm and its managers make and nothing else: beside the CLI as it was named (the Homebrew
// and nvm layouts, <prefix>/bin/claude next to <prefix>/bin/node), beside the file it links to, or <prefix>/bin for a package under <prefix>/lib/node_modules. The first such
// folder with a node is used if trustedNodeDir accepts it; "" and why when there is none or it is not safe.
func nodeDirFor(cliPath, real string) (dir, why string) {
	var dirs []string
	if abs, err := filepath.Abs(cliPath); err == nil {
		dirs = append(dirs, filepath.Dir(abs))
	}
	dirs = append(dirs, filepath.Dir(real))
	if i := strings.LastIndex(real, string(os.PathSeparator)+filepath.Join("lib", "node_modules")+string(os.PathSeparator)); i > 0 {
		dirs = append(dirs, filepath.Join(real[:i], "bin"))
	}
	why = "none was found on PATH or beside it"
	seen := map[string]bool{}
	for _, d := range dirs {
		if seen[d] {
			continue
		}
		seen[d] = true
		if _, err := os.Stat(filepath.Join(d, "node")); err != nil {
			continue
		}
		if err := trustedNodeDir(d); err != nil {
			why = fmt.Sprintf("the node in %s is not used (%v)", d, err)
			continue
		}
		return d, ""
	}
	return "", why
}
