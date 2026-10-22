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
