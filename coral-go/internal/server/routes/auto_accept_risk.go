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
//   - git reset --hard, git clean (not a dry run), git checkout -- …/./-f and
//     git restore (except --staged only): they destroy uncommitted work
//   - git push --mirror (it overwrites the remote's refs, like a force push)
//   - sudo / doas, also as \sudo, (sudo …), $(sudo …), `sudo …`, /usr/bin/sudo
//   - find … -delete / -exec rm … with a search path outside the working
//     directory, and sed -i on a file outside it
//   - any of the above after a cd out of the working directory in the same
//     command (cd .., cd /tmp, cd ~, cd $X): later paths count from there
//   - chmod -R / chown -R of anything outside the working directory
//   - curl or wget piped into a shell (curl … | sh, bash <(curl …))
//   - a shell write outside the working directory: a > or >> redirection
//     target, tee's files, the destination of cp, install or ln, any path of
//     mv (its sources go away too), dd of= (/dev/null, /dev/stdout,
//     /dev/stderr and /dev/tty are fine)
//   - a file read or write by a tool (Write, Edit, MultiEdit, NotebookEdit,
//     Read, Codex edits) outside the working directory
//
// The command and path come from the PreToolUse hook's tool input when there
// is one, otherwise from the prompt on screen. Paths have symlinks resolved;
// one that cannot be read literally ($VAR, ~, $(…)) counts as outside. The
// patterns are read without a full shell parser, so they err on the side of
// asking: a ">" inside a quoted string can hold a prompt for the user. Other
// shell commands that write elsewhere (touch, mkdir, a script) are not
// inspected for paths.

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
func shellRisk(command, workDir string) string { return shellRiskFrom(command, workDir, workDir) }

