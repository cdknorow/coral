package routes

import (
	"regexp"
	"strconv"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// Commands that run another command which they are given as data (#7106, on top of #7086's parser). shellRiskCore reads the words of one simple command, so a risky command handed to eval,
// trap, xargs, watch, find -exec, an alias, a shell reading its input or source <( ) was never read at all, and neither was one whose name is spelled with quotes or ANSI-C quoting
// ($'rm', r''m) or built at run time ($(echo rm), $X). Each of these is read as the command it runs, and what cannot be read fails closed: the person decides.

// whyRunTime is the answer for a command whose text is only known when it runs.
const (
	whyRunTimeCommand = "it runs a command that is built at run time"
	whyInputCommand   = "it runs commands read from its input"
)

// runTimeText reports whether command text holds an expansion (a variable, a command substitution, backticks): what it runs is not known until it runs.
func runTimeText(s string) bool {
	return strings.Contains(s, "`") || strings.Contains(s, "$(") || strings.Contains(s, "${") || shellVarRe.MatchString(s)
}

var shellVarRe = regexp.MustCompile(`\$[A-Za-z_0-9@*#?!$-]`)

// xargsInput is what stands for the words xargs reads from its input: a path that cannot be read, so a rule that asks "is the target inside the working folder" says no.
const xargsInput = "$XARGS_INPUT"

// launchedRisk is why a command that runs another command is high-risk ("" when it is not, or is not such a command). cmd and args are the command and its words (wrappers like env and
// nice are already unwrapped), seg is the segment's text (quotes intact), and inner judges a command text in the directory cwd.
func launchedRisk(cmd string, args []string, seg, workDir, cwd string, inner func(command, workDir, dir string) string) string {
	run := func(words ...string) string { return inner(strings.Join(words, " "), workDir, cwd) }
	switch cmd {
	case "eval":
		if len(args) == 0 {
			return ""
		}
		text := strings.Join(args, " ")
		if runTimeText(text) {
			return "it runs a command that eval builds at run time"
		}
		return run(text)
	case "trap":
		if text, ok := trapCommand(seg); ok {
			if runTimeText(text) {
				return "it runs a command that trap builds at run time"
			}
			return run(text)
		}
	case "xargs":
		if words := xargsCommand(args); len(words) > 0 {
			return run(words...)
		}
	case "watch":
		if words := watchCommand(args); len(words) > 0 {
			return run(words...)
		}
	case "find":
		return findExecRisk(args, cwd, func(dir string, words ...string) string { return inner(strings.Join(words, " "), workDir, dir) })
	case "source", ".":
		if len(args) > 0 && (strings.HasPrefix(args[0], "<(") || args[0] == "/dev/stdin" || args[0] == "-" || strings.HasPrefix(args[0], "/dev/fd/") || strings.HasPrefix(args[0], "/proc/self/fd/")) {
			return whyInputCommand
		}
	case "setsid", "busybox", "chrt", "taskset", "flock", "unbuffer":
		if words := launcherCommand(cmd, args); len(words) > 0 {
			return run(words...)
		}
	}
	return ""
}

// launcherCommand is the command a small launcher runs: setsid [-cfw] cmd, busybox applet args, chrt [opts] priority cmd, taskset [opts] mask cmd, flock [opts] file cmd, unbuffer cmd.
func launcherCommand(cmd string, args []string) []string {
	skip := 0
	switch cmd {
	case "chrt", "taskset":
		skip = 1
	case "flock":
		return flockCommand(args)
	}
	var out []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") && a != "-" && len(out) == 0 {
			continue
		}
		if skip > 0 {
			skip--
			continue
		}
		out = append(out, a)
	}
	return out
}

// flockCommand is the command flock runs: flock [options] file|fd command args, or flock [options] file|fd -c 'command'. -w/--timeout/--wait, -E/--conflict-exit-code take a value.
func flockCommand(args []string) []string {
	takes := map[string]bool{"-w": true, "--timeout": true, "--wait": true, "-E": true, "--conflict-exit-code": true}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-c" || a == "--command":
			// The command is the quoted text that follows (cut into words here, with the lock file after it when it comes last): all of it is read as a command line.
			return args[i+1:]
		case strings.HasPrefix(a, "--command="):
			return append([]string{strings.TrimPrefix(a, "--command=")}, args[i+1:]...)
		case takes[a]:
			i++
		case strings.HasPrefix(a, "-") && a != "-":
			// -n -x -s -u -o, --nonblock ... and the attached forms (-w5, --timeout=5)
		default:
			// the lock file or descriptor, then the command or -c 'command'
			rest := args[i+1:]
			if len(rest) > 0 && (rest[0] == "-c" || rest[0] == "--command") {
				return rest[1:]
			}
			return rest
		}
	}
	return nil
}

