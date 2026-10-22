package routes

import (
	"regexp"
	"strings"
)

// PowerShell and cmd commands for auto-accept (#53). shellRisk reads a command the way a POSIX shell does, which
// recognises none of Windows' own destructive commands: `Remove-Item -Recurse -Force C:\Windows`, `del /s /q C:\*`
// and `rd /s /q C:\Users` were all rated safe. winVerbRisk reads a command the way PowerShell and cmd do and says why
// it is high-risk, or "". It is pure text: outside says whether a path argument is outside the working folder (or
// cannot be read), so the table tests run on every OS.
//
// Fail safe: a command it recognises as destructive waits for the user unless every path it names is inside the
// folder; a PowerShell cmdlet it does not know, with -Recurse or -Force or a path outside the folder, waits too.

type winStage struct {
	words []string
	redir []string // targets of > and >>
	sink  bool     // fed by a pipeline
}

// winStages splits a command into stages at ; && || | and newlines outside quotes, and each stage into words
// (single and double quotes group and are dropped, a backtick escapes the next character outside single quotes).
// Redirection targets are taken out of the words.
func winStages(command string) []winStage {
	var stages []winStage
	var cur winStage
	var word strings.Builder
	have := false
	flushWord := func() {
		if !have {
			return
		}
		w := word.String()
		word.Reset()
		have = false
		cur.words = append(cur.words, w)
	}
	flushStage := func(nextSink bool) {
		flushWord()
		cur.words, cur.redir = splitRedirections(cur.words)
		if len(cur.words) > 0 || len(cur.redir) > 0 {
			stages = append(stages, cur)
		}
		cur = winStage{sink: nextSink}
	}
	rs := []rune(command)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case c == '\'' || c == '"':
			have = true
			for i++; i < len(rs) && rs[i] != c; i++ {
				if c == '"' && rs[i] == '`' && i+1 < len(rs) {
					i++
				}
				word.WriteRune(rs[i])
			}
		case c == '`' && i+1 < len(rs):
			i++
			have = true
			word.WriteRune(rs[i])
		case c == ' ' || c == '\t' || c == '\r':
			flushWord()
		case c == '\n' || c == ';':
			flushStage(false)
		case c == '&' && i+1 < len(rs) && rs[i+1] == '&':
			i++
			flushStage(false)
		case c == '&' && !strings.HasSuffix(word.String(), ">") && (have || len(cur.words) > 0):
			// cmd's single & separates commands (echo hi & format C:); PowerShell's call operator is a & that starts a stage, and 2>&1 is a redirection.
			flushStage(false)
		case c == '|' && i+1 < len(rs) && rs[i+1] == '|':
			i++
			flushStage(false)
		case c == '|':
			flushStage(true)
		default:
			have = true
			word.WriteRune(c)
		}
	}
	flushStage(false)
	return stages
}

// winVarCommandRe: a command word that is a cmd variable (%COMSPEC%, %SystemRoot%\\System32\\format.com) or PowerShell's environment variable ($env:ComSpec). A bare %, $_ or $x.Name is
// an alias or an expression in a pipeline, not a command.
var winVarCommandRe = regexp.MustCompile(`^(%[A-Za-z_][A-Za-z0-9_]*%|\$env:[A-Za-z_][A-Za-z0-9_]*$)`)

// winAssignRe: the word after a variable that makes the stage an assignment ($env:FOO = 'x', $x += 1), not a command.
var winAssignRe = regexp.MustCompile(`^[+\-*/%.]?=`)

var winCompareOps = setOf("equ", "neq", "lss", "leq", "gtr", "geq", "not", "exist", "defined", "errorlevel", "cmdextversion")

var redirWordRe = regexp.MustCompile(`^(\d?>>?)(.*)$`)

// splitRedirections takes > >> 2> and their targets out of a stage's words.
func splitRedirections(words []string) (rest, targets []string) {
	for i := 0; i < len(words); i++ {
		m := redirWordRe.FindStringSubmatch(words[i])
		if m == nil || strings.HasPrefix(words[i], ">&") || strings.Contains(words[i], ">&") {
			rest = append(rest, words[i])
			continue
		}
		if m[2] != "" {
			targets = append(targets, m[2])
		} else if i+1 < len(words) {
			i++
			targets = append(targets, words[i])
		}
	}
	return rest, targets
}

