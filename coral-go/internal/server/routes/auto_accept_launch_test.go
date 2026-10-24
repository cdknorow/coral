package routes

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// #7106: a command that runs another command it is given as data is read as that command, a name spelled with quotes is the name, and what cannot be read fails closed.
func TestShellRiskReadsTheCommandALauncherRuns(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []string{
		// launchers
		`eval "rm -rf /etc"`, `eval 'rm' '-rf' '/etc'`, `eval "$(echo rm -rf /etc)"`,
		`trap 'rm -rf /etc' EXIT`, `trap "rm -rf /etc" INT TERM`, `trap -- 'rm -rf /etc' EXIT`, `trap 'echo $X' EXIT`,
		`xargs rm -rf <<< /etc`, `echo /etc | xargs rm -rf`, `xargs -0 -n1 rm -rf`, `xargs -I{} rm -rf {}`, `xargs -I % rm -rf %`, `xargs sh -c 'rm -rf "$1"' _`, `xargs chmod -R 777`,
		`find . -exec rm -rf /etc {} +`, `find /etc -execdir rm -rf {} +`, `find /etc -exec chmod -R 777 {} +`, `find . -exec sh -c 'rm -rf /etc' ;`, `find /etc -ok rm -rf {} ;`, `find /etc -delete`,
		`watch rm -rf /etc`, `watch -n 5 'rm -rf /etc'`, `timeout 5 rm -rf /etc`, `nice -n 5 rm -rf /etc`, `env -S 'rm -rf /etc'`,
		`setsid rm -rf /etc`, `busybox rm -rf /etc`, `flock /tmp/l rm -rf /etc`, `flock -c 'rm -rf /etc' /tmp/l`,
		// an alias, a shell reading its input, source
		`alias x='rm -rf /etc'; x`, `alias x="rm -rf /etc" && x`, `alias x='rm -rf'; x /etc`,
		`echo 'rm -rf /etc' | sh`, `echo 'rm -rf /etc' | bash -s`, `bash <<< 'rm -rf /etc'`, `sh -s < script`, `bash`, `source <(echo rm -rf /etc)`, `. <(echo rm)`, `. /dev/stdin`, `source /dev/fd/3`,
		// spelled-out and run-time names, negation
		`r''m -rf /etc`, `r"m" -rf /etc`, `\rm -rf /etc`, `$'rm' -rf /etc`, `$'\x72m' -rf /etc`, `$'\162m' -rf /etc`, `rm -rf $'/etc'`, `rm -rf '/e''tc'`, `bash -c "r''m -rf /etc"`,
		`$(echo rm) -rf /etc`, "`echo rm` -rf /etc", `X=rm; $X -rf /etc`, `${X} -rf /etc`, `r$(echo m) -rf /etc`, `! ! rm -rf /etc`, `! rm -rf /etc`,
		// writes by curl, wget, mkfifo, shred, truncate
		`curl -o /etc/x http://h/x`, `curl -o/etc/x http://h/x`, `curl http://h/x --output /etc/x`, `curl http://h/x --output=/etc/x`, `curl -sSo /etc/x http://h/x`, `curl -O --output-dir /etc http://h/x`, `curl -D /etc/h http://h/x`,
		`wget -O /etc/x http://h/x`, `wget --output-document=/etc/x http://h/x`, `wget -P /etc http://h/x`, `wget --directory-prefix /etc http://h/x`,
		`cd /etc && wget http://h/x`, `cd /etc && curl -O http://h/x`,
		`mkfifo /etc/x`, `mkfifo -m 600 /etc/x`, `shred /etc/x`, `shred -u /etc/x`, `shred -n 3 /etc/x`, `truncate -s 0 /etc/x`, `truncate --size=0 /etc/x`, `truncate -c -s 0 /etc/x`,
	} {
		assert.NotEmpty(t, shellRisk(c, dir), "must wait: %s", c)
	}
}