// trapCommand is the action of a trap command (trap 'action' SIGNAL ...), read from the segment because the action holds spaces; "" and false for trap -l, trap -p, trap - SIGNAL and a trap
// that only lists.
var trapRe = regexp.MustCompile(`(?:^|[\s;&|(])trap\s+(?:--\s+)?(?:'([^']*)'|"((?:[^"\\]|\\.)*)"|(\S+))`)

func trapCommand(seg string) (string, bool) {
	m := trapRe.FindStringSubmatch(seg)
	if m == nil {
		return "", false
	}
	for _, t := range m[1:] {
		if t != "" && t != "-" && !strings.HasPrefix(t, "-l") && !strings.HasPrefix(t, "-p") {
			return t, true
		}
	}
	return "", false
}

// xargsCommand is the command xargs runs, as words with the words it reads from its input stood in by xargsInput: the command's own words, then xargsInput when no replace string is given,
// or xargsInput in place of the replace string.
func xargsCommand(args []string) []string {
	takes := map[string]bool{"-L": true, "-n": true, "-P": true, "-s": true, "-E": true, "-d": true, "-a": true, "-I": true,
		"--arg-file": true, "--delimiter": true, "--max-args": true, "--max-procs": true, "--max-lines": true, "--max-chars": true, "--process-slot-var": true, "--eof": false}
	replace := ""
	i := 0
loop:
	for i < len(args) {
		a := args[i]
		switch {
		case a == "-I":
			if i+1 < len(args) {
				replace = args[i+1]
			}
			i += 2
		case strings.HasPrefix(a, "-I") && !strings.HasPrefix(a, "--"):
			replace = a[2:]
			i++
		case a == "-i" || a == "--replace":
			replace = "{}"
			i++
		case strings.HasPrefix(a, "-i") && !strings.HasPrefix(a, "--"):
			replace = a[2:]
			i++
		case strings.HasPrefix(a, "--replace="):
			replace = strings.TrimPrefix(a, "--replace=")
			i++
		case takes[a]:
			i += 2
		case strings.HasPrefix(a, "-") && a != "-" && a != "--":
			i++
		case a == "--":
			i++
			break loop
		default:
			break loop
		}
	}
	if i >= len(args) {
		return nil
	}
	words := append([]string(nil), args[i:]...)
	if replace == "" {
		return append(words, xargsInput)
	}
	for j, w := range words {
		words[j] = strings.ReplaceAll(w, replace, xargsInput)
	}
	return words
}

// watchCommand is the command watch runs: the words after its options (-n and -i take a value).
func watchCommand(args []string) []string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-n" || a == "--interval" || a == "-w" || a == "--width":
			i++
		case strings.HasPrefix(a, "-") && a != "-":
		default:
			return args[i:]
		}
	}
	return nil
}

// findExecRisk judges the command each -exec, -execdir, -ok and -okdir of a find runs, once for every search root with {} standing for it. -exec and -ok run in the folder find runs in; -execdir and
// -okdir run in the folder of each match, so a relative path there is judged from the search root, and {} is ./name in it.
func findExecRisk(args []string, cwd string, runIn func(dir string, words ...string) string) string {
	roots, _ := findDeletes(args)
	for j := 0; j < len(args); j++ {
		var inMatchDir bool
		switch args[j] {
		case "-exec", "-ok":
		case "-execdir", "-okdir":
			inMatchDir = true
		default:
			continue
		}
		var words []string
		for k := j + 1; k < len(args); k++ {
			if args[k] == ";" || args[k] == "+" || args[k] == `\;` {
				break
			}
			words = append(words, args[k])
		}
		for _, root := range roots {
			dir, match := cwd, root
			if inMatchDir {
				dir, match = changeDir(cwd, []string{root}), "./match"
			}
			sub := make([]string, len(words))
			for n, w := range words {
				sub[n] = strings.ReplaceAll(w, "{}", match)
			}
			if why := runIn(dir, sub...); why != "" {
				return why
			}
		}
	}
	return ""
}

// stdinShell reports whether a shell command line reads its commands from its input (no -c command and no script file, or -s): what it runs is not in the command.
func stdinShell(args []string) bool {
	for _, a := range args {
		if a == "--version" || a == "--help" || (strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "c")) {
			return false
		}
	}
	for _, a := range args {
		if a == "-s" || a == "-" {
			return true
		}
	}
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			return false // a script file
		}
	}
	return true
}

var hereStringRe = regexp.MustCompile(`<<<\s*(?:'([^']*)'|"((?:[^"\\]|\\.)*)"|(\S+))`)

// hereString is the literal text a here-string (<<<) feeds a command, and whether there is one.
func hereString(seg string) (string, bool) {
	m := hereStringRe.FindStringSubmatch(seg)
	if m == nil {
		return "", false
	}
	for _, t := range m[1:] {
		if t != "" {
			return t, true
		}
	}
	return "", true
}