// Command classes, by lower-case name without a .exe or .cmd suffix.
var (
	// Removes files; ambiguous names (rm, rmdir, del) are PowerShell's only when the arguments are PowerShell's.
	winDelete = setOf("remove-item", "ri", "rm", "rmdir", "del", "erase", "rd", "clear-content", "clc", "clear-item", "cli", "rimraf")
	// Writes over or moves files.
	winWrite = setOf("move-item", "mi", "mv", "move", "rename-item", "rni", "ren", "rename", "set-content", "add-content", "ac",
		"out-file", "new-item", "ni", "mkdir", "md", "set-item", "si", "copy-item", "cp", "copy", "cpi", "xcopy", "robocopy", "tee-object", "export-csv", "export-clixml")
	// Always high-risk, whatever they are given.
	winSystem = setOf("format-volume", "clear-disk", "initialize-disk", "remove-partition", "diskpart", "stop-computer", "restart-computer",
		"shutdown", "set-executionpolicy", "bcdedit", "takeown", "cipher", "clear-recyclebin", "invoke-expression", "iex",
		"remove-itemproperty", "set-itemproperty", "new-itemproperty", "clear-eventlog", "remove-eventlog", "wevtutil",
		"set-mppreference", "add-mppreference", "disable-netfirewallrule", "remove-netfirewallrule", "netsh", "schtasks",
		"remove-service", "set-service", "stop-service", "new-service", "sc", "net", "net1", "taskkill", "wmic", "mklink",
		"format", "convert", "label", "fsutil", "vssadmin", "mountvol",
		// aliases of the cmdlets above, and tools that change the registry, the environment, file associations, permissions or drives (#5690)
		"sp", "rp", "spsv", "regedit", "setx", "at", "cacls", "xcacls", "subst", "assoc", "ftype",
		// Disk, boot, image and backup tools, secure delete, run-as, and programs that fetch and run code or load a library (#5662): never judged by their arguments.
		"chkdsk", "sfc", "dism", "bootrec", "bootsect", "wbadmin", "diskshadow", "sdelete", "sdelete64", "runas", "certutil", "bitsadmin", "msiexec",
		"regsvr32", "mshta", "rundll32", "wscript", "cscript", "installutil", "cmstp", "odbcconf")
	// Cmdlets that only read or print: never risky for being given -Recurse or a path.
	cmdletRe   = regexp.MustCompile(`^[A-Za-z]+-[A-Za-z]+$`)
	providerRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]+:`) // HKLM:, HKCU:, Env:, Cert:, Function:... (a drive letter is one character)
)

func setOf(names ...string) map[string]bool {
	m := map[string]bool{}
	for _, n := range names {
		m[n] = true
	}
	return m
}

// cmdName is the command word as a name: lower case, without a directory or .exe/.cmd/.bat.
func cmdName(w string) string {
	w = strings.ReplaceAll(w, `\`, "/")
	if i := strings.LastIndex(w, "/"); i >= 0 {
		w = w[i+1:]
	}
	w = strings.ToLower(w)
	for _, ext := range []string{".exe", ".cmd", ".bat", ".com"} {
		w = strings.TrimSuffix(w, ext)
	}
	return w
}

// isParam says whether arg is the parameter that full names, in the abbreviations PowerShell accepts (a unique
// prefix, at least min letters) and, for cmd verbs, the /x switch form.
func isParam(arg, full string, min int) bool {
	if len(arg) < 2 || (arg[0] != '-' && arg[0] != '/') {
		return false
	}
	name := strings.ToLower(strings.TrimLeft(arg, "-/"))
	if i := strings.IndexAny(name, ":="); i >= 0 {
		name = name[:i]
	}
	return len(name) >= min && strings.HasPrefix(full, name)
}

func isRecurse(arg string) bool { return isParam(arg, "recurse", 1) || strings.EqualFold(arg, "/s") }
func isForce(arg string) bool   { return isParam(arg, "force", 1) }

// psStyle: the arguments are PowerShell's (a name like rm, cp or mv is also a POSIX command, judged by the POSIX rules).
func psStyle(args []string) bool {
	for _, a := range args {
		if len(a) > 2 && a[0] == '-' && a[1] != '-' && (isParam(a, "recurse", 2) || isParam(a, "force", 3) || isParam(a, "path", 3) ||
			isParam(a, "literalpath", 3) || isParam(a, "destination", 4) || isParam(a, "confirm", 4)) {
			return true
		}
	}
	return false
}

// winSwitches: the cmd, xcopy and robocopy switches (any one letter, and these). A longer word after a slash is a path.
var winSwitches = func() map[string]bool {
	m := setOf("mir", "purge", "mov", "move", "copyall", "dcopy", "xo", "xn", "xx", "xd", "xf", "xj", "np", "nfl", "ndl", "njh", "njs",
		"nc", "ns", "tee", "mt", "zb", "sec", "secfix", "lev", "maxage", "minage", "create", "unicode", "eta", "fp", "ts", "bytes", "fft", "log", "log+")
	for c := 'a'; c <= 'z'; c++ {
		m[string(c)] = true
	}
	return m
}()

var winPathParams = []struct {
	name string
	min  int
}{{"path", 1}, {"literalpath", 1}, {"destination", 3}, {"filepath", 2}, {"outfile", 3}}

// winPaths are the path arguments of a stage: values of the path parameters, and every word that is not a switch.
func winPaths(args []string) []string { return winPathsMode(args, false) }

// winPathsMode is winPaths; namedOnly keeps just the values of the path parameters (-Path, -LiteralPath, -Destination...).
func winPathsMode(args []string, namedOnly bool) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if len(a) >= 2 && a[0] == '-' {
			name := strings.ToLower(strings.TrimLeft(a, "-"))
			val := ""
			if j := strings.IndexAny(name, ":="); j >= 0 {
				name, val = name[:j], a[len(a)-len(name[j:])+1:]
			}
			for _, p := range winPathParams {
				if len(name) >= p.min && strings.HasPrefix(p.name, name) {
					if val != "" {
						out = append(out, strings.Split(val, ",")...)
					} else if i+1 < len(args) {
						i++
						out = append(out, strings.Split(args[i], ",")...)
					}
					break
				}
			}
			continue
		}
		if a[0] == '/' && winSwitches[strings.ToLower(a[1:])] { // cmd's /s /q /f /y and robocopy's /MIR; /etc is a path
			continue
		}
		if !namedOnly {
			out = append(out, strings.Split(a, ",")...)
		}
	}
	return out
}

// pathOutside: a path argument that is not provably inside the working folder.
func pathOutside(p string, outside func(string) bool) bool {
	p = strings.TrimSpace(p)
	if p == "" {
		return false
	}
	if strings.Contains(p, groupMark) || strings.HasPrefix(p, "@") || strings.HasPrefix(p, "(") {
		return true // a subexpression, a script block or a splat: not a path Coral can read
	}
	if providerRe.MatchString(p) || strings.HasPrefix(p, "\\\\") {
		return true // the registry, the environment, certificates; a share
	}
	return outside(strings.ReplaceAll(p, `\`, "/"))
}

// winVerbRisk is why a PowerShell or cmd command is high-risk, or "".
// winForInCommandRe: for /f's in ('command') or in (`command`): cmd runs the quoted text as a command, but the group reader below takes it for a string.
var winForInCommandRe = regexp.MustCompile("(?i)\\bfor\\b[^()]*\\bin\\s*\\(\\s*(['`])(.*?)['`]\\s*\\)")

func winVerbRisk(command string, outside func(string) bool) string {
	for _, m := range winForInCommandRe.FindAllStringSubmatch(command, -1) {
		if why := winVerbRisk(m[2], outside); why != "" {
			return why
		}
	}
	// The text inside { } ( ) $( ) @( ) is a command of its own: judged on its own, and a group Coral cannot read asks.
	body, inner, ok := extractGroups(command)
	if !ok {
		return "its brackets can't be read"
	}
	for _, in := range inner {
		if why := winVerbRisk(in, outside); why != "" {
			return why
		}
	}
	if winHiddenRe.MatchString(stripQuoted(body)) {
		return "it calls .NET or COM, or a method that deletes, moves or starts something"
	}
	// A cd out of the working folder (or to a place that cannot be read) moves what every later relative path means: after it nothing is provably inside (#5690).
	for _, st := range winStages(body) {
		if why := winStageRisk(st, outside); why != "" {
			return why
		}
		if winMovesOutside(st, outside) {
			outside = func(string) bool { return true }
		}
	}
	return ""
}

// winLocationCmds change the directory later commands start in.
var winLocationCmds = setOf("cd", "chdir", "sl", "set-location", "pushd", "push-location")

// winMovesOutside says whether a stage changes directory to somewhere outside the working folder or somewhere that cannot be read.
func winMovesOutside(st winStage, outside func(string) bool) bool {
	if len(st.words) == 0 || !winLocationCmds[cmdName(st.words[0])] {
		return false
	}
	for _, a := range st.words[1:] {
		if strings.EqualFold(a, "/d") || isParam(a, "path", 1) || isParam(a, "literalpath", 1) || a == "--" {
			continue
		}
		if strings.HasPrefix(a, "-") && !strings.ContainsAny(a, `\/:.$`) {
			continue
		}
		return pathOutside(strings.TrimPrefix(a, "-path:"), outside) || a == "-" || a == "~"
	}
	return false // cd with no target only prints the folder
}

func winStageRisk(st winStage, outside func(string) bool) string {
	for _, t := range st.redir {
		if !isDiscard(t) && pathOutside(t, outside) {
			return "it writes outside the working directory (" + t + ")"
		}
	}
	if len(st.words) == 0 {
		return ""
	}
	words := splitGlued(st.words)
	if strings.Contains(words[0], "^") {
		return "its command is written with cmd escapes (^)"
	}
	// cmd's echo-off prefix (@format C:) and the call operator glued to its command (&cmd /c ...): the command is what follows.
	if len(words[0]) > 1 && (words[0][0] == '@' || words[0][0] == '&') {
		words = append([]string{words[0][1:]}, words[1:]...)
		words = splitGlued(words)
	}
	// A command held in a variable (%COMSPEC% /c format C:, $env:ComSpec ...) cannot be read; an assignment ($x = 1, $x=1) is not a command.
	if first := words[0]; winVarCommandRe.MatchString(first) && !(len(words) > 1 && winAssignRe.MatchString(words[1])) {
		return "its command (" + first + ") comes from a variable Coral can't read"
	}
	words[0] = moduleQualified(words[0])
	// The call operator and dot-sourcing: & 'path' args, . .\script.ps1.
	if words[0] == "&" || words[0] == "." {
		if len(words) == 1 {
			return ""
		}
		words = splitGlued(words[1:])
		words[0] = moduleQualified(words[0])
		if !strings.Contains(words[0], groupMark) && !winKnownCommand(words[0]) && pathOutside(words[0], outside) {
			return "it runs a script outside the working directory (" + words[0] + ")"
		}
		if strings.Contains(words[0], groupMark) {
			return "" // a script block: its text was judged as a command
		}
	}
	name := cmdName(words[0])
	args := words[1:]

	switch name {
	case "powershell", "pwsh", "powershell_ise":
		if why := winShellWrapper(args, outside); why != "" {
			return why
		}
		if st.sink && !winHasCommandArg(args) {
			return "it runs what a pipeline feeds its PowerShell" // curl http://e | powershell
		}
		return ""
	case "call":
		return winVerbRisk(strings.Join(args, " "), outside) // call format C: runs format
	case "for":
		for i, a := range args {
			if strings.EqualFold(a, "do") {
				return winVerbRisk(strings.Join(args[i+1:], " "), outside)
			}
		}
		return ""
	case "forfiles":
		for i, a := range args {
			if strings.EqualFold(a, "/c") && i+1 < len(args) {
				return winVerbRisk(args[i+1], outside)
			}
		}
		return ""
	case "if", "else":
		// if exist x del C:\x: the command is somewhere after the condition, so every tail of the stage is judged.
		for j := 0; j < len(args); j++ {
			if strings.Contains(args[j], "==") || winCompareOps[strings.ToLower(args[j])] {
				continue // a comparison ("%A%"=="1", a equ b), not the command
			}
			if why := winStageRisk(winStage{words: args[j:]}, outside); why != "" {
				return why
			}
		}
		return ""
	case "cmd":
		if st.sink && !winHasCmdCommand(args) {
			return "it runs what a pipeline feeds its cmd"
		}
		for i, a := range args {
			if l := strings.ToLower(a); len(a) > 2 && (strings.HasPrefix(l, "/c") || strings.HasPrefix(l, "/k") || strings.HasPrefix(l, "/r")) {
				// cmd /c"format C:" and cmd /cformat C:: the command text follows the switch with no space.
				rest := strings.Join(append([]string{a[2:]}, args[i+1:]...), " ")
				if strings.Contains(rest, "^") {
					return "its cmd text is written with escapes (^)"
				}
				return winVerbRisk(rest, outside)
			}
			if strings.EqualFold(a, "/c") || strings.EqualFold(a, "/k") || strings.EqualFold(a, "/r") {
				rest := strings.Join(args[i+1:], " ")
				if strings.Contains(rest, "^") {
					return "its cmd text is written with escapes (^)"
				}
				return winVerbRisk(rest, outside)
			}
		}
		return ""
	case "start":
		// cmd's start and PowerShell's alias of Start-Process: the program it starts is judged, with -ArgumentList always asking.
		for i, a := range args {
			if (strings.EqualFold(a, "/d") || isParam(a, "workingdirectory", 2)) && (i+1 >= len(args) || pathOutside(args[i+1], outside)) {
				return "it starts a program in a folder outside the working directory (#5690)"
			}
			if isParam(a, "argumentlist", 2) || strings.EqualFold(a, "-args") {
				return "it starts a program with arguments Coral can't judge"
			}
		}
		for len(args) > 0 && strings.HasPrefix(args[0], "/") && len(args[0]) <= 8 { // start's own switches: /b /wait /min
			args = args[1:]
		}
		if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
			return winVerbRisk(strings.Join(args, " "), outside)
		}
		return ""
	case "start-process", "saps":
		filePath := ""
		for i, a := range args {
			if isParam(a, "workingdirectory", 2) && (i+1 >= len(args) || pathOutside(args[i+1], outside)) {
				return "it starts a process in a folder outside the working directory (#5690)"
			}
			if isParam(a, "verb", 1) && i+1 < len(args) && strings.EqualFold(args[i+1], "runas") {
				return "it starts a process as administrator"
			}
			if isParam(a, "argumentlist", 2) || strings.EqualFold(a, "-args") {
				return "it starts a program with arguments Coral can't judge (S2)"
			}
			if isParam(a, "filepath", 2) && i+1 < len(args) {
				filePath = args[i+1]
			}
		}
		if filePath != "" {
			return winVerbRisk(filePath, outside) // Start-Process -FilePath format.com: the program is judged
		}
		if len(args) > 0 && !strings.HasPrefix(args[0], "-") && winKnownCommand(args[0]) {
			return winVerbRisk(strings.Join(args, " "), outside) // Start-Process cmd, powershell: the program is judged
		}
		return ""
	case "invoke-webrequest", "iwr", "invoke-restmethod", "irm", "curl", "wget":
		for _, a := range args {
			// curl -K file and --config file read more options (an output among them) from a file Coral does not read.
			if name == "curl" && (strings.HasPrefix(a, "-K") || strings.EqualFold(a, "--config") || strings.HasPrefix(strings.ToLower(a), "--config=")) {
				return "it reads curl options from a file Coral can't judge"
			}
			// curl -oPATH and wget -OPATH / -PPATH: the short option with its value glued on.
			if len(a) > 2 && ((name == "curl" && strings.HasPrefix(a, "-o")) || (name == "wget" && (strings.HasPrefix(a, "-O") || strings.HasPrefix(a, "-P")))) && pathOutside(a[2:], outside) {
				return "it writes outside the working directory (" + a[2:] + ")"
			}
			// --output=path, --output-document=path, --directory-prefix=path: the path is glued on.
			if k := strings.IndexByte(a, '='); k > 0 && strings.HasPrefix(a, "--") {
				switch strings.ToLower(a[:k]) {
				case "--output", "--output-document", "--directory-prefix", "--output-dir":
					if pathOutside(a[k+1:], outside) {
						return "it writes outside the working directory (" + a[k+1:] + ")"
					}
				}
			}
		}
		for i, a := range args {
			if i+1 >= len(args) {
				break
			}
			// -OutFile (PowerShell), curl's -o / --output and wget's -O / --output-document / -P: where the download is written.
			if (isParam(a, "outfile", 3) || a == "-o" || strings.EqualFold(a, "--output") || strings.EqualFold(a, "--output-dir") || (name == "wget" && (a == "-O" || strings.EqualFold(a, "--output-document") || a == "-P" || strings.EqualFold(a, "--directory-prefix")))) && pathOutside(args[i+1], outside) {
				return "it writes outside the working directory (" + args[i+1] + ")"
			}
		}
		return "" // piped into iex, the sink is judged
	case "bash", "sh", "zsh", "dash", "ksh":
		// Git Bash and the like, by any path (a path with a space in it too): the -c text is judged, as PowerShell's and cmd's own text is.
		for i, a := range args {
			if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "c") && i+1 < len(args) {
				return winVerbRisk(strings.Join(args[i+1:], " "), outside)
			}
		}
		return ""
	case "tar", "bsdtar":
		for i, a := range args {
			v := ""
			switch {
			case (a == "-C" || a == "--directory") && i+1 < len(args):
				v = args[i+1]
			case strings.HasPrefix(a, "--directory="):
				v = a[len("--directory="):]
			case len(a) > 2 && strings.HasPrefix(a, "-C"):
				v = a[2:]
			}
			if v != "" && pathOutside(v, outside) {
				return "it extracts outside the working directory (" + v + ")"
			}
		}
		return ""
	case "7z", "7za", "7zr":
		for _, a := range args {
			if len(a) > 2 && strings.HasPrefix(a, "-o") && pathOutside(a[2:], outside) {
				return "it extracts outside the working directory (" + a[2:] + ")"
			}
		}
		return ""
	case "unzip":
		for i, a := range args {
			if a == "-d" && i+1 < len(args) && pathOutside(args[i+1], outside) {
				return "it extracts outside the working directory (" + args[i+1] + ")"
			}
		}
		return ""
	case "expand", "makecab":
		if p := winPaths(args); len(p) > 1 && pathOutside(p[len(p)-1], outside) {
			return "it writes outside the working directory (" + p[len(p)-1] + ")"
		}
		return ""
	case "git":
		// A setting that makes git run a program (core.hooksPath, an alias with !, git -c ...): the same rule as the POSIX words (auto_accept_gitcfg.go).
		if why := gitCodeExecRisk(args); why != "" {
			return why
		}
		// git clone <url> <dir>: the directory it creates.
		for i, a := range args {
			if strings.EqualFold(a, "clone") {
				return gitCloneRisk(args[i+1:], outside)
			}
		}
		return ""
	case "attrib":
		for _, p := range winPaths(args) {
			if pathOutside(p, outside) {
				return "it changes attributes outside the working directory (" + p + ")"
			}
		}
		return ""
	case "reg":
		if len(args) > 0 {
			switch strings.ToLower(args[0]) {
			case "delete", "add", "import", "load", "unload", "restore", "copy":
				return "it changes the registry"
			}
		}
		return ""
	case "icacls":
		for _, a := range args {
			l := strings.ToLower(a)
			if strings.HasPrefix(l, "/grant") || strings.HasPrefix(l, "/reset") || strings.HasPrefix(l, "/setowner") || strings.HasPrefix(l, "/deny") || strings.HasPrefix(l, "/remove") {
				return "it changes file permissions"
			}
		}
		return ""
	case "stop-process", "spps", "kill":
		for _, a := range args {
			if isForce(a) {
				return "it force-stops processes"
			}
		}
		return ""
	}
	if winAsk[name] {
		return "it runs " + words[0] + ", which can run anything"
	}
	if name == "sc" && (len(args) == 0 || !winServiceVerbs[strings.ToLower(args[0])]) {
		name = "set-content" // sc is PowerShell 5's alias of Set-Content: judged by the path it writes (#5690)
	}
	if winSystem[name] {
		switch name {
		case "sc", "net", "net1":
			if len(args) == 0 || !winServiceVerbs[strings.ToLower(args[0])] || (strings.EqualFold(args[0], "use") && len(args) == 1) {
				return "" // net use alone only lists the connections
			}
		case "cipher":
			if !hasArgFold(args, "/w") {
				return ""
			}
		case "assoc", "ftype":
			if !strings.Contains(strings.Join(args, " "), "=") {
				return "" // without = they only print
			}
		case "schtasks", "netsh", "wmic", "taskkill", "mklink":
			// only the changing forms
			if !winChangesState(name, args) {
				return ""
			}
		}
		return "it " + winSystemWhy(name)
	}

	// PowerShell uses its own parameters for names a POSIX shell also has.
	ambiguous := name == "rm" || name == "mv" || name == "cp"
	paths := winPaths(args)
	if ambiguous && !psStyle(args) && !st.sink {
		// The POSIX rules judge it, but on Windows these are PowerShell's aliases too: a path that is outside the folder or cannot be read still asks (#5690).
		anyOutside := false
		for _, p := range paths {
			anyOutside = anyOutside || pathOutside(p, outside)
		}
		if !anyOutside {
			return ""
		}
	}
	recurse, force := false, false
	for _, a := range args {
		recurse = recurse || isRecurse(a)
		force = force || isForce(a)
	}

	if name == "xargs" { // xargs hands the pipeline's output to another command: the paths it names are not visible here
		for _, a := range args {
			if v := cmdName(a); winDelete[v] || winWrite[v] || winSystem[v] {
				return "it hands what a pipeline names to " + v + ", not a path inside the working directory"
			}
		}
	}

	if name == "new-item" || name == "ni" {
		for i, a := range args {
			v := strings.ToLower(a)
			if isParam(a, "itemtype", 2) {
				if k := strings.IndexAny(a, ":="); k > 0 {
					v = strings.ToLower(a[k+1:])
				} else if i+1 < len(args) {
					v = strings.ToLower(args[i+1])
				}
				if strings.Contains(v, "junction") || strings.Contains(v, "link") {
					return "it makes a link (junction or symbolic link): what it points at is not judged (#5690)"
				}
			}
			if isParam(a, "target", 3) {
				return "it makes a link: what it points at is not judged (#5690)"
			}
		}
	}

	switch {
	case winDelete[name]:
		if len(paths) == 0 {
			return "it deletes what a pipeline or its defaults name, not a path inside the working directory"
		}
		for _, p := range paths {
			if pathOutside(p, outside) {
				return "it deletes outside the working directory (" + p + ")"
			}
		}
		return ""
	case winWrite[name]:
		if name == "robocopy" && (hasArgFold(args, "/mov") || hasArgFold(args, "/move")) && len(paths) > 0 && pathOutside(paths[0], outside) {
			return "it moves files out of a folder outside the working directory (" + paths[0] + ")" // /mov and /move delete the source
		}
		if name == "robocopy" && (hasArgFold(args, "/mir") || hasArgFold(args, "/purge")) {
			if len(paths) < 2 || pathOutside(paths[1], outside) {
				return "it mirrors over a folder outside the working directory"
			}
		}
		if len(paths) == 0 && !st.sink {
			return "it writes without naming a path inside the working directory"
		}
		copyOnly := name == "copy-item" || name == "cp" || name == "copy" || name == "cpi" || name == "xcopy" || name == "robocopy"
		check := paths
		if copyOnly && len(paths) > 1 {
			check = paths[len(paths)-1:] // reading from outside is not the risk: the destination is
		}
		for _, p := range check {
			if pathOutside(p, outside) {
				return "it writes outside the working directory (" + p + ")"
			}
		}
		return ""
	}

	// Any other PowerShell cmdlet: by its verb. Reading and printing verbs pass; the rest (Stop-Service, Set-Foo,
	// Install-Thing, anything nobody has thought of yet) asks unless every path it names is inside the working folder.
	if cmdletRe.MatchString(words[0]) && !cmdletReadOnly(name) {
		if recurse || force {
			return "it runs " + words[0] + " with -Recurse or -Force"
		}
		named := winPathsMode(args, true) // only a path parameter names a path here: a bare word may be a service, a name
		if len(named) == 0 {
			return "it runs " + words[0] + ", which is not known to only read"
		}
		for _, p := range named {
			if pathOutside(p, outside) {
				return "it runs " + words[0] + " on a path outside the working directory (" + p + ")"
			}
		}
	}
	return ""
}

