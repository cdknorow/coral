package routes

import (
	"regexp"
	"strings"
)

// Round 2 of #53 (Security's S1-S5): PowerShell reaches a command through syntax a word list does not see, so the reader
// recurses into what is inside brackets, asks outright for the forms that hide a command, and classifies a cmdlet by its
// verb instead of by a list of bad names.

// groupMark stands in a stage's words for a { } block, ( ) group, $( ) or @( ) whose text was judged as a command of
// its own. A path argument that is a group is not readable, so it is outside.
const groupMark = "\x01"

// extractGroups takes the balanced { }, ( ), $( ) and @( ) groups out of command (outside quotes; inside double quotes
// only $( ) is code) and returns the command with each outermost group replaced by groupMark, and the groups' inner
// text. ok is false when the brackets are not balanced: a command Coral cannot read.
func extractGroups(command string) (body string, inner []string, ok bool) {
	rs := []rune(command)
	var out strings.Builder
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case c == '\'':
			out.WriteRune(c)
			for i++; i < len(rs); i++ {
				out.WriteRune(rs[i])
				if rs[i] == '\'' {
					break
				}
			}
		case c == '"':
			out.WriteRune(c)
			for i++; i < len(rs); i++ {
				if rs[i] == '`' && i+1 < len(rs) {
					out.WriteRune(rs[i])
					i++
					out.WriteRune(rs[i])
					continue
				}
				if rs[i] == '$' && i+1 < len(rs) && rs[i+1] == '(' {
					text, end, balanced := groupFrom(rs, i+1)
					if !balanced {
						return "", nil, false
					}
					inner = append(inner, text)
					out.WriteString(groupMark)
					i = end
					continue
				}
				out.WriteRune(rs[i])
				if rs[i] == '"' {
					break
				}
			}
		case c == '`' && i+1 < len(rs):
			out.WriteRune(c)
			i++
			out.WriteRune(rs[i])
		case c == '{' || c == '(':
			text, end, balanced := groupFrom(rs, i)
			if !balanced {
				return "", nil, false
			}
			inner = append(inner, text)
			// $( and @( are one word with their group.
			s := out.String()
			if strings.HasSuffix(s, "$") || strings.HasSuffix(s, "@") {
				out.Reset()
				out.WriteString(s[:len(s)-1])
			}
			out.WriteString(groupMark)
			i = end
		case c == '}' || c == ')':
			return "", nil, false
		default:
			out.WriteRune(c)
		}
	}
	return out.String(), inner, true
}

// groupFrom reads the group that opens at rs[open] and returns its inner text and the index of its closer.
func groupFrom(rs []rune, open int) (text string, end int, ok bool) {
	var stack []rune
	closer := func(o rune) rune {
		if o == '{' {
			return '}'
		}
		return ')'
	}
	for i := open; i < len(rs); i++ {
		switch c := rs[i]; {
		case c == '\'' || c == '"':
			for i++; i < len(rs) && rs[i] != c; i++ {
				if c == '"' && rs[i] == '`' {
					i++
				}
			}
		case c == '`':
			i++
		case c == '{' || c == '(':
			stack = append(stack, closer(c))
		case c == '}' || c == ')':
			if len(stack) == 0 || stack[len(stack)-1] != c {
				return "", 0, false
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				return string(rs[open+1 : i]), i, true
			}
		}
	}
	return "", 0, false
}

// winHiddenRe: text that reaches .NET, COM or a method that deletes, moves or starts something (S3): [IO.File]::Delete(...),
// [Diagnostics.Process]::Start(...), $x.Delete(), (New-Object -ComObject Shell.Application).InvokeVerb("delete").
var winHiddenRe = regexp.MustCompile(`(?i)::|\.\s*(delete|move|moveto|copyto|kill|start|invokeverb|invoke|dispose|create\w*|write\w*|remove\w*)\s*(\x01|\()`)

