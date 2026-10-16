package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
	if !strings.HasPrefix(tool, "coral-hook-") || strings.ContainsAny(tool, `/\`) {
		return cmd
	}
	dir := CoralToolsDir()
	if dir == "" {
		return cmd
	}
	p := filepath.Join(dir, tool)
	if runtime.GOOS == "windows" {
		p += ".exe"
	}
	if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() {
		return cmd
	}
	p = filepath.ToSlash(p)
	if strings.ContainsAny(p, "\"$`\\%!\n") {
		return cmd // never a path a shell would expand inside double quotes
	}
	out := `"` + p + `"`
	if rest != "" {
		out += " " + rest
	}
	return out
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

// tomlString is s as a TOML basic string.
func tomlString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