var winServiceVerbs = setOf("delete", "stop", "config", "create", "failure", "start", "user", "localgroup", "share", "stop", "use") // net use maps a drive or a share

func hasArgFold(args []string, want string) bool {
	for _, a := range args {
		if strings.EqualFold(a, want) || strings.HasPrefix(strings.ToLower(a), strings.ToLower(want)+":") {
			return true
		}
	}
	return false
}

func winChangesState(name string, args []string) bool {
	if len(args) == 0 {
		return false
	}
	joined := " " + strings.ToLower(strings.Join(args, " ")) + " "
	switch name {
	case "schtasks":
		return strings.Contains(joined, " /create ") || strings.Contains(joined, " /delete ") || strings.Contains(joined, " /change ")
	case "netsh":
		return strings.Contains(joined, " add ") || strings.Contains(joined, " delete ") || strings.Contains(joined, " set ") || strings.Contains(joined, " reset ")
	case "wmic":
		return strings.Contains(joined, " delete") || strings.Contains(joined, " call ") || strings.Contains(joined, " set ")
	case "taskkill":
		return strings.Contains(joined, " /f ")
	case "mklink":
		return true
	}
	return false
}

func winSystemWhy(name string) string {
	switch name {
	case "invoke-expression", "iex":
		return "runs text as a command (Invoke-Expression)"
	case "shutdown", "stop-computer", "restart-computer":
		return "shuts down or restarts the machine"
	case "format-volume", "clear-disk", "initialize-disk", "remove-partition", "diskpart":
		return "changes disks"
	case "set-executionpolicy":
		return "changes the execution policy"
	}
	return "changes the system (" + name + ")"
}

