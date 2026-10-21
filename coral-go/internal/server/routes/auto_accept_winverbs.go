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
	winDelete = setOf("remove-item", "ri", "rm", "rmdir", "del", "erase", "rd", "clear-content", "clear-item", "rimraf")
	// Writes over or moves files.
	winWrite = setOf("move-item", "mi", "mv", "move", "rename-item", "rni", "ren", "rename", "set-content", "add-content", "ac",
		"out-file", "new-item", "ni", "set-item", "si", "copy-item", "cp", "copy", "cpi", "xcopy", "robocopy", "tee-object", "export-csv", "export-clixml")
	// Always high-risk, whatever they are given.
	winSystem = setOf("format-volume", "clear-disk", "initialize-disk", "remove-partition", "diskpart", "stop-computer", "restart-computer",
		"shutdown", "set-executionpolicy", "bcdedit", "takeown", "cipher", "clear-recyclebin", "invoke-expression", "iex",
		"remove-itemproperty", "set-itemproperty", "new-itemproperty", "clear-eventlog", "remove-eventlog", "wevtutil",
		"set-mppreference", "add-mppreference", "disable-netfirewallrule", "remove-netfirewallrule", "netsh", "schtasks",
		"remove-service", "set-service", "stop-service", "new-service", "sc", "net", "net1", "taskkill", "wmic", "mklink")
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
func winVerbRisk(command string, outside func(string) bool) string {
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
	for _, st := range winStages(body) {
		if why := winStageRisk(st, outside); why != "" {
			return why
		}
	}
	return ""
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
		return winShellWrapper(args, outside)
	case "cmd":
		for i, a := range args {
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
		for _, a := range args {
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
		for i, a := range args {
			if isParam(a, "verb", 1) && i+1 < len(args) && strings.EqualFold(args[i+1], "runas") {
				return "it starts a process as administrator"
			}
			if isParam(a, "argumentlist", 2) || strings.EqualFold(a, "-args") {
				return "it starts a program with arguments Coral can't judge (S2)"
			}
		}
		if len(args) > 0 && !strings.HasPrefix(args[0], "-") && winKnownCommand(args[0]) {
			return winVerbRisk(strings.Join(args, " "), outside) // Start-Process cmd, powershell: the program is judged
		}
		return ""
	case "invoke-webrequest", "iwr", "invoke-restmethod", "irm", "curl", "wget", "bitsadmin":
		for i, a := range args {
			if isParam(a, "outfile", 3) && i+1 < len(args) && pathOutside(args[i+1], outside) {
				return "it writes outside the working directory (" + args[i+1] + ")"
			}
		}
		return "" // piped into iex, the sink is judged
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
	if winSystem[name] {
		switch name {
		case "sc", "net", "net1":
			if len(args) == 0 || !winServiceVerbs[strings.ToLower(args[0])] {
				return ""
			}
		case "cipher":
			if !hasArgFold(args, "/w") {
				return ""
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
	if ambiguous && !psStyle(args) {
		return "" // the POSIX rules judge it
	}

	paths := winPaths(args)
	recurse, force := false, false
	for _, a := range args {
		recurse = recurse || isRecurse(a)
		force = force || isForce(a)
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

var winServiceVerbs = setOf("delete", "stop", "config", "create", "failure", "start", "user", "localgroup", "share", "stop")

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
		if args[i] == "" || (args[i][0] != '-' && args[i][0] != '/') || name == "" {
			continue
		}
		switch {
		case name == "e" || name == "ec" || (len(name) >= 2 && strings.HasPrefix("encodedcommand", name)):
			return "it runs an encoded PowerShell command"
		case len(name) >= 2 && strings.HasPrefix("executionpolicy", name), len(name) >= 2 && strings.HasPrefix("windowstyle", name),
			len(name) >= 2 && strings.HasPrefix("inputformat", name), len(name) >= 2 && strings.HasPrefix("outputformat", name),
			len(name) >= 2 && strings.HasPrefix("workingdirectory", name):
			i++ // a switch with a value
		case name == "c" || strings.HasPrefix("command", name):
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