var aliasRe = regexp.MustCompile(`(?:^|[\s;&|(])alias\s+([A-Za-z0-9_.+:-]+)=(?:'([^']*)'|"((?:[^"\\]|\\.)*)"|(\S+))`)

// aliasesIn is the aliases a segment defines (alias name='command'), name to command text.
func aliasesIn(seg string) map[string]string {
	var out map[string]string
	for _, m := range aliasRe.FindAllStringSubmatch(seg, -1) {
		for _, t := range m[2:] {
			if t != "" {
				if out == nil {
					out = map[string]string{}
				}
				out[m[1]] = t
				break
			}
		}
	}
	return out
}

// Options of curl, wget, mkfifo, shred and truncate that name a file the command writes or changes. optionTargets collects the values of the given short and long options, and the operands
// of the commands that write each of theirs.
func optionTargets(args []string, short, long map[string]bool) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case strings.HasPrefix(a, "--"):
			name, val, hasVal := strings.Cut(a, "=")
			if long[name] {
				if hasVal {
					out = append(out, val)
				} else if i+1 < len(args) {
					out = append(out, args[i+1])
					i++
				}
			}
		case strings.HasPrefix(a, "-") && len(a) > 1:
			// A short option, alone (-o FILE) or with its value attached (-oFILE); combined letters (-sSo FILE) end in the one that takes a value.
			for k := 1; k < len(a); k++ {
				if short["-"+a[k:k+1]] {
					if k+1 < len(a) {
						out = append(out, a[k+1:])
					} else if i+1 < len(args) {
						out = append(out, args[i+1])
						i++
					}
					break
				}
			}
		}
	}
	return out
}

func curlRisk(_ string, args []string, at func([]string) []string, workDir string) string {
	targets := optionTargets(args, map[string]bool{"-o": true, "-D": true, "-c": true}, map[string]bool{"--output": true, "--dump-header": true, "--cookie-jar": true, "--trace": true, "--trace-ascii": true, "--stderr": true, "--output-dir": true})
	if hasFlag(args, "O", "J", "--remote-name") || hasFlag(args, "", "", "--remote-name-all") {
		targets = append(targets, ".") // written in the folder it runs in
	}
	if t := outsideTarget(at(notDevices(targets)), workDir); t != "" {
		return "it writes outside the working directory (" + t + ")"
	}
	return ""
}

func wgetRisk(_ string, args []string, at func([]string) []string, workDir string) string {
	targets := optionTargets(args, map[string]bool{"-O": true, "-P": true, "-o": true, "-a": true}, map[string]bool{"--output-document": true, "--directory-prefix": true, "--output-file": true, "--append-output": true, "--save-cookies": true})
	if len(targets) == 0 {
		targets = []string{"."} // written in the folder it runs in
	}
	var real []string
	for _, t := range targets {
		if t != "-" {
			real = append(real, t)
		}
	}
	if t := outsideTarget(at(notDevices(real)), workDir); t != "" {
		return "it writes outside the working directory (" + t + ")"
	}
	return ""
}

// operandRisk judges the operands of a command that writes to or destroys every file it is given (mkfifo, shred, truncate), skipping the options and the values of the ones that take one.
func operandRisk(takes map[string]bool) commandRisk {
	return func(_ string, args []string, at func([]string) []string, workDir string) string {
		var files []string
		for i := 0; i < len(args); i++ {
			a := args[i]
			switch {
			case a == "--":
				files = append(files, args[i+1:]...)
				i = len(args)
			case strings.HasPrefix(a, "--") && !strings.Contains(a, "="):
				if takes[a] {
					i++
				}
			case strings.HasPrefix(a, "-") && len(a) > 1:
				if len(a) == 2 && takes[a] {
					i++
				}
			default:
				files = append(files, a)
			}
		}
		if t := outsideTarget(at(files), workDir); t != "" {
			return "it changes a file outside the working directory (" + t + ")"
		}
		return ""
	}
}

// Normalizing words: the parser knows what a quoted or ANSI-C quoted word spells, the cutting of the text into fields does not.

// normalizedCommand is the command with every word that is spelled out (quotes, backslashes, $'...') written plainly, so r""m, "rm", \rm and $'\x72m' read as rm; dynamic is true when a command
// has a name built at run time (a word that is only an expansion, as $(echo rm) or $X, not a path that starts with one). ok is false when the command cannot be parsed.
func normalizedCommand(command string) (text string, dynamic, ok bool) {
	if len(command) > maxParsedCommand {
		return "", false, false
	}
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil {
		return "", false, false
	}
	syntax.Walk(f, func(n syntax.Node) bool {
		call, isCall := n.(*syntax.CallExpr)
		if !isCall {
			return true
		}
		for i, w := range call.Args {
			if i == 0 && dynamicName(w) {
				dynamic = true
			}
			if v, ok := spelledOut(w); ok {
				rewriteWord(w, v)
			}
		}
		return true
	})
	var b strings.Builder
	if syntax.NewPrinter().Print(&b, f) != nil {
		return "", dynamic, false
	}
	return strings.TrimSpace(b.String()), dynamic, true
}

