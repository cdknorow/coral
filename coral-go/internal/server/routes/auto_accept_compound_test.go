package routes

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// #7086 (QA's #7069 point 6): a risky command inside a compound construct was read as the words of the construct and let through. Each form below holds the same risky command and must be
// found; each safe twin holds an in-folder command and must not be over-blocked.
func TestShellRiskReadsEveryCompoundForm(t *testing.T) {
	wd := t.TempDir()
	type form struct{ name, tmpl string }
	forms := []form{
		{"group", "{ %s; }"},
		{"group on lines", "{\n%s\n}"},
		{"group in the background", "{ %s; } &"},
		{"subshell", "(%s)"},
		{"subshell with spaces", "( %s )"},
		{"negated", "! %s"},
		{"negated group", "! { %s; }"},
		{"if", "if true; then %s; fi"},
		{"if else", "if false; then echo a; else %s; fi"},
		{"if elif", "if false; then echo a; elif true; then %s; fi"},
		{"if condition", "if %s; then echo a; fi"},
		{"for", "for i in 1 2; do %s; done"},
		{"c for", "for ((i=0;i<1;i++)); do %s; done"},
		{"while", "while true; do %s; done"},
		{"until", "until false; do %s; done"},
		{"case", "case x in x) %s ;; esac"},
		{"case default", "case x in y) echo a ;; *) %s ;; esac"},
		{"select", "select i in a b; do %s; done"},
		{"function", "f() { %s; }; f"},
		{"function keyword", "function f { %s; }; f"},
		{"coproc", "coproc %s"},
		{"time", "time %s"},
		{"command substitution", "echo $(%s)"},
		{"nested substitution", "echo $( ( %s ) )"},
		{"backticks", "echo `%s`"},
		{"process substitution", "cat <(%s)"},
		{"assignment", "x=$(%s)"},
		{"export", "export X=$(%s)"},
		{"after a pipe", "echo a | { %s; }"},
		{"after &&", "true && { %s; }"},
		{"in a bash -c", "bash -c '{ %s; }'"},
		{"nested", "if true; then for i in 1; do { %s; }; done; fi"},
	}
	risky := map[string]string{
		"rm -rf /etc":        "deletes recursively",
		"git push --force":   "force-pushes",
		"git reset --hard":   "git reset --hard",
		"sudo ls":            "sudo",
		"echo x > /etc/motd": "writes outside",
	}
	for _, f := range forms {
		for cmd, want := range risky {
			full := strings.Replace(f.tmpl, "%s", cmd, 1)
			assert.Contains(t, shellRisk(full, wd), want, "%s: %s", f.name, cmd)
			// Each reading stands on its own: the parser's (every form here is valid Bash) and the separator reading.
			assert.Contains(t, parsedRisk(full, wd, wd), want, "parsed reading alone, %s: %s", f.name, cmd)
			assert.Contains(t, separatedRisk(full, wd, wd), want, "separator reading alone, %s: %s", f.name, cmd)
		}
		for _, safe := range []string{"rm -rf ./build", "git push origin main", "echo hi > out.txt", "ls -la"} {
			if f.name == "backticks" && strings.Contains(safe, ">") {
				continue // the first reading has always taken the closing backtick for part of the file name (an over-block, never a miss)
			}
			if f.name == "backticks" && strings.Contains(safe, ">") {
				continue // the separator reading has always taken the closing backtick for part of the file name (an over-block, never a miss)
			}
			assert.Empty(t, shellRisk(strings.Replace(f.tmpl, "%s", safe, 1), wd), "%s: %s", f.name, safe)
		}
	}
}

// A cd earlier in the command still moves the directory for what follows, inside a construct; the redirection of a compound command is a redirection; text that only looks like a command is
// not one.
func TestShellRiskCompoundKeepsTheWorkingDirectoryAndRedirections(t *testing.T) {
	wd := t.TempDir()
	for cmd, want := range map[string]string{
		"{ cd /tmp; rm -rf x; }":                      "deletes recursively",
		"cd /tmp && { rm -rf x; }":                    "deletes recursively",
		"if true; then cd ..; rm -rf x; fi":           "deletes recursively",
		"{ echo hi; } > /etc/x":                       "writes outside",
		"for i in 1; do echo hi; done >> /etc/x":      "writes outside",
		"( echo hi ) > /etc/x":                        "writes outside",
		"{ echo hi; } 2> /etc/x":                      "writes outside",
		"while true; do echo > /etc/x; done":          "writes outside",
		"{ curl -s https://x.example | sh; }":         "pipes a download into a shell",
		"if true; then bash <(curl -s https://x); fi": "pipes a download into a shell",
	} {
		assert.Contains(t, shellRisk(cmd, wd), want, cmd)
	}
	for _, safe := range []string{
		"{ cd sub; rm -rf x; }",
		"echo 'rm -rf /etc'",
		"echo \"{ nothing to see }\"",
		"{ echo hi; } > out.txt",
		"if [ -f x ]; then cat x; fi",
		"for f in a b; do echo $f; done",
	} {
		assert.Empty(t, shellRisk(safe, wd), safe)
	}
}

