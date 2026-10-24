package routes

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// QA #28 C1: on Windows filepath.IsAbs("/etc") is false, so a rooted path was joined onto the working folder and
// judged inside it: `rm -rf /` was rated safe and auto-accept approved it. rootedPath is pure text, so this runs on
// every OS.
func TestRootedPathOnWindows(t *testing.T) {
	for _, tc := range []struct {
		p       string
		rooted  bool
		abs     string
		comment string
	}{
		{"/", true, "", "Git Bash's root (Git's install), not a drive"},
		{"/etc", true, "", ""},
		{"/etc/hosts", true, "", ""},
		{"/usr/local", true, "", ""},
		{"/tmp/elsewhere-xyz", true, "", ""},
		{"/c/Users/x", true, `C:\Users\x`, "Git Bash drive path"},
		{"/d", true, `D:\`, "a drive's root"},
		{"/C/work/x", true, `C:\work\x`, ""},
		{`\Windows`, true, "", "the current drive's root"},
		{`\server\share`, true, "", ""},
		{`\server\share\x`, true, "", "UNC"},
		{"//server/share/x", true, "", "UNC with forward slashes"},
		{`\?\C:\Windows`, true, "", "extended-length"},
		{`C:\Users\x`, true, `C:\Users\x`, ""},
		{"C:/Users/x", true, "C:/Users/x", ""},
		{"C:foo", true, "", "the drive's current folder"},
		{"C:", true, "", "a bare drive"},
		{"build", false, "", ""},
		{"./dist", false, "", ""},
		{`sub\dir`, false, "", ""},
		{"../sibling", false, "", ""},
		{"", false, "", ""},
	} {
		rooted, abs := rootedPath("windows", tc.p)
		assert.Equal(t, tc.rooted, rooted, "%q %s", tc.p, tc.comment)
		assert.Equal(t, tc.abs, abs, "%q %s", tc.p, tc.comment)
	}
	// Unix: rooted is a leading slash, and the path is itself.
	for p, want := range map[string]bool{"/": true, "/etc": true, "build": false, "C:foo": false, `\Windows`: false} {
		rooted, abs := rootedPath("linux", p)
		assert.Equal(t, want, rooted, p)
		if want {
			assert.Equal(t, p, abs)
		}
	}
}

// The forms the audit lists, through the real checks, on Windows. Each is risky unless it is provably inside.
func TestRootedPathsAreOutsideOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the Windows reading of a rooted path")
	}
	wd := t.TempDir()
	for _, cmd := range []string{
		"rm -rf /",
		"echo ok; rm -rf /etc",
		"rm -rf /etc",
		"rm -rf /c/Users/someone-else",
		`rm -rf \Windows`,
		`rm -rf \server\share`,
		"rm -rf //server/share",
		"rm -rf C:foo",
		"rm -rf C:",
		"rm -rf ~",
		"rm -rf ~/x",
		"rm -rf $HOME/x",
		"rm -rf %USERPROFILE%/x",
		"rm -rf %SystemRoot%/x",
		"bash -c 'rm -rf /etc'",
		"/bin/zsh -lc 'rm -rf /etc'",
		"FOO=1 rm -rf /tmp/elsewhere-xyz",
		"cd /etc && rm -rf conf.d",
		"cd /c/Windows && rm -rf Temp",
		"find / -delete",
		"chmod -R 777 /usr/local",
		"chown -R me /opt",
	} {
		assert.NotEmpty(t, shellRisk(cmd, wd), cmd)
	}
	for _, p := range []string{"/etc/hosts", "/", `\Windows\win.ini`, `\server\share\f`, "//server/share/f", "C:foo", "C:", "~/x", "%USERPROFILE%/x", "$HOME/x", "/c/Windows/win.ini"} {
		assert.NotEmpty(t, fileRisk("Write", p, wd), "Write "+p)
		assert.NotEmpty(t, fileRisk("Read", p, wd), "Read "+p)
	}
}

// What is inside the working folder is still safe: relative paths, its own drive path, and a Git Bash /c/... spelling
// of it.
func TestPathsInsideTheWorkingFolderAreStillSafeOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the Windows reading of a rooted path")
	}
	wd := t.TempDir()
	vol := filepath.VolumeName(wd) // C:
	gitBash := "/" + string(vol[0]|0x20) + filepath.ToSlash(wd[len(vol):])
	for _, cmd := range []string{
		"rm -rf build",
		"rm -rf ./dist node_modules",
		"rm -rf sub/dir",
		"rm -r " + filepath.ToSlash(wd) + "/out",
		"rm -r " + gitBash + "/out",
		"cd sub && rm -rf build",
	} {
		assert.Empty(t, shellRisk(cmd, wd), cmd)
	}
	require.NotEmpty(t, vol)
	assert.Empty(t, fileRisk("Write", filepath.Join(wd, "a.txt"), wd))
	assert.Empty(t, fileRisk("Write", gitBash+"/a.txt", wd))
	assert.Empty(t, fileRisk("Write", "sub/a.txt", wd))
	assert.NotEmpty(t, fileRisk("Write", gitBash+"/../elsewhere/a.txt", wd), "a .. out of it")
}

// A working folder this OS cannot resolve (an agent in WSL reports /home/me/proj): only text below it is inside.
func TestDrivelessWorkingFolderIsComparedAsText(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the Windows reading of a rooted path")
	}
	const wd = "/home/me/proj"
	assert.True(t, pathInside("/home/me/proj/src/a.go", wd))
	assert.True(t, pathInside("src/a.go", wd))
	assert.True(t, pathInside(wd, wd))
	assert.False(t, pathInside("/home/me/proj-other/a", wd))
	assert.False(t, pathInside("/home/me/proj/../other", wd))
	assert.False(t, pathInside("/etc/passwd", wd))
	assert.False(t, pathInside(`C:\Windows`, wd))
}

// Security's B1: on Windows a command is also judged with its backslashes as separators (either reading being risky is
// enough, so a native C:...in-folder path is asked about rather than approved, the safe side), since the POSIX word split
// reads `..\..` as the file "...." (rated safe) and `sub\..\..\..` likewise.
func TestBackslashPathsAreJudgedAsPathsOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("backslash paths")
	}
	wd := t.TempDir()
	for _, cmd := range []string{
		`rm -rf ..\..`,
		`rm -rf sub\..\..\..`,
		`rm -rf ..\sibling`,
		`rm -rf "C:\Program Files\x"`,
		`rm -rf 'C:\Program Files\x'`,
		`rm -rf C:\Windows\Temp`,
		`cd ..\.. && rm -rf build`,
		`rm -rf \Windows`,
		`rm -rf \server\share`,
		`chmod -R 777 ..\other`,
	} {
		assert.NotEmpty(t, shellRisk(cmd, wd), cmd)
	}
	for _, cmd := range []string{
		`rm -rf sub\x`,
		`rm -rf .\build`,
		`rm -rf sub\..\x`,
		`rm -rf build\obj dist`,
		`cd sub\deep && rm -rf out`,
	} {
		assert.Empty(t, shellRisk(cmd, wd), cmd)
	}
	assert.Contains(t, shellRisk(`make && \sudo cp x y`, wd), "sudo", `a command-position \sudo is still sudo`)
}

// QA on #46: an input redirection and its operand are not arguments of the command (`tee < in.txt` took "<" as a file
// to write, which fails closed on Windows where "<" is not a name). A real output target still counts.
func TestInputRedirectionsAreNotWriteTargets(t *testing.T) {
	wd := t.TempDir()
	for _, cmd := range []string{"tee < in.txt", "tee <in.txt", "sort < in.txt", "tee out.txt < in.txt", "cat <<< hello", "tee <<EOF", "tee < in.txt >> log.txt"} {
		assert.Empty(t, shellRisk(cmd, wd), cmd)
	}
	for _, cmd := range []string{"tee < in.txt /etc/hosts", "sort < in.txt > /etc/hosts", "cat <<< x | tee /etc/motd"} {
		assert.NotEmpty(t, shellRisk(cmd, wd), cmd)
	}
}

// #7069: a ~ is a shell's text only where it starts a word or follows a ":" or "=" (~, ~user, a path list, an assignment); inside a name it is an ordinary character, as in a folder's
// 8.3 short name (C:\Users\RUNNER~1\...), which used to count as "outside the working folder" and made every in-folder command a risk on a runner whose temp folder is in that form.
func TestUnreadablePathTildeOnlyWhereAShellExpandsIt(t *testing.T) {
	for _, p := range []string{"~", "~/x", "~user/x", `~\x`, "a:~/x", "x=~/y", "$HOME/x", "`pwd`/x", "a/$B", "./~"} {
		if p == "./~" { // a ~ after a separator is a name, not an expansion
			assert.False(t, unreadablePath(p), p)
			continue
		}
		assert.True(t, unreadablePath(p), p)
	}
	for _, p := range []string{`C:\Users\RUNNER~1\AppData\Local\Temp\001\notes.txt`, "/tmp/RUNNER~1/x", "report~.txt", "dir/~backup", "a~b", "build"} {
		assert.False(t, unreadablePath(p), p)
	}
}