// winAsk: cmdlets (and aliases) that run something else or reach it a way a word list cannot judge, so they always ask.
var winAsk = setOf("invoke-command", "icm", "start-job", "sajb", "start-threadjob", "invoke-item", "ii", "invoke-wmimethod", "invoke-cimmethod",
	"register-scheduledtask", "register-objectevent", "new-object", "add-type", "invoke-expression", "iex", "invoke-restmethod-unsafe",
	"start-process", "saps", "set-alias", "sal", "new-alias", "nal", "import-module", "ipmo", "install-module", "install-package", "update-help")

// winReadVerbs: the verbs of cmdlets that only read or print. Any other Verb-Noun cmdlet asks unless every path it names is
// inside the working folder.
var winReadVerbs = setOf("get", "select", "where", "sort", "measure", "group", "format", "compare", "test", "resolve", "split", "join",
	"convertto", "convertfrom", "show", "find", "foreach", "write", "start-sleep", "set-location", "push-location", "pop-location")

// winReadCmdlets are whole names: Out-Null and the like are read-only, Out-File is not.
var winReadCmdlets = setOf("out-null", "out-string", "out-host", "out-default", "start-sleep", "set-location", "push-location", "pop-location",
	"write-output", "write-host", "write-verbose", "write-warning", "write-error", "write-information", "write-debug", "write-progress",
	"foreach-object", "where-object", "select-object", "sort-object", "measure-object", "group-object", "tee-object-readonly")

// cmdletReadOnly says whether a Verb-Noun name only reads or prints.
func cmdletReadOnly(name string) bool {
	if winReadCmdlets[name] {
		return true
	}
	verb := name
	if i := strings.IndexByte(name, '-'); i >= 0 {
		verb = name[:i]
	}
	return winReadVerbs[verb]
}

// moduleQualified strips a module path (Microsoft.PowerShell.Management\Remove-Item) from a command word.
func moduleQualified(w string) string {
	if i := strings.LastIndex(w, `\`); i >= 0 && strings.Contains(w[:i], ".") && !strings.ContainsAny(w[:i], ":/ ") && !strings.HasPrefix(w, ".") {
		return w[i+1:]
	}
	return w
}

// splitGlued turns `del/s/q` or `rd/s` into del /s /q: cmd reads the verb up to the slash (S4).
func splitGlued(words []string) []string {
	if len(words) == 0 || strings.ContainsAny(words[0], `\:`) {
		return words
	}
	i := strings.IndexByte(words[0], '/')
	if i <= 0 || !winCmdVerbs[strings.ToLower(words[0][:i])] {
		return words
	}
	out := []string{words[0][:i]}
	for _, sw := range strings.Split(words[0][i:], "/") {
		if sw != "" {
			out = append(out, "/"+sw)
		}
	}
	return append(out, words[1:]...)
}

var winCmdVerbs = setOf("del", "erase", "rd", "rmdir", "copy", "move", "xcopy", "robocopy", "ren", "rename", "format", "cipher", "shutdown",
	"attrib", "takeown", "icacls", "diskpart", "reg", "sc", "net", "taskkill", "schtasks", "bcdedit", "cmd", "powershell", "pwsh")

// winKnownCommand: the word names a command the reader judges (a cmdlet, a cmd verb or a PowerShell host), not a script path.
func winKnownCommand(w string) bool {
	name := cmdName(moduleQualified(w))
	return winDelete[name] || winWrite[name] || winSystem[name] || winAsk[name] || winCmdVerbs[name] || cmdletRe.MatchString(moduleQualified(w))
}

// stripQuoted blanks the text inside quotes: a method name in a string (python -c "f.write(x)") is not a call.
func stripQuoted(s string) string {
	var b strings.Builder
	var q rune
	for _, r := range s {
		switch {
		case q != 0:
			if r == q {
				q = 0
				b.WriteRune(r)
			}
		case r == '\'' || r == '"':
			q = r
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