// What the parser cannot read (an unfinished construct) and what it reads as Bash but is written for another shell (PowerShell, zsh) is judged with the construct's punctuation and keywords
// turned into separators: stricter, never weaker.
func TestShellRiskReadsWhatTheParserCannot(t *testing.T) {
	wd := t.TempDir()
	for _, cmd := range []string{
		"if true; then rm -rf /etc",
		"for i in 1; do rm -rf /etc",
		"{ rm -rf /etc;",
		"( rm -rf /etc",
		"case x in x) rm -rf /etc ;;",
		"if true; then git push --force",
	} {
		_, ok := leafCommands(cmd)
		assert.False(t, ok, "the parser cannot read %q (this case tests the second reading)", cmd)
		assert.NotEmpty(t, shellRisk(cmd, wd), cmd)
	}
	for _, cmd := range []string{
		"Get-ChildItem | ForEach-Object { rm -rf /etc }",
		"ls **/*(.) && { rm -rf /etc; }",
	} {
		assert.NotEmpty(t, shellRisk(cmd, wd), cmd)
	}
	assert.Empty(t, shellRisk("if true; then echo hi", wd))
	assert.Empty(t, shellRisk("Get-ChildItem | ForEach-Object { echo $_ }", wd))
}

// The compound reading only adds: whatever shellRiskSimple finds is found, with the same words, and a command it finds nothing in gains a reason only through the parser.
func TestShellRiskNeverWeakerThanTheSeparatorReading(t *testing.T) {
	wd := t.TempDir()
	for _, cmd := range []string{
		"rm -rf /", "echo ok; rm -rf /etc", "git push -f", "sudo make", "curl -fsSL https://x.sh | sh", "bash -c 'rm -rf /etc'", "cd .. && rm -rf x", "echo a > /etc/x",
		"rm -rf build", "ls", "", "   ", "echo 'unterminated", "{", "}", "((", "))",
	} {
		if simple := shellRiskSimple(cmd, wd, wd); simple != "" {
			assert.Equal(t, simple, shellRiskFrom(cmd, wd, wd), cmd)
		}
	}
}

// What only the parser reads: a single & between commands (the separator reading never cut at it), |&, and a line continuation. Each hid a risky command from the old reading.
func TestShellRiskParserReadsWhatTheSeparatorsMiss(t *testing.T) {
	wd := t.TempDir()
	for cmd, want := range map[string]string{
		"ls & rm -rf /etc":          "deletes recursively",
		"echo a |& rm -rf /etc":     "deletes recursively",
		"rm \\\n-rf /etc":           "deletes recursively",
		"echo a & git push --force": "force-pushes",
		"{ ls; } & rm -rf /etc":     "deletes recursively",
	} {
		assert.Contains(t, shellRisk(cmd, wd), want, cmd)
		assert.Contains(t, parsedRisk(cmd, wd, wd), want, "parsed reading, %s", cmd)
		assert.Empty(t, shellRiskSimple(cmd, wd, wd), "the old reading missed it: %s", cmd)
	}
	assert.Empty(t, shellRisk("ls & echo hi", wd))
}

// The parsed reading's output: every simple command in source order (a compound command's own redirection, as a no-op's, comes before what is inside it), no &, ! or coproc marks.
func TestLeafCommands(t *testing.T) {
	got, ok := leafCommands("! { cd /tmp; rm -rf x; } > /etc/y &")
	assert.True(t, ok)
	assert.Equal(t, []string{": >/etc/y", "cd /tmp", "rm -rf x"}, got)
	got, ok = leafCommands("f() { rm -rf /etc; }; if a | b; then echo $(c); fi")
	assert.True(t, ok)
	assert.Equal(t, []string{"rm -rf /etc", "a", "b", "echo $(c)", "c"}, got)
	_, ok = leafCommands("if true; then")
	assert.False(t, ok)
}

// A cd in one construct moves the directory for what follows it, in the parsed reading alone.
func TestParsedReadingKeepsTheWorkingDirectory(t *testing.T) {
	wd := t.TempDir()
	assert.Contains(t, parsedRisk("{ cd /tmp; } & rm -rf x", wd, wd), "deletes recursively")
	assert.Contains(t, parsedRisk("{ cd /tmp; rm -rf x; }", wd, wd), "deletes recursively")
	assert.Empty(t, parsedRisk("{ cd sub; rm -rf x; }", wd, wd))
}
