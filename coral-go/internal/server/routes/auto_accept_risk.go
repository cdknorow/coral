package routes

import (
	"path/filepath"
	"regexp"
	"strings"
)

// High-risk actions are never auto-approved (#3182, Coral Hub review E); they
// wait for the user, with an auto_accept_skipped event saying why. The list
// applies to every auto-accept, local or from the Cloud Hub:
//
//   - recursive rm (-r, -R, -rf, --recursive) of anything outside the session's
//     working directory, or of a target that cannot be read literally
//     ($VAR, ~, $(…), globs at the root)
//   - git push --force / -f / --force-with-lease / a +refspec
//   - git reset --hard
//   - sudo
//   - chmod -R / chown -R of anything outside the working directory
//   - curl or wget piped into a shell (curl … | sh, bash <(curl …))
//   - a file read or write by a tool (Write, Edit, MultiEdit, NotebookEdit,
//     Read, Codex edits) outside the working directory
//
// The command and path come from the PreToolUse hook's tool input when there
// is one, otherwise from the prompt on screen. Shell commands are matched by
// these patterns only: a shell command that writes a file elsewhere (touch,
// cp, redirection) is not inspected for paths.

var (
	shellSplitRe   = regexp.MustCompile(`&&|\|\||[;|\n]`)
	curlPipeRe     = regexp.MustCompile(`\b(curl|wget)\b[^|;&]*\|\s*(sudo\s+)?(ba|z|da|k)?sh\b`)
	curlSubstRe    = regexp.MustCompile(`\b(ba|z|da|k)?sh\s+<\(\s*(curl|wget)\b`)
	envAssignRe    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	shellWrapperRe = regexp.MustCompile(`^(/bin/|/usr/bin/)?(ba|z|da|k)?sh$`)
)

// Tools whose path input is read or written.
var autoAcceptFileTools = map[string]string{
	"Write":        "writes",
	"Edit":         "writes",
	"MultiEdit":    "writes",
	"NotebookEdit": "writes",
	"Update":       "writes", // Claude's screen name for Edit
	"Read":         "reads",
}

// shellRisk returns why a shell command is high-risk, or "".
func shellRisk(command, workDir string) string {
	if curlPipeRe.MatchString(command) || curlSubstRe.MatchString(command) {
		return "it pipes a download into a shell"
	}
	for _, seg := range shellSplitRe.Split(command, -1) {
		words := shellWords(seg)
		// Skip env assignments and shell wrappers (bash -lc '…' is split by the caller).
		for len(words) > 0 && envAssignRe.MatchString(words[0]) {
			words = words[1:]
		}
		if len(words) == 0 {
			continue
		}
		for _, w := range words {
			if w == "sudo" || w == "doas" {
				return "it uses sudo"
			}
		}
		cmd := filepath.Base(words[0])
		args := words[1:]
		switch cmd {
		case "rm":
			if hasFlag(args, "r", "R", "--recursive") {
				if t := outsideTarget(args, workDir); t != "" {
					return "it deletes recursively outside the working directory (" + t + ")"
				}
			}
		case "chmod", "chown":
			if hasFlag(args, "R", "", "--recursive") {
				if t := outsideTarget(args, workDir); t != "" {
					return "it changes ownership or permissions recursively outside the working directory (" + t + ")"
				}
			}
		case "git":
			sub, rest := gitSubcommand(args)
			switch sub {
			case "push":
				for _, a := range rest {
					if a == "-f" || a == "--force" || strings.HasPrefix(a, "--force-with-lease") || strings.HasPrefix(a, "--force-if-includes") ||
						(strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "f")) ||
						(strings.HasPrefix(a, "+") && len(a) > 1) {
						return "it force-pushes"
					}
				}
			case "reset":
				for _, a := range rest {
					if a == "--hard" {
						return "it runs git reset --hard"
					}
				}
			}
		default:
			// bash -c '…' / zsh -lc '…': inspect the inner command.
			if shellWrapperRe.MatchString(words[0]) {
				for i, a := range args {
					if strings.HasPrefix(a, "-") && strings.Contains(a, "c") && i+1 < len(args) {
						if why := shellRisk(strings.Join(args[i+1:], " "), workDir); why != "" {
							return why
						}
						break
					}
				}
			}
		}
	}
	return ""
}

