package routes

import (
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Git options that run a program are held (#182, Security's #181 F2): set persistently (git config), per command (git -c, --config-env), at clone, or as an alias
// that is a shell command. Harmless settings, reads and unsets stay allowed.
func TestGitSettingsThatRunAProgramAreHeld(t *testing.T) {
	wd := t.TempDir()
	held := []string{
		// persistent, every repository
		"git config --global core.hooksPath /tmp/hooks",
		"git config --system core.hooksPath /tmp/hooks",
		"git config --global core.fsmonitor /tmp/evil",
		"git config --global core.sshCommand 'sh /tmp/evil'",
		"git config --global credential.helper '!sh /tmp/evil'",
		"git config --global credential.helper store",
		"git config --global credential.https://github.com.helper /tmp/evil",
		"git config --global alias.x '!rm -rf $HOME'",
		"git config --global alias.x \"!sh -c 'id'\"",
		"git config --global core.pager 'sh /tmp/evil'",
		"git config --global core.editor /tmp/evil",
		"git config --global diff.external /tmp/evil",
		"git config --global gpg.program /tmp/evil",
		"git config --global include.path /tmp/evil.cfg",
		"git config --global includeIf.gitdir:/.path /tmp/evil.cfg",
		"git config --global filter.x.smudge /tmp/evil",
		"git config --global merge.x.driver /tmp/evil",
		"git config --global --add core.hooksPath /tmp/hooks",
		"git config --global --replace-all core.hooksPath /tmp/hooks",
		"git config --file /tmp/gitconfig core.hooksPath /tmp/hooks",
		"git config set --global core.hooksPath /tmp/hooks",
		"git config --global --edit",
		"git config --system -e",
		// the repository's own config runs it in that repository
		"git config core.hooksPath .githooks",
		"git config core.fsmonitor /tmp/evil",
		"git config --local core.sshCommand evil",
		"git config alias.x '!evil'",
		// case: git keys are not case sensitive
		"git config --global CORE.HOOKSPATH /tmp/hooks",
		"git config --global Core.FsMonitor /tmp/evil",
		// for one command
		"git -c core.hooksPath=/tmp/hooks commit -m x",
		"git -c core.fsmonitor=/tmp/evil status",
		"git -c core.sshCommand=evil fetch",
		"git -c credential.helper='!evil' fetch",
		"git -c alias.x='!evil' x",
		"git -ccore.fsmonitor=/tmp/evil status",
		"git --config-env=core.sshCommand=EVIL fetch",
		"git --config-env core.hooksPath=EVIL status",
		"git -C . -c core.hooksPath=/tmp/hooks status",
		"git --exec-path=/tmp/gitexec status",
		"git clone -c core.fsmonitor=/tmp/evil https://example.com/r.git",
		"git clone --config core.sshCommand=evil https://example.com/r.git",
		"git clone --config=core.hooksPath=/tmp/hooks https://example.com/r.git",
		// the ext:: transport runs the command in the URL (Security's G1)
		"git -c protocol.ext.allow=always clone 'ext::sh -c id' x",
		"git config --global protocol.ext.allow always",
		"git config --global protocol.ext.allow user",
		"git config --global protocol.allow always",
		// other keys that name a program (G2)
		"git config --global pager.log 'sh /tmp/evil'",
		"git -c pager.diff=/tmp/evil diff",
		"git config --global uploadpack.packObjectsHook /tmp/evil",
		"git config --global trailer.x.cmd /tmp/evil",
		"git config --global trailer.x.command /tmp/evil",
		"git config --global interactive.diffFilter /tmp/evil",
		"git config --global sendemail.smtpServer /tmp/evil",
		// --name-only only changes listing output; older gits ignored it with a set (G3)
		"git config --name-only core.hooksPath /tmp/hooks",
		// after other commands, in wrappers and with a program path
		"echo hi && git config --global core.hooksPath /tmp/hooks",
		"bash -c 'git config --global core.hooksPath /tmp/hooks'",
		"/usr/bin/git config --global core.fsmonitor evil",
	}
	for _, c := range held {
		assert.NotEmpty(t, shellRisk(c, wd), "should be held: %s", c)
	}

	allowed := []string{
		"git status",
		"git config --global user.name Bob",
		"git config --global user.email bob@example.com",
		"git config user.name Bob",
		"git config --global alias.co checkout",
		"git config --global alias.lg 'log --oneline --graph'",
		"git config --global core.autocrlf false",
		"git config --global pull.rebase true",
		"git config --global init.defaultBranch main",
		"git config --global push.default simple",
		"git config --global --get core.hooksPath",
		"git config --get core.fsmonitor",
		"git config --global core.hooksPath",
		"git config --global --unset core.hooksPath",
		"git config --global --unset-all credential.helper",
		"git config --list",
		"git config --global -l",
		"git config get core.hooksPath",
		"git config --global credential.helper ''",
		"git config --global core.hooksPath ''",
		"git -c user.name=Bob commit -m x",
		"git -c core.autocrlf=false status",
		"git -c color.ui=always diff",
		"git -c alias.co=checkout co main",
		"git -C /tmp/elsewhere log",
		"git clone https://example.com/r.git",
		"git clone -c core.autocrlf=false https://example.com/r.git",
		"git config --global protocol.ext.allow never",
		"git config --global protocol.allow user",
		"git config --global pager.log true",
		"git config --global pager.log false",
		"git config --global --get pager.log",
		"git config --global --name-only --list",
		"git commit -m 'core.hooksPath is a setting'",
		"git log --grep core.hooksPath",
		"git config --global --edit-nothing-here",
	}
	for _, c := range allowed {
		assert.Empty(t, shellRisk(c, wd), "should be allowed: %s", c)
	}
}

// The same rule through PowerShell's and cmd's words (Windows).
func TestGitSettingsThatRunAProgramAreHeldInPowerShellAndCmd(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the PowerShell and cmd words are read on Windows")
	}
	wd := t.TempDir()
	held := []string{
		`git config --global core.hooksPath 'C:\hooks'`,
		`git config --global core.hooksPath "C:\hooks"`,
		`git.exe config --global core.fsmonitor C:\evil.exe`,
		`GIT.EXE config --system core.sshCommand "C:\evil.cmd"`,
		`git -c core.hooksPath=C:\hooks status`,
		`git -c "core.fsmonitor=C:\evil.exe" status`,
		`& git config --global alias.x '!evil'`,
		`git config --global credential.helper '!C:\evil.exe'`,
		`cd . & git config --global core.hooksPath C:\hooks`,
		`cmd /c git config --global core.hooksPath C:\hooks`,
		`powershell -Command "git config --global core.hooksPath C:\hooks"`,
		`pwsh -c "git -c core.hooksPath=C:\h status"`,
	}
	for _, c := range held {
		assert.NotEmpty(t, shellRisk(c, wd), "should be held: %s", c)
	}
	allowed := []string{
		`git config --global user.name 'Bob Smith'`,
		`git.exe config --global alias.co checkout`,
		`git -c user.name=Bob commit -m x`,
		`git config --global --get core.hooksPath`,
		`git config --global --unset core.hooksPath`,
		`git status`,
	}
	for _, c := range allowed {
		assert.Empty(t, shellRisk(c, wd), "should be allowed: %s", c)
	}
}

// #5934: quotation may start in the middle of a shell word. Keep the key/value
// together, remove only shell syntax, and still allow literal non-program aliases.
func TestGitConfigMidWordQuotesAreHeld(t *testing.T) {
	wd := t.TempDir()
	for _, command := range []string{
		`git -c alias.x='!evil' x`,
		`git -c alias.x="!evil" x`,
		`git -c alias.x=' !evil' x`,
		`git -c alias.x=" !evil" x`,
		`git -calias.x=' !evil' x`,
		`git -c al'ias'.x='!evil' x`,
		`git -c submodule.vendor.update='!evil' submodule update`,
		`git -c submodule.vendor.update=" !evil" submodule update`,
		`git clone -c alias.x='!evil' https://example.com/r.git`,
		`git clone --config=alias.x=" !evil" https://example.com/r.git`,
		`git init --config submodule.vendor.update='!evil'`,
		`git --config-env=core.'sshCommand'=EVIL fetch`,
		`git --config-env 'core.'hooksPath="EVIL" status`,
		`git -c alias.x=\!evil x`,
		`git -c alias.x=\ !evil x`,
		`env X=y git -c alias.x=' !evil' x`,
		`env -S "git -c core.hooksPath=/tmp/hooks status"`,
		`echo ok && git -c alias.x=' !evil' x`,
		`bash -c 'git -c alias.x=" !evil" x'`,
	} {
		assert.NotEmpty(t, shellRisk(command, wd), "should be held: %s", command)
	}
	for _, command := range []string{
		`git -c alias.x='checkout' x`,
		`git -c alias.x="checkout" x`,
		`git -c alias.x=' checkout' x`,
		`git -c alias.x='' status`,
		`git -c core.hooksPath='' status`,
		`git -c protocol.ext.allow='never' status`,
		`git -c pager.diff="false" diff`,
		`git -c alias.x='\!evil' x`, // POSIX single quotes preserve the backslash
		`git -c alias.x="\!evil" x`, // ! is not escapable inside POSIX double quotes
		`git --config-env=user.'name'=NAME status`,
	} {
		assert.Empty(t, shellRisk(command, wd), "should remain allowed: %s", command)
	}
}

// Both modes use the same tokenizer, with their own shell's escape rules. These
// pure Windows classifier calls run on every OS; no Windows command is executed.
func TestGitConfigQuotesUseTheShellsOwnRules(t *testing.T) {
	for _, command := range []string{
		`git -c alias.x='!evil' x`,
		`git -c alias.x=" !evil" x`,
		`git -c submodule.vendor.update=' !evil' submodule update`,
		`git --config-env=core.'sshCommand'=EVIL fetch`,
		"git -c alias.x=`!evil x", // PowerShell backtick escapes !
	} {
		assert.NotEmpty(t, winVerbRisk(command, func(string) bool { return false }), command)
	}
	for _, command := range []string{
		`git -c alias.x='\!evil' x`,
		`git -c alias.x="\!evil" x`,
		`git -c alias.x=\!evil x`, // Windows preserves an unquoted backslash too
		`git -c alias.x='checkout' x`,
		`git --config-env=user.'name'=NAME status`,
	} {
		assert.Empty(t, winVerbRisk(command, func(string) bool { return false }), command)
	}
	cases := []struct {
		command        string
		posix, windows []string
	}{
		{`git -c alias.x='\!evil' x`, []string{"git", "-c", `alias.x=\!evil`, "x"}, []string{"git", "-c", `alias.x=\!evil`, "x"}},
		{`git -c alias.x=\!evil x`, []string{"git", "-c", "alias.x=!evil", "x"}, []string{"git", "-c", `alias.x=\!evil`, "x"}},
		{`git -c alias.x="\!evil" x`, []string{"git", "-c", `alias.x=\!evil`, "x"}, []string{"git", "-c", `alias.x=\!evil`, "x"}},
		{"git -c alias.x=\"\\\"!evil\" x", []string{"git", "-c", "alias.x=\"!evil", "x"}, nil},
		{"git -c alias.x=`!evil x", []string{"git", "-c", "alias.x=`!evil", "x"}, []string{"git", "-c", "alias.x=!evil", "x"}},
	}
	for _, c := range cases {
		stages := commandStages(c.command, true)
		if assert.Len(t, stages, 1, c.command) {
			assert.Equal(t, c.posix, stages[0].words, c.command)
		}
		if c.windows != nil {
			stages = winStages(c.command)
			if assert.Len(t, stages, 1, c.command) {
				assert.Equal(t, c.windows, stages[0].words, c.command)
			}
		}
	}
}

// #5934 round 2: dollar quoting and expansion can hide both the key and a !
// value. The classifier never evaluates these words; they wait for the person.
func TestGitConfigUnresolvedSettingWordsAreHeld(t *testing.T) {
	wd := t.TempDir()
	for _, command := range []string{
		`git -c alias.x=$'!evil' x`,
		`git -c alias.x=$'\x21evil' x`,
		`git -c alias.x=$X x`,
		`git -c alias.x=${X} x`,
		`git -c alias.x=$(echo '!evil') x`,
		"git -c alias.x=`echo '!evil'` x",
		`git -c $'alias.x=!evil' x`,
		`git -c $KEY=value status`,
		`git -c "$PAIR" status`,
		`git -c$PAIR status`,
		`git clone --config alias.x=$X https://example.com/r.git`,
		`git clone --config=alias.x=$'\x21evil' https://example.com/r.git`,
		`git init --config submodule.vendor.update=$X`,
		`git --config-env=$KEY=ENV status`,
		`git --config-env user.name=$ENV_NAME status`,
		`git --config-env=alias.x=ENV x`, // the environment value might itself start with !
		`git --config-env=submodule.vendor.update=ENV submodule update`,
		`git config --global alias.x $'\x21evil'`,
		`git config alias.x $X`,
		`git config --global $KEY value`,
		`git config --global $PAIR`,
		`git config set alias.x $X`,
		`git -c user.name=$USER status`, // known conservative cost accepted in review
	} {
		assert.NotEmpty(t, shellRisk(command, wd), "should be held: %s", command)
	}
	for _, command := range []string{
		`git config --get alias.$X`,
		`git config --unset alias.$X`,
		`git config --get-regexp 'alias.*$'`,
		`git config --global alias.co checkout`,
		`git -c user.name='A Person' status`,
		`git --config-env=user.name=NAME status`,
	} {
		assert.Empty(t, shellRisk(command, wd), "should remain allowed: %s", command)
	}
	for _, command := range []string{
		`git -c alias.x=$X x`,
		`git -c alias.x=$env:COMMAND x`,
		`git --config-env user.name=$ENV_NAME status`,
		`git config --global alias.x $X`,
	} {
		assert.NotEmpty(t, winVerbRisk(command, func(string) bool { return false }), "Windows: %s", command)
	}
}
