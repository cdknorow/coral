package routes

import (
	"errors"
	"io/fs"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/safefs"
)

// A best-effort safety net, not a guarantee (#3182; operator, 2026-10-13): the
// high-risk actions it recognises wait for the user, with an auto_accept_skipped
// event saying why. It is not a full shell parser, so risky actions can still be
// approved, and the user is told so when turning auto-accept on. The list
// applies to every auto-accept, local or from the Cloud Hub:
//
//   - recursive rm (-r, -R, -rf, --recursive) of anything outside the session's
//     working directory, or of a target that cannot be read literally
//     ($VAR, ~, $(…), globs at the root)
//   - git push --force / -f / --force-with-lease / a +refspec
//   - git reset --hard, git clean (not a dry run), git checkout -- …/./-f and
//     git restore (except --staged only): they destroy uncommitted work
//   - git push --mirror (it overwrites the remote's refs, like a force push)
//   - git push --delete / -d / :branch (deletes a remote branch)
//   - su, and perl -i on a file outside the working directory
//   - sudo / doas (backslash escapes inside a word are undone: s\udo is sudo), also as \sudo, (sudo …), $(sudo …), `sudo …`, /usr/bin/sudo
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
	"Write":                "writes",
	"Edit":                 "writes",
	"MultiEdit":            "writes",
	"NotebookEdit":         "writes",
	"Update":               "writes", // Claude's screen name for Edit
	"Read":                 "reads",
	"write_to_file":        "writes",
	"replace_file_content": "writes",
	"view_file":            "reads",
}

