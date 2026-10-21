package executil

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3690 (#3689 finding 3): a URL with '&' and '|' is one argument to the protocol handler on
// Windows, never a cmd.exe command line; on macOS and Linux it is one argument too.
func TestBrowserCommandKeepsTheURLOneArgument(t *testing.T) {
	url := "http://127.0.0.1:8420/auth?token=a&next=/x|calc"
	assert.Equal(t, []string{systemRundll32(), "url.dll,FileProtocolHandler", url}, browserCommand("windows", url))
	assert.Equal(t, []string{"open", url}, browserCommand("darwin", url))
	assert.Equal(t, []string{"xdg-open", url}, browserCommand("linux", url))
	for _, goos := range []string{"windows", "darwin", "linux", "freebsd"} {
		argv := browserCommand(goos, url)
		assert.NotContains(t, argv, "cmd", goos)
		assert.Equal(t, url, argv[len(argv)-1], goos)
	}
}

// What a web page asks the app to open goes through WebURL (#5094): only a plain http or https address reaches the system's opener, which would otherwise
// open or run a file, start a custom scheme's handler or read a leading "-" as an option.
func TestWebURLAcceptsOnlyPlainWebAddresses(t *testing.T) {
	for _, ok := range []string{"https://example.com/a?b=c#d", "http://127.0.0.1:8420/auth?x=1&y=2", "HTTPS://Example.com/", "https://[::1]:8443/x", "https://example.com/a%20b"} {
		got, accepted := WebURL(ok)
		assert.True(t, accepted, ok)
		assert.NotEmpty(t, got, ok)
	}
	for _, bad := range []string{
		"", "-a", "-a /Applications/Calculator.app", "--args", "/Applications/Calculator.app", "/tmp/x.command", "x.command", "file:///tmp/x.command", "FILE:///etc/passwd",
		"C:\\Windows\\System32\\calc.exe", "\\\\host\\share\\x.exe", "ftp://example.com/", "ssh://host", "smb://host/share", "vnc://host", "x-apple.systempreferences:", "tel:123",
		"mailto:a@b.c", "javascript:alert(1)", "data:text/html,x", "itms-services://?action=x", "http://", "https:///path", "http:example.com", "https://user:pw@example.com/",
		"https://example.com/ x", "https://example.com/\n", "https://example.com/\t", "https://exam ple.com", "https://example.com\\@evil.example", "//example.com/x", "  https://example.com",
		"https://example.com/" + string(make([]byte, 10)), "https://example.com/" + strings.Repeat("a", maxWebURL),
	} {
		_, accepted := WebURL(bad)
		assert.False(t, accepted, "%q", bad)
	}
}

func FuzzWebURL(f *testing.F) {
	for _, s := range []string{"https://example.com", "-a", "file:///x", "http://[::1]/", "x:y", "https://u@h/"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		got, ok := WebURL(raw)
		if !ok {
			return
		}
		if strings.HasPrefix(got, "-") || !(strings.HasPrefix(got, "http://") || strings.HasPrefix(got, "https://")) {
			t.Fatalf("%q accepted as %q", raw, got)
		}
		for i := 0; i < len(got); i++ {
			if got[i] <= 0x20 || got[i] == 0x7f {
				t.Fatalf("%q accepted with a control or space byte in %q", raw, got)
			}
		}
	})
}

// #78 T1: on Windows the opener is the system folder's rundll32.
func TestWindowsBrowserCommandUsesTheSystemRundll32(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the absolute path is Windows' only")
	}
	argv := browserCommand("windows", "https://example.com/")
	// The system folder Windows reports (GetSystemDirectory), not the SystemRoot variable; its case is Windows' own (system32), so compare without case.
	assert.True(t, filepath.IsAbs(argv[0]), argv[0])
	assert.True(t, strings.HasSuffix(strings.ToLower(argv[0]), `\system32\rundll32.exe`), argv[0])
}

// OpenFile shows only an absolute, regular .log or .txt file: never a program, a link, a relative
// path, an option or a web address.
func TestOpenFileOnlyOpensAnAbsoluteRegularLogInsideTheDataDir(t *testing.T) {
	dir := t.TempDir()
	other := t.TempDir()
	exe := filepath.Join(dir, "evil.exe")
	require.NoError(t, os.WriteFile(exe, []byte("x"), 0o700))
	outside := filepath.Join(other, "elsewhere.log")
	require.NoError(t, os.WriteFile(outside, []byte("x"), 0o600))
	for _, bad := range []string{"", "tray.log", "-a", exe, filepath.Join(dir, "gone.log"), dir, "https://example.com/x.log", filepath.Join(dir, "tray.log.exe"),
		outside, filepath.Join(dir, "..", filepath.Base(other), "elsewhere.log")} {
		assert.False(t, OpenFile(dir, bad), "%q", bad)
	}
	assert.False(t, OpenFile("", filepath.Join(dir, "x.log")), "no folder, nothing opens")
	assert.False(t, OpenFile("relative", filepath.Join(dir, "x.log")), "a relative folder is refused")
}

// insideDir is the confinement OpenFile uses: below the folder, cleaned, and not the folder itself.
func TestInsideDir(t *testing.T) {
	d := filepath.Join(string(filepath.Separator), "data", "coral")
	if runtime.GOOS == "windows" {
		d = `C:\Users\u\.coral`
	}
	assert.True(t, insideDir(d, filepath.Join(d, "tray.log")))
	assert.True(t, insideDir(d, filepath.Join(d, "logs", "app.log")))
	assert.False(t, insideDir(d, d))
	assert.False(t, insideDir(d, filepath.Join(d, "..", "x.log")))
	assert.False(t, insideDir(d, d+"2"+string(filepath.Separator)+"x.log"), "a sibling with the same prefix is not inside")
	if runtime.GOOS == "windows" {
		assert.True(t, insideDir(`C:\Users\U\.CORAL`, `c:\users\u\.coral\Tray.log`), "file names do not count case on Windows")
	}
}

// OpenBrowser starts nothing for an address WebURL refuses (a file, a program path, a custom scheme, an option).
func TestOpenBrowserRefusesWhatIsNotAWebAddress(t *testing.T) {
	for _, bad := range []string{"file:///C:/Windows/System32/calc.exe", `C:\Windows\System32\calc.exe`, "ms-msdt:/id", "-a x", ""} {
		_, ok := WebURL(bad)
		assert.False(t, ok, bad)
		OpenBrowser(bad) // returns before building a command
	}
}