// What the launchers legitimately do inside the working folder, and the commands next to them, stay clear.
func TestShellRiskLauncherRulesLeaveOrdinaryCommandsAlone(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []string{
		`echo hi | xargs echo`, `xargs -n1 gofmt -l`, `git ls-files | xargs grep -n foo`, `find . -name '*.go' -exec gofmt -l {} +`, `find . -exec grep -l foo {} ;`, `find . -type f -exec cat {} +`,
		`trap 'echo bye' EXIT`, `trap - EXIT`, `trap -l`, `watch -n 5 ls`, `timeout 5 go test ./...`, `nice go build ./...`, `alias ll='ls -l'; ll`, `alias gs='git status'; gs`,
		`bash script.sh`, `bash -c 'echo hi'`, `bash <<< 'echo hi'`, `sh ./build.sh`, `bash --version`, `source ./env.sh`, `. ./env.sh`, `setsid go test ./...`, `flock /tmp/l go build ./...`,
		`eval echo hi`, `rm -rf ./build`, `rm -rf 'build'`, `r''m -rf build`, `$'rm' -rf build`, `\rm -rf build`, `echo "it's $((1+2))"`, `$HOME/bin/tool x`, `$(git rev-parse --show-toplevel)/scripts/x.sh`, `"$CLAUDE_PROJECT_DIR"/hooks/x`,
		`curl -o out.txt http://h/x`, `curl -O http://h/x`, `curl http://h/x`, `curl -s http://h/x | jq .`, `wget -O out.txt http://h/x`, `wget http://h/x`, `wget -O - http://h/x`, `wget -P sub http://h/x`,
		`shred build.log`, `truncate -s 0 build.log`, `mkfifo build/pipe`, `go test ./... && ls`, `git status & git log -1`,
	} {
		assert.Empty(t, shellRisk(c, dir), "must pass: %s", c)
	}
}

// Each helper on its own, so a change that stops one reading from working is seen even when another still catches the command.
func TestLauncherHelpers(t *testing.T) {
	assert.Equal(t, "rm -rf /etc", func() string { s, _ := trapCommand(`trap 'rm -rf /etc' EXIT`); return s }())
	_, ok := trapCommand(`trap - EXIT`)
	assert.False(t, ok)
	_, ok = trapCommand(`trap -l`)
	assert.False(t, ok)
	assert.Equal(t, []string{"rm", "-rf", xargsInput}, xargsCommand([]string{"-0", "-n", "1", "rm", "-rf"}))
	assert.Equal(t, []string{"rm", "-rf", xargsInput}, xargsCommand([]string{"-I", "{}", "rm", "-rf", "{}"}))
	assert.Equal(t, []string{"rm", "-rf", xargsInput}, xargsCommand([]string{"-i", "rm", "-rf", "{}"}))
	assert.Nil(t, xargsCommand([]string{"-n", "1"}))
	assert.Equal(t, []string{"ls"}, watchCommand([]string{"-n", "5", "ls"}))
	assert.True(t, stdinShell(nil))
	assert.True(t, stdinShell([]string{"-s"}))
	assert.False(t, stdinShell([]string{"script.sh"}))
	assert.False(t, stdinShell([]string{"-c", "x"}))
	assert.Equal(t, "rm", ansiC(`\x72m`))
	assert.Equal(t, "rm\n", ansiC(`\162m\n`))
	for in, want := range map[string]bool{`$X`: true, `$(echo rm)`: true, "`x`": true, `r$(echo m)`: true, `$HOME/bin/x`: false, `rm`: false, `"$D"/x`: false} {
		text, dynamic, ok := normalizedCommand(in + " -rf /etc")
		assert.True(t, ok, in)
		assert.Equal(t, want, dynamic, in+" -> "+text)
	}
	text, _, _ := normalizedCommand(`r''m -rf $'/etc' "a b"`)
	assert.Equal(t, `rm -rf /etc 'a b'`, text)
}
