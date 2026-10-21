package agent

import (
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"

	"github.com/subgentic/coral/internal/psquote"
)

// ResolveHookCommand turns a hook command Coral installs ("coral-hook-agentic-state --hooks-rev
// 2") into one that names the tool by its absolute path in CoralToolsDir, quoted: the agents'
// CLIs run hooks through a shell in the agent's folder, so a bare name was found through PATH,
// where an earlier folder (a repository's node_modules/.bin, ".") or, on Windows, the current
// folder could put another program of that name first (#3660, update audit item 6). The path
// is written with forward slashes (sh, cmd.exe and Git Bash all take them) inside double quotes
// (a space in "Program Files"). With no tools folder, no such file there, or a path a shell
// would expand, the command stays as it is. Commands that aren't Coral's tools are unchanged.
func ResolveHookCommand(cmd string) string {
	tool, rest, _ := strings.Cut(cmd, " ")
	p := hookToolPath(tool)
	if p == "" {
		return cmd
	}
	// PowerShell also ends a double-quoted string at the curly double quotes U+201C, U+201D and
	// U+201E (#3930, Security's #3927 finding 6, as #3904 did for single quotes).
	if strings.ContainsAny(p, "\"$`\\%!\n“”„") {
		return cmd // never a path a shell would expand inside double quotes
	}
	return withRest(`"`+p+`"`, rest)
}

// ResolveCmdHookCommand is ResolveHookCommand for an agent that runs its hooks as
// cmd.exe /c <command> with Go's argument quoting, as agy does on Windows: that quoting
// turns each " into \", which cmd.exe doesn't read, so a quoted path fails ("'\"C:/Program
// Files (x86)/...\"' is not recognized"). The command names the tool by its absolute path
// without quotes instead: as it is when cmd.exe reads it as one plain word (cmdWord),
// otherwise with its folder's 8.3 short name (C:/PROGRA~2/Coral/...). ok is false when
// neither works (a volume without 8.3 names, a UNC install), or there is no tools folder or
// no such tool in it: then there is no command to install, since a bare name would be found
// in the agent's folder first (cmd.exe searches the current folder before PATH) and a
// repository could ship its own (#3660; #21, Security's #20 F1). Elsewhere it is
// ResolveHookCommand, always ok.
func ResolveCmdHookCommand(cmd string) (string, bool) {
	if runtime.GOOS != "windows" {
		return ResolveHookCommand(cmd), true
	}
	tool, rest, _ := strings.Cut(cmd, " ")
	p := hookToolPath(tool)
	if p == "" {
		return "", false
	}
	if cmdWord(p) {
		return withRest(p, rest), true
	}
	dir, base := path.Split(p)
	// On a volume without 8.3 names GetShortPathNameW returns the long path, which fails
	// cmdWord again.
	if short := filepath.ToSlash(shortPathName(filepath.FromSlash(dir))); short != "" {
		if p := strings.TrimSuffix(short, "/") + "/" + base; cmdWord(p) {
			return withRest(p, rest), true
		}
	}
	return "", false
}

// ResolvePowerShellHookCommand is ResolveHookCommand for an agent that runs its hooks through
// PowerShell, as Codex does on Windows (0.161, #49): there a quoted path is a string
// expression, which PowerShell prints instead of running ("\"C:/.../coral-hook-agentic-state.exe\""
// came back as the hook's output, so the hook never ran and Codex reported "invalid stop hook
// JSON output"). The command calls the tool with PowerShell's call operator on its absolute
// path as a single-quoted literal (& 'C:/Program Files (x86)/Coral/...exe'), in which nothing
// expands. ok is false without a tools folder or the tool in it: no command, never the bare
// name, which would rest on Codex running hooks through PowerShell (PATH only) and not cmd.exe
// (the current folder first, #11; Security's #49 note). Elsewhere it is ResolveHookCommand,
// always ok.
func ResolvePowerShellHookCommand(cmd string) (string, bool) {
	if runtime.GOOS != "windows" {
		return ResolveHookCommand(cmd), true
	}
	tool, rest, _ := strings.Cut(cmd, " ")
	p := hookToolPath(tool)
	if p == "" {
		return "", false
	}
	return withRest("& "+psquote.Literal(p), rest), true
}

// hookToolPath is the absolute path, with forward slashes (sh, cmd.exe and Git Bash all take
// them), of Coral's hook tool in CoralToolsDir, or "" for a name that isn't one of Coral's
// tools, with no tools folder or no such file there.
func hookToolPath(tool string) string {
	if !strings.HasPrefix(tool, "coral-hook-") || strings.ContainsAny(tool, `/\`) {
		return ""
	}
	dir := CoralToolsDir()
	if dir == "" {
		return ""
	}
	p := filepath.Join(dir, tool)
	if runtime.GOOS == "windows" {
		p += ".exe"
	}
	if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	return filepath.ToSlash(p)
}

// cmdWord reports whether cmd.exe reads p, unquoted, as one plain word: only ASCII letters
// and digits, . _ - / : ~ and non-ASCII letters (an allowlist: nothing cmd.exe expands,
// redirects, separates or escapes), and not a UNC path (//server/share), which goes to the
// fail-closed path (#21, Security's #20 F2).
func cmdWord(p string) bool {
	if p == "" || strings.HasPrefix(p, "//") {
		return false
	}
	for _, r := range p {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', strings.ContainsRune("._-/:~", r):
		case r > unicode.MaxASCII && unicode.IsLetter(r):
		default:
			return false
		}
	}
	return true
}

func withRest(cmd, rest string) string {
	if rest != "" {
		return cmd + " " + rest
	}
	return cmd
}

// resolvedHookGroup is a copy of a Coral hook group with each command resolved
// (ResolveHookCommand); the shared table is never changed.
func resolvedHookGroup(group map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(group))
	for k, v := range group {
		out[k] = v
	}
	if hooks, ok := group["hooks"].([]map[string]interface{}); ok {
		copied := make([]map[string]interface{}, len(hooks))
		for i, h := range hooks {
			c := make(map[string]interface{}, len(h))
			for k, v := range h {
				c[k] = v
			}
			if cmd, ok := c["command"].(string); ok {
				c["command"] = ResolveHookCommand(cmd)
			}
			copied[i] = c
		}
		out["hooks"] = copied
	}
	return out
}

// tomlString is s as a TOML basic string, with no whitespace in it: a space is written \u0020
// and a tab \t. The string goes into a -c argument, and an argument without whitespace reaches
// the program as it is from any shell: Windows PowerShell 5.1 wouldn't put quotes around one
// whose spaces all fall inside a TOML string (after an odd number of quotes), so it needed
// psquote.NativeArg's own quoting, which not every argv parser reads alike (#49).
func tomlString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, " ", `\u0020`, "\t", `\t`).Replace(s) + `"`
}