// winShellWrapper judges what a nested powershell/pwsh runs: its -Command text, a -File script, and refuses what it
// cannot read (-EncodedCommand).
func winShellWrapper(args []string, outside func(string) bool) string {
	for i := 0; i < len(args); i++ {
		name := strings.ToLower(strings.TrimLeft(args[i], "-/"))
		if args[i] != "" && args[i] != "-" && args[i][0] != '-' && args[i][0] != '/' {
			// powershell Remove-Item -Recurse C:\x: a first argument that is not a switch is the command text.
			return winVerbRisk(strings.Join(args[i:], " "), outside)
		}
		if args[i] == "-" {
			return "it reads its PowerShell command from standard input"
		}
		if args[i] == "" || name == "" {
			continue
		}
		if (name == "wd" || (len(name) >= 2 && strings.HasPrefix("workingdirectory", name))) && (i+1 >= len(args) || pathOutside(args[i+1], outside)) {
			return "it runs in a folder outside the working directory (#5690)"
		}
		switch {
		case name == "e" || name == "ec" || (len(name) >= 2 && strings.HasPrefix("encodedcommand", name)):
			return "it runs an encoded PowerShell command"
		case len(name) >= 2 && strings.HasPrefix("executionpolicy", name), len(name) >= 2 && strings.HasPrefix("windowstyle", name),
			len(name) >= 2 && strings.HasPrefix("inputformat", name), len(name) >= 2 && strings.HasPrefix("outputformat", name),
			len(name) >= 2 && strings.HasPrefix("workingdirectory", name), name == "ep", name == "w", name == "if", name == "of", name == "wd",
			len(name) >= 1 && strings.HasPrefix("version", name), len(name) >= 2 && strings.HasPrefix("psconsolefile", name),
			len(name) >= 2 && strings.HasPrefix("configurationname", name), len(name) >= 2 && strings.HasPrefix("settingsfile", name),
			len(name) >= 2 && strings.HasPrefix("custompipename", name):
			i++ // a switch with a value
		case name == "c" || strings.HasPrefix("command", name):
			if i+1 < len(args) && args[i+1] == "-" {
				return "it reads its PowerShell command from standard input"
			}
			return winVerbRisk(strings.Join(args[i+1:], " "), outside)
		case name == "f" || strings.HasPrefix("file", name):
			if i+1 < len(args) && pathOutside(args[i+1], outside) {
				return "it runs a script outside the working directory (" + args[i+1] + ")"
			}
			return ""
		}
	}
	return ""
}