// dynamicName reports whether a command name is built at run time: it holds an expansion and no literal "/" after it (a path as $HOME/bin/tool or $(git rev-parse --show-toplevel)/x names a program
// in a place; $X, $(echo rm) and r$(echo m) name the program itself).
func dynamicName(w *syntax.Word) bool {
	dyn, slashAfter := false, false
	for _, p := range w.Parts {
		switch p := p.(type) {
		case *syntax.Lit:
			if dyn && strings.Contains(p.Value, "/") {
				slashAfter = true
			}
		case *syntax.SglQuoted:
		case *syntax.DblQuoted:
			for _, q := range p.Parts {
				switch q := q.(type) {
				case *syntax.Lit:
					if dyn && strings.Contains(q.Value, "/") {
						slashAfter = true
					}
				default:
					dyn = true
				}
			}
		default:
			dyn = true
		}
	}
	return dyn && !slashAfter
}

// spelledOut is the text a word spells when all of it is literal (unquoted, single or double quoted without expansions, ANSI-C quoted), and whether it is.
func spelledOut(w *syntax.Word) (string, bool) {
	var b strings.Builder
	plain := true
	for _, p := range w.Parts {
		switch p := p.(type) {
		case *syntax.Lit:
			v := p.Value
			if strings.Contains(v, `\`) {
				plain = false
				v = unbackslash(v)
			}
			b.WriteString(v)
		case *syntax.SglQuoted:
			plain = false
			if p.Dollar {
				b.WriteString(ansiC(p.Value))
			} else {
				b.WriteString(p.Value)
			}
		case *syntax.DblQuoted:
			plain = false
			for _, q := range p.Parts {
				lit, ok := q.(*syntax.Lit)
				if !ok {
					return "", false
				}
				b.WriteString(undouble(lit.Value))
			}
		default:
			return "", false
		}
	}
	return b.String(), !plain
}

var plainWordRe = regexp.MustCompile(`^[A-Za-z0-9_./:=@%+,~-]+$`)

// rewriteWord writes a literal word plainly: as is when it needs no quoting, in single quotes when it does and can, and untouched when neither (a value holding a single quote or a newline).
func rewriteWord(w *syntax.Word, v string) {
	switch {
	case v != "" && plainWordRe.MatchString(v) && !strings.HasPrefix(v, "~"):
		w.Parts = []syntax.WordPart{&syntax.Lit{Value: v}}
	case !strings.ContainsAny(v, "'\n"):
		w.Parts = []syntax.WordPart{&syntax.SglQuoted{Value: v}}
	}
}

// unbackslash drops the backslashes of an unquoted word (\rm is rm, a\ b is "a b").
func unbackslash(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// undouble resolves the backslashes a double-quoted literal can hold: \" \\ \$ and \`.
func undouble(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && strings.IndexByte("\"\\$`", s[i+1]) >= 0 {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// ansiC decodes the escapes of an ANSI-C quoted string ($'...'): \a \b \e \E \f \n \r \t \v \\ \' \" \? \nnn \xHH \uHHHH \UHHHHHHHH. An escape it does not know stays as written.
func ansiC(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch c := s[i]; c {
		case 'a':
			b.WriteByte(7)
		case 'b':
			b.WriteByte(8)
		case 'e', 'E':
			b.WriteByte(27)
		case 'f':
			b.WriteByte(12)
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'v':
			b.WriteByte(11)
		case '\\', '\'', '"', '?':
			b.WriteByte(c)
		case 'x', 'u', 'U':
			max := map[byte]int{'x': 2, 'u': 4, 'U': 8}[c]
			j := i + 1
			for j < len(s) && j-i-1 < max && isHex(s[j]) {
				j++
			}
			if n, err := strconv.ParseUint(s[i+1:j], 16, 32); err == nil && j > i+1 {
				if c == 'x' {
					b.WriteByte(byte(n))
				} else {
					b.WriteRune(rune(n))
				}
				i = j - 1
			} else {
				b.WriteString(`\` + string(c))
			}
		case '0', '1', '2', '3', '4', '5', '6', '7':
			j := i
			for j < len(s) && j-i < 3 && s[j] >= '0' && s[j] <= '7' {
				j++
			}
			n, _ := strconv.ParseUint(s[i:j], 8, 32)
			b.WriteByte(byte(n))
			i = j - 1
		default:
			b.WriteString(`\` + string(c))
		}
	}
	return b.String()
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}