// shellRisk returns why a shell command is high-risk, or "". On Windows the words are split as a POSIX shell's, where
// a backslash escapes the next character, so PowerShell's `rm -rf ..\..` would read as the file "...." and `\Windows` as
// the relative "Windows": the command is judged a second time with every backslash turned into "/", and the first risk
// found is the answer (a path with backslashes is then the path it names).
func shellRisk(command, workDir string) string {
	if why := shellRiskFrom(command, workDir, workDir); why != "" || runtime.GOOS != "windows" || !strings.Contains(command, `\`) {
		return why
	}
	return shellRiskFrom(strings.ReplaceAll(command, `\`, "/"), workDir, workDir)
}

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
		if why := switchesUser(words); why != "" {
			return why
		}
		cmd := filepath.Base(bareCommand(words[0]))
		args := words[1:]
		if cmd == "cd" || cmd == "pushd" {
			cwd = changeDir(cwd, args)
			continue
		}
		if check, ok := shellCommandRisks[cmd]; ok {
			if why := check(cmd, args, at, workDir); why != "" {
				return why
			}
			continue
		}
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
	return ""
}

// switchesUser is why a command's words run something as another user: sudo or doas anywhere (also as \sudo,
// (sudo …), $(sudo …), `sudo …`, /usr/bin/sudo), or su as a command ($(su …), (su …), `su …`), not the word "su".
func switchesUser(words []string) string {
	for _, w := range words {
		if b := filepath.Base(bareCommand(w)); b == "sudo" || b == "doas" {
			return "it uses sudo"
		} else if b == "su" && bareCommand(w) != w {
			return "it switches user (su)"
		}
	}
	return ""
}

// commandRisk is why one command, with its arguments, is high-risk ("" when it isn't). at resolves paths from the
// directory the command runs in.
type commandRisk func(cmd string, args []string, at func([]string) []string, workDir string) string

// shellCommandRisks: the commands shellRiskFrom inspects, each with its rule (cd and the shell wrappers are handled
// there: one moves the directory later commands run in, the other holds a command of its own).
var shellCommandRisks = map[string]commandRisk{
	"rm":      rmRisk,
	"tee":     writeRisk,
	"cp":      writeRisk,
	"mv":      writeRisk,
	"install": writeRisk,
	"ln":      writeRisk,
	"dd":      writeRisk,
	"find":    findRisk,
	"su":      func(string, []string, func([]string) []string, string) string { return "it switches user (su)" },
	"perl":    perlRisk,
	"sed":     sedRisk,
	"chmod":   chmodRisk,
	"chown":   chmodRisk,
	"git":     gitRisk,
}

func rmRisk(_ string, args []string, at func([]string) []string, workDir string) string {
	if hasFlag(args, "r", "R", "--recursive") {
		if t := outsideTarget(at(args), workDir); t != "" {
			return "it deletes recursively outside the working directory (" + t + ")"
		}
	}
	return ""
}

func writeRisk(cmd string, args []string, at func([]string) []string, workDir string) string {
	if t := outsideTarget(at(notDevices(writeTargets(cmd, args))), workDir); t != "" {
		return "it writes outside the working directory (" + t + ")"
	}
	return ""
}

func findRisk(_ string, args []string, at func([]string) []string, workDir string) string {
	if roots, deletes := findDeletes(args); deletes {
		if t := outsideTarget(at(roots), workDir); t != "" {
			return "it deletes files outside the working directory (" + t + ")"
		}
	}
	return ""
}

func perlRisk(_ string, args []string, at func([]string) []string, workDir string) string {
	if files, inPlace := perlInPlaceFiles(args); inPlace {
		if t := outsideTarget(at(files), workDir); t != "" {
			return "it edits a file outside the working directory (" + t + ")"
		}
	}
	return ""
}

func sedRisk(_ string, args []string, at func([]string) []string, workDir string) string {
	if files, inPlace := sedInPlaceFiles(args); inPlace {
		if t := outsideTarget(at(files), workDir); t != "" {
			return "it edits a file outside the working directory (" + t + ")"
		}
	}
	return ""
}

func chmodRisk(_ string, args []string, at func([]string) []string, workDir string) string {
	if hasFlag(args, "R", "", "--recursive") {
		if t := outsideTarget(at(args), workDir); t != "" {
			return "it changes ownership or permissions recursively outside the working directory (" + t + ")"
		}
	}
	return ""
}

// gitRisk: the git subcommands that rewrite a remote or destroy uncommitted work.
func gitRisk(_ string, args []string, _ func([]string) []string, _ string) string {
	sub, rest := gitSubcommand(args)
	switch sub {
	case "push":
		return gitPushRisk(rest)
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
		return gitRestoreRisk(rest)
	}
	return ""
}

// gitPushRisk: a force push (-f, --force, --force-with-lease, --force-if-includes, a short-flag group with f, a
// +refspec, --mirror), or a remote branch deleted (--delete, -d, :branch); the first such argument decides.
func gitPushRisk(args []string) string {
	for _, a := range args {
		if a == "-f" || a == "--force" || a == "--mirror" || strings.HasPrefix(a, "--force-with-lease") || strings.HasPrefix(a, "--force-if-includes") ||
			(strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "f")) ||
			(strings.HasPrefix(a, "+") && len(a) > 1) {
			return "it force-pushes"
		}
		if a == "--delete" || a == "-d" || (strings.HasPrefix(a, ":") && len(a) > 1) {
			return "it deletes a remote branch"
		}
	}
	return ""
}

// gitRestoreRisk: git restore touches the working tree unless it is --staged (-S) only.
func gitRestoreRisk(args []string) string {
	staged, worktree := false, false
	for _, a := range args {
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
		isEnv := false
		switch filepath.Base(bareCommand(w)) {
		case "env":
			// -S / --split-string takes no value here: its value IS the command line (env -S
			// "rm -rf /etc"), already split into words, so -S is skipped like a plain flag and the
			// command follows. An attached value is handled in the option loop below.
			takesArg = map[string]bool{"-u": true, "--unset": true, "-C": true, "--chdir": true}
			isEnv = true
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
			case isEnv && (strings.HasPrefix(a, "--split-string=") || (strings.HasPrefix(a, "-S") && !strings.HasPrefix(a, "--"))):
				// An attached value (-S'rm -rf /etc', --split-string=rm …) starts the command.
				v := strings.Trim(strings.TrimPrefix(strings.TrimPrefix(a, "--split-string="), "-S"), `'"`)
				if v == "" {
					words = words[1:]
				} else {
					words = append([]string{v}, words[1:]...)
				}
				break opts
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
		if !strings.HasPrefix(a, "-") && !rooted(a) && !unreadablePath(a) {
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
	if target == "" || target == "-" || dir == "" || unreadablePath(target) {
		return ""
	}
	if isRooted, abs := rootedPath(runtime.GOOS, target); isRooted {
		if abs == "" {
			return "" // a root with no drive to read (/etc, \Windows, \\server\share, C:foo): not a folder inside this one
		}
		return filepath.Clean(abs)
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

// perlInPlaceFiles returns the files perl -i (-pi, -i.bak, -pie …) would edit, and whether
// it edits in place. The script is -e's value, or else the first operand.
func perlInPlaceFiles(args []string) ([]string, bool) {
	inPlace, script := false, false
	var operands []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			operands = append(operands, args[i+1:]...)
			i = len(args)
		case strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && len(a) > 1:
			// Walk the cluster left to right: a letter that takes a value takes the rest
			// of the cluster, or the next word when it is the cluster's last letter.
			// (-0777's octal digits match nothing: a record separator, not flags.)
		cluster:
			for k := 1; k < len(a); k++ {
				switch c := a[k]; {
				case c == 'i':
					inPlace = true
					break cluster // the rest is -i's backup suffix
				case c == 'e' || c == 'E':
					script = true
					if k == len(a)-1 {
						i++ // the program is the next word (-e 's/a/b/')
					}
					break cluster // else it is attached (-e's/a/b/)
				case strings.IndexByte("IMmlxCdD", c) >= 0:
					break cluster // -Ilib, -Mstrict, -l, -x… (and -0x1F's x): the rest is the value
				}
			}
		default:
			operands = append(operands, a)
		}
	}
	if !script && len(operands) > 0 {
		operands = operands[1:] // the first operand is the script file
	}
	return operands, inPlace
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

// unescapeWord drops shell backslash escapes inside a word (s\udo is sudo, \; is ;).
func unescapeWord(w string) string {
	if !strings.Contains(w, `\`) {
		return w
	}
	var b strings.Builder
	for i := 0; i < len(w); i++ {
		if w[i] == '\\' && i+1 < len(w) {
			i++
		}
		b.WriteByte(w[i])
	}
	return b.String()
}

// shellWords splits a command segment into words, dropping quotes.
func shellWords(seg string) []string {
	var out []string
	for _, f := range strings.Fields(seg) {
		f = unescapeWord(strings.Trim(f, `'"`))
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
		if unreadablePath(a) || !pathInside(a, workDir) {
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
	if wdRooted, wdAbs := rootedPath(runtime.GOOS, workDir); wdRooted && wdAbs == "" {
		// The working folder is itself a path with no drive (an agent in WSL or on another host reports /home/me/proj):
		// nothing here can resolve it, so p is inside only by its text, cleaned, below the folder's text.
		return textInside(p, workDir)
	}
	if isRooted, abs := rootedPath(runtime.GOOS, p); isRooted {
		if abs == "" {
			return false // rooted with no drive to compare (/etc, \Windows, UNC, C:foo): outside, never joined onto workDir
		}
		p = abs
	} else {
		p = filepath.Join(workDir, p)
	}
	wd, target := resolvePath(workDir), resolvePath(p)
	if wd == "" || target == "" {
		return false
	}
	rel, err := filepath.Rel(wd, target)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// textInside is pathInside for a working folder this OS cannot resolve: a relative p is under it, a rooted p only when
// its cleaned text is the folder's or below it. No symlink is followed, so this only ever says "inside" for a path
// written under the folder.
func textInside(p, workDir string) bool {
	wd := path.Clean(strings.ReplaceAll(workDir, `\`, "/"))
	if r, _ := rootedPath("windows", p); r && p[0] != '/' && p[0] != '\\' {
		return false // a path on a drive (C:\x, C:x) is not below a folder that has none
	}
	p = strings.ReplaceAll(p, `\`, "/")
	if r, _ := rootedPath("linux", p); !r {
		p = path.Join(wd, p)
	}
	p = path.Clean(p)
	return p == wd || strings.HasPrefix(p, strings.TrimSuffix(wd, "/")+"/")
}

// resolvePath cleans p and resolves symlinks in its longest existing prefix.
func resolvePath(p string) string {
	p = filepath.Clean(p)
	rest := ""
	for cur := p; ; {
		r, err := safefs.Resolve(cur)
		if err == nil {
			return filepath.Join(r, rest)
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "" // can't be resolved: never taken as inside (#3633)
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
	if strings.ContainsAny(path, "$`") || strings.HasPrefix(path, "~") || unreadablePath(path) || !pathInside(path, workDir) {
		return "it " + verb + " a file outside the working directory (" + path + ")"
	}
	return ""
}

// autoAcceptRisk returns why the action behind an approval prompt is high-risk
// (so it waits for the user), or "". It checks the PreToolUse input and what
// the prompt shows; either one being high-risk is enough.
func autoAcceptRisk(agentType, workDir string, pending *pendingTool, d approvalDecision, capture string) string {
	pendingPath := false // the PreToolUse input names the file: it is the path, the screen only shows it
	if pending != nil {
		cmd, ok := pending.Input["command"].(string)
		if !ok {
			cmd, ok = pending.Input["CommandLine"].(string)
		}
		if ok {
			if why := shellRisk(cmd, workDir); why != "" {
				return why
			}
		}
		for _, k := range []string{"file_path", "notebook_path", "path", "TargetFile", "AbsolutePath"} {
			if p, ok := pending.Input[k].(string); ok && strings.TrimSpace(p) != "" {
				pendingPath = true
				if why := fileRisk(pending.ToolName, p, workDir); why != "" {
					return why
				}
			}
		}
	}
	tool, input := screenAction(agentType, d, capture)
	switch tool {
	case "command", "Bash", "run_command":
		// A command wrapped over several screen lines: try it joined both ways.
		for _, c := range []string{input, strings.ReplaceAll(input, "\x00", "")} {
			if why := shellRisk(strings.ReplaceAll(c, "\x00", " "), workDir); why != "" {
				return why
			}
		}
	case "edit", "write_to_file", "replace_file_content":
		if why := fileRisk("write_to_file", strings.ReplaceAll(input, "\x00", ""), workDir); why != "" {
			return why
		}
	default:
		// A path the screen wrapped is joined without a space: the terminal breaks a
		// long path mid-word, so a space there would make a different, bogus path
		// (outside the repository, refusing every long in-repo path, #3207 QA). When
		// the PreToolUse input named the path, that is authoritative and was checked.
		if !pendingPath {
			if why := fileRisk(tool, strings.ReplaceAll(input, "\x00", ""), workDir); why != "" {
				return why
			}
		}
	}
	return ""
}

// screenAction reads the tool and its input from the prompt on screen. Lines
// a long command or path wrapped over are joined with \x00: a command is tried
// with a space and with nothing in between, a path with nothing.
func screenAction(agentType string, d approvalDecision, capture string) (string, string) {
	switch agentType {
	case at.Codex:
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

	case at.Agy, at.Antigravity, at.Gemini:
		var parts []string
		inCmd := false
		for _, l := range d.Context {
			if agyApprovalHeaderRe.MatchString(l) {
				inCmd = true
				continue
			}
			if agyApprovalQuestionRe.MatchString(l) {
				break
			}
			if inCmd {
				parts = append(parts, strings.TrimSpace(l))
			}
		}
		return "command", strings.Join(parts, "\x00")
	}
	// Claude: the newest tool line above the prompt. A long call wraps over several screen
	// lines, so the newest line that STARTS a call is taken and its continuation lines are
	// joined (with \x00) up to the closing parenthesis. Matching only complete one-line calls
	// fell through a wrapped one to an older call in the scrollback (QA #3207).
	lines := strings.Split(capture, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		m := claudeToolStartRe.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		parts := []string{strings.TrimRight(m[2], " ")}
		for j := i + 1; !strings.HasSuffix(parts[len(parts)-1], ")") && j < len(lines); j++ {
			next := strings.TrimSpace(lines[j])
			if next == "" || ruleLineRe.MatchString(lines[j]) || strings.HasPrefix(next, "⎿") || strings.HasPrefix(next, "⏺") {
				break
			}
			parts = append(parts, next)
		}
		last := len(parts) - 1
		parts[last] = strings.TrimSuffix(parts[last], ")")
		return m[1], strings.Join(parts, "\x00")
	}
	return "", ""
}