// isDiscard: a redirection target that throws the output away (/dev/null, NUL, PowerShell's $null) or is another stream
// (&1, /dev/stdout, /dev/stderr), not a file.
func isDiscard(t string) bool {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "/dev/null", "nul", "nul:", "$null", "/dev/stdout", "/dev/stderr", "&1", "&2":
		return true
	}
	return false
}

// winHasCommandArg: a PowerShell host's arguments carry its command (-Command text, -File, a first argument that is not a switch).
func winHasCommandArg(args []string) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "" {
			continue
		}
		if a[0] != '-' && a[0] != '/' {
			return true
		}
		name := strings.ToLower(strings.TrimLeft(a, "-/"))
		if name == "c" || name == "f" || (len(name) >= 1 && (strings.HasPrefix("command", name) || strings.HasPrefix("file", name))) || name == "e" || name == "ec" || (len(name) >= 2 && strings.HasPrefix("encodedcommand", name)) {
			return true
		}
	}
	return false
}

// winHasCmdCommand: cmd's arguments carry its command text (/c, /k or /r).
func winHasCmdCommand(args []string) bool {
	for _, a := range args {
		if l := strings.ToLower(a); strings.HasPrefix(l, "/c") || strings.HasPrefix(l, "/k") || strings.HasPrefix(l, "/r") {
			return true
		}
	}
	return false
}