// shellWords splits a command segment into words, dropping quotes.
func shellWords(seg string) []string {
	var out []string
	for _, f := range strings.Fields(seg) {
		f = strings.Trim(f, `'"`)
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

// hasFlag reports a short flag letter (alone or combined, e.g. -rf) or a long flag.
func hasFlag(args []string, short1, short2, long string) bool {
	for _, a := range args {
		if a == long {
			return true
		}
		if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") {
			if strings.Contains(a, short1) || (short2 != "" && strings.Contains(a, short2)) {
				return true
			}
		}
	}
	return false
}

// gitSubcommand skips git's global options (-C dir, -c k=v, --git-dir=…).
func gitSubcommand(args []string) (string, []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-C" || a == "-c":
			i++
		case strings.HasPrefix(a, "-"):
		default:
			return a, args[i+1:]
		}
	}
	return "", nil
}

// outsideTarget returns the first non-flag argument that is not a literal
// path inside workDir.
func outsideTarget(args []string, workDir string) string {
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		if strings.ContainsAny(a, "$`~") || !pathInside(a, workDir) {
			return a
		}
	}
	return ""
}

// pathInside reports whether p (absolute, or relative to workDir) is workDir
// or below it. Symlinked prefixes (macOS /tmp -> /private/tmp) are resolved.
func pathInside(p, workDir string) bool {
	if workDir == "" {
		return false
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(workDir, p)
	}
	wd, target := resolvePath(workDir), resolvePath(p)
	rel, err := filepath.Rel(wd, target)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolvePath cleans p and resolves symlinks in its longest existing prefix.
func resolvePath(p string) string {
	p = filepath.Clean(p)
	rest := ""
	for cur := p; ; {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// fileRisk returns why a tool's file access is high-risk, or "".
func fileRisk(tool, path, workDir string) string {
	verb, ok := autoAcceptFileTools[tool]
	if !ok || strings.TrimSpace(path) == "" {
		return ""
	}
	if strings.ContainsAny(path, "$`") || strings.HasPrefix(path, "~") || !pathInside(path, workDir) {
		return "it " + verb + " a file outside the working directory (" + path + ")"
	}
	return ""
}

// autoAcceptRisk returns why the action behind an approval prompt is high-risk
// (so it waits for the user), or "". It checks the PreToolUse input and what
// the prompt shows; either one being high-risk is enough.
func autoAcceptRisk(agentType, workDir string, pending *pendingTool, d approvalDecision, capture string) string {
	if pending != nil {
		if cmd, ok := pending.Input["command"].(string); ok {
			if why := shellRisk(cmd, workDir); why != "" {
				return why
			}
		}
		for _, k := range []string{"file_path", "notebook_path", "path"} {
			if p, ok := pending.Input[k].(string); ok {
				if why := fileRisk(pending.ToolName, p, workDir); why != "" {
					return why
				}
			}
		}
	}
	tool, input := screenAction(agentType, d, capture)
	switch tool {
	case "command", "Bash":
		// A command wrapped over several screen lines: try it joined both ways.
		for _, c := range []string{input, strings.ReplaceAll(input, "\x00", "")} {
			if why := shellRisk(strings.ReplaceAll(c, "\x00", " "), workDir); why != "" {
				return why
			}
		}
	case "edit":
		if why := fileRisk("Write", strings.ReplaceAll(input, "\x00", ""), workDir); why != "" {
			return why
		}
	default:
		if why := fileRisk(tool, input, workDir); why != "" {
			return why
		}
	}
	return ""
}

// screenAction reads the tool and its input from the prompt on screen. Lines
// a long command or path wrapped over are joined with \x00, so the caller can
// try both a space and nothing in between.
func screenAction(agentType string, d approvalDecision, capture string) (string, string) {
	if agentType == "codex" {
		var parts []string
		dest := false
		for _, l := range d.Context {
			switch {
			case codexApprovalHeaderRe.MatchString(l), strings.HasPrefix(l, "Environment:"), strings.HasPrefix(l, "Reason:"),
				strings.HasPrefix(l, "Description:"):
				continue
			case strings.HasPrefix(l, "Destination:"):
				dest = true
				if rest := strings.TrimSpace(strings.TrimPrefix(l, "Destination:")); rest != "" {
					parts = append(parts, rest)
				}
				continue
			}
			parts = append(parts, strings.TrimPrefix(l, "$ "))
		}
		if d.Kind == "edit" || dest {
			return "edit", strings.Join(parts, "\x00")
		}
		return "command", strings.Join(parts, "\x00")
	}
	lines := strings.Split(capture, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if m := claudeToolLineRe.FindStringSubmatch(lines[i]); m != nil {
			return m[1], m[2]
		}
	}
	return "", ""
}