// shellRiskFrom is shellRisk for a command that starts in dir (a bash -c after a cd).
func shellRiskFrom(command, workDir, dir string) string {
	if curlPipeRe.MatchString(command) || curlSubstRe.MatchString(command) {
		return "it pipes a download into a shell"
	}
	// The directory later commands run in: a cd out of the working directory moves it
	// ("" = somewhere that cannot be read literally, e.g. cd ~ or cd $X).
	cwd := dir
	at := func(args []string) []string { return fromDir(args, cwd) }
	// ">|" (overwrite despite noclobber) is a redirection, not a pipe.
	for _, seg := range shellSplitRe.Split(strings.ReplaceAll(command, ">|", "> "), -1) {
		seg, targets := redirections(seg)
		if o := outsideTarget(at(notDevices(targets)), workDir); o != "" {
			return "it writes outside the working directory (" + o + ")"
		}
		words := shellWords(seg)
		// Skip env assignments and shell wrappers (bash -lc '…' is split by the caller).
		words = unwrapCommand(words)
		if len(words) == 0 {
			continue
		}
		for _, w := range words {
			if b := filepath.Base(bareCommand(w)); b == "sudo" || b == "doas" {
				return "it uses sudo"
			}
		}
		cmd := filepath.Base(bareCommand(words[0]))
		args := words[1:]
		switch cmd {
		case "cd", "pushd":
			cwd = changeDir(cwd, args)
		case "rm":
			if hasFlag(args, "r", "R", "--recursive") {
				if t := outsideTarget(at(args), workDir); t != "" {
					return "it deletes recursively outside the working directory (" + t + ")"
				}
			}
		case "tee", "cp", "mv", "install", "ln", "dd":
			if t := outsideTarget(at(notDevices(writeTargets(cmd, args))), workDir); t != "" {
				return "it writes outside the working directory (" + t + ")"
			}
		case "find":
			if roots, deletes := findDeletes(args); deletes {
				if t := outsideTarget(at(roots), workDir); t != "" {
					return "it deletes files outside the working directory (" + t + ")"
				}
			}
		case "sed":
			if files, inPlace := sedInPlaceFiles(args); inPlace {
				if t := outsideTarget(at(files), workDir); t != "" {
					return "it edits a file outside the working directory (" + t + ")"
				}
			}
		case "chmod", "chown":
			if hasFlag(args, "R", "", "--recursive") {
				if t := outsideTarget(at(args), workDir); t != "" {
					return "it changes ownership or permissions recursively outside the working directory (" + t + ")"
				}
			}
		case "git":
			sub, rest := gitSubcommand(args)
			switch sub {
			case "push":
				for _, a := range rest {
					if a == "-f" || a == "--force" || a == "--mirror" || strings.HasPrefix(a, "--force-with-lease") || strings.HasPrefix(a, "--force-if-includes") ||
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
			case "clean":
				if !hasFlag(rest, "n", "", "--dry-run") {
					return "it runs git clean (deletes untracked files)"
				}
			case "checkout":
				for i, a := range rest {
					if a == "." || a == "-f" || a == "--force" || (a == "--" && i+1 < len(rest)) {
						return "it discards uncommitted changes (git checkout " + strings.Join(rest, " ") + ")"
					}
				}
			case "restore":
				staged, worktree := false, false
				for _, a := range rest {
					switch {
					case a == "--staged" || a == "-S":
						staged = true
					case a == "--worktree" || a == "-W":
						worktree = true
					case strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "W"):
						worktree = true
					case strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "S"):
						staged = true
					}
				}
				if !staged || worktree {
					return "it discards uncommitted changes (git restore)"
				}
			}
		default:
			// bash -c '…' / zsh -lc '…': inspect the inner command.
			if shellWrapperRe.MatchString(words[0]) {
				for i, a := range args {
					if strings.HasPrefix(a, "-") && strings.Contains(a, "c") && i+1 < len(args) {
						if why := shellRiskFrom(strings.Join(args[i+1:], " "), workDir, cwd); why != "" {
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

// Devices a shell write may target without risk.
var harmlessDevice = map[string]bool{"/dev/null": true, "/dev/stdout": true, "/dev/stderr": true, "/dev/tty": true}

// notDevices drops the harmless devices from a list of written paths.
func notDevices(paths []string) []string {
	var out []string
	for _, p := range paths {
		if !harmlessDevice[p] {
			out = append(out, p)
		}
	}
	return out
}

// redirections removes output redirections (>, >>, >|, &>, 2> …) from a
// command segment and returns their targets. Descriptor duplications (2>&1,
// >&2) and process substitutions (>(…)) are not files.
func redirections(seg string) (string, []string) {
	var targets []string
	b := []byte(seg)
	for i := 0; i < len(b); i++ {
		if b[i] != '>' {
			continue
		}
		// For ">>", the first ">" has no target and the second is read next.
		// "2>&1" and ">&2" duplicate descriptors: "&" ends the target at once.
		j := i + 1
		for j < len(b) && (b[j] == ' ' || b[j] == '\t') {
			j++
		}
		k := j
		for k < len(b) && !strings.ContainsRune(" \t;&|<>", rune(b[k])) {
			k++
		}
		if t := strings.Trim(string(b[j:k]), `'"`); t != "" {
			targets = append(targets, t)
		}
		for x := i; x < k; x++ {
			b[x] = ' '
		}
		i = k - 1
	}
	return string(b), targets
}

// writeTargets returns the paths a file-writing command writes (or, for mv,
// also removes).
func writeTargets(cmd string, args []string) []string {
	var paths []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case cmd == "dd":
			if strings.HasPrefix(a, "of=") {
				paths = append(paths, strings.TrimPrefix(a, "of="))
			}
		case a == "-t" || a == "--target-directory":
			if i+1 < len(args) {
				return []string{args[i+1]}
			}
		case strings.HasPrefix(a, "--target-directory="):
			return []string{strings.TrimPrefix(a, "--target-directory=")}
		case strings.HasPrefix(a, "-"):
		default:
			paths = append(paths, a)
		}
	}
	switch cmd {
	case "tee", "mv", "dd":
		return paths
	case "install":
		if hasFlag(args, "d", "", "--directory") {
			return paths
		}
	}
	if len(paths) < 2 {
		return nil // ln -s target (a link in the current dir); cp/install need a destination
	}
	return paths[len(paths)-1:]
}

// unwrapCommand drops what runs the real command on its behalf, so the checks
// see that command: environment assignments (FOO=1 rm …) and the wrappers env,
// nice, nohup, time, timeout, command, exec, stdbuf and ionice, with their
// options. (xargs is not unwrapped: its targets come from input, a documented
// limit.)
func unwrapCommand(words []string) []string {
	for len(words) > 0 {
		w := words[0]
		if envAssignRe.MatchString(w) {
			words = words[1:]
			continue
		}
		var takesArg map[string]bool // options of the wrapper that take a value
		positional := 0              // leading values after the options (timeout's duration)
		switch filepath.Base(bareCommand(w)) {
		case "env":
			takesArg = map[string]bool{"-u": true, "--unset": true, "-C": true, "--chdir": true, "-S": true, "--split-string": true}
		case "nice":
			takesArg = map[string]bool{"-n": true, "--adjustment": true}
		case "nohup", "command", "builtin":
		case "time":
		case "timeout":
			takesArg = map[string]bool{"-s": true, "--signal": true, "-k": true, "--kill-after": true}
			positional = 1
		case "exec":
			takesArg = map[string]bool{"-a": true}
		case "stdbuf":
			takesArg = map[string]bool{"-i": true, "-o": true, "-e": true}
		case "ionice":
			takesArg = map[string]bool{"-c": true, "-n": true, "-p": true}
		default:
			return words
		}
		words = words[1:]
	opts:
		for len(words) > 0 {
			a := words[0]
			switch {
			case takesArg[a]:
				words = words[min(2, len(words)):]
			case strings.HasPrefix(a, "-") && a != "-":
				words = words[1:]
			case positional > 0:
				positional--
				words = words[1:]
			default:
				break opts
			}
		}
	}
	return words
}

// bareCommand strips what can hide a command name: a leading backslash (\sudo), a
// subshell or substitution opener ((sudo, $(sudo, `sudo, {), a closing ) or `.
func bareCommand(w string) string {
	for {
		t := strings.TrimLeft(w, `\({`+"`")
		t = strings.TrimPrefix(t, "$(")
		if t == w {
			break
		}
		w = t
	}
	return strings.TrimRight(w, ")`}")
}

// fromDir makes relative paths absolute from dir (the directory after any cd). From a
// directory that cannot be read literally (dir == ""), a relative path is outside.
func fromDir(args []string, dir string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if !strings.HasPrefix(a, "-") && !filepath.IsAbs(a) && !strings.ContainsAny(a, "$`~") {
			if dir == "" {
				a = "$CWD/" + a // unknown directory: never a literal path inside
			} else {
				a = filepath.Join(dir, a)
			}
		}
		out = append(out, a)
	}
	return out
}

// changeDir is the directory after cd/pushd args from dir; "" when it cannot be read
// literally (no argument = home, ~, -, $VAR, $(…)).
func changeDir(dir string, args []string) string {
	var target string
	for _, a := range args {
		if a == "-L" || a == "-P" || a == "-e" || a == "-@" {
			continue
		}
		target = a
		break
	}
	if target == "" || target == "-" || dir == "" || strings.ContainsAny(target, "$`~") {
		return ""
	}
	if filepath.IsAbs(target) {
		return filepath.Clean(target)
	}
	return filepath.Join(dir, target)
}

// findDeletes returns find's search roots and whether it deletes what it finds
// (-delete, or -exec/-execdir/-ok running rm, unlink, shred or rmdir).
func findDeletes(args []string) ([]string, bool) {
	var roots []string
	i := 0
	// Options before the search paths: -H, -L, -P, -D debugopts, -Olevel.
	for i < len(args) {
		switch a := args[i]; {
		case a == "-H" || a == "-L" || a == "-P" || strings.HasPrefix(a, "-O"):
			i++
			continue
		case a == "-D":
			i += 2
			continue
		}
		break
	}
	for ; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") || a == "(" || a == "!" {
			break
		}
		roots = append(roots, a)
	}
	if len(roots) == 0 {
		roots = []string{"."}
	}
	for j := i; j < len(args); j++ {
		switch args[j] {
		case "-delete":
			return roots, true
		case "-exec", "-execdir", "-ok", "-okdir":
			if j+1 < len(args) {
				switch filepath.Base(bareCommand(args[j+1])) {
				case "rm", "unlink", "shred", "rmdir":
					return roots, true
				}
			}
		}
	}
	return roots, false
}

// sedInPlaceFiles returns the files sed -i would edit, and whether it edits in place.
func sedInPlaceFiles(args []string) ([]string, bool) {
	inPlace, script := false, false
	var operands []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-e" || a == "-f" || a == "--expression" || a == "--file":
			script = true
			i++
		case strings.HasPrefix(a, "--expression=") || strings.HasPrefix(a, "--file="):
			script = true
		case a == "--in-place" || strings.HasPrefix(a, "--in-place="):
			inPlace = true
		case strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && len(a) > 1:
			if strings.Contains(a, "i") {
				inPlace = true
			}
			if strings.ContainsAny(a, "ef") {
				script = true
				i++
			}
		default:
			operands = append(operands, a)
		}
	}
	if !script && len(operands) > 0 {
		operands = operands[1:] // the first operand is the script
	}
	return operands, inPlace
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