// gitCloneValueOpts are the options of git clone that take a value in the next word (the value is a branch, a count, a name or a URL, not the folder).
var gitCloneValueOpts = setOf("-b", "--branch", "--depth", "-o", "--origin", "--reference", "--reference-if-able", "--template", "-c", "--config", "-j", "--jobs", "--filter",
	"--shallow-since", "--shallow-exclude", "-u", "--upload-pack", "--server-option", "--bundle-uri", "--revision")

// gitCloneRisk judges git clone's arguments (after "clone"): the second positional word is the folder it creates, and --separate-git-dir names where its .git goes. The repository (a URL, a
// path, git@host:path) and the values of options like -b and --depth are never the folder.
func gitCloneRisk(args []string, outside func(string) bool) string {
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			positional = append(positional, args[i+1:]...)
			i = len(args)
		case a == "--separate-git-dir" && i+1 < len(args):
			if pathOutside(args[i+1], outside) {
				return "it puts a repository's .git outside the working directory (" + args[i+1] + ")"
			}
			i++
		case strings.HasPrefix(a, "--separate-git-dir="):
			if v := a[len("--separate-git-dir="):]; pathOutside(v, outside) {
				return "it puts a repository's .git outside the working directory (" + v + ")"
			}
		case gitCloneValueOpts[a]:
			i++
		case strings.HasPrefix(a, "-"):
			// a flag, or an option with its value glued on (--depth=1, -bmain)
		default:
			positional = append(positional, a)
		}
	}
	if len(positional) > 1 && pathOutside(positional[1], outside) {
		return "it clones into a folder outside the working directory (" + positional[1] + ")"
	}
	return ""
}
