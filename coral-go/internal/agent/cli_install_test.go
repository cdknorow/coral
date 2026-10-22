package agent

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	at "github.com/subgentic/coral/internal/agenttypes"
)

// #4569: every CLI Coral can launch says how to install it and links the vendor's own page (https, never a guide).
func TestEveryCLIHasAnInstallCommandAndOfficialPage(t *testing.T) {
	official := map[string]string{
		at.Claude: "https://code.claude.com/docs/en/overview",
		at.Codex:  "https://github.com/openai/codex",
		at.Pi:     "https://pi.dev",
		at.Agy:    "https://antigravity.google", at.Antigravity: "https://antigravity.google", at.Gemini: "https://antigravity.google",
	}
	for typ, info := range agentCLIs {
		assert.NotEmpty(t, info.InstallCommand, typ)
		assert.Equal(t, official[typ], info.InstallURL, typ)
		assert.True(t, strings.HasPrefix(info.InstallURL, "https://"), typ)
	}
	for _, typ := range []string{at.Agy, at.Antigravity, at.Gemini} {
		info := agentCLIs[typ]
		assert.Equal(t, "curl -fsSL https://antigravity.google/cli/install.sh | bash", info.InstallCommand, typ)
		assert.Contains(t, info.InstallNote, "~/.local/bin", typ)
		assert.Contains(t, info.InstallNote, `export PATH="$HOME/.local/bin:$PATH"`, typ)
	}
	for _, typ := range []string{at.Claude, at.Codex, at.Pi} {
		assert.Empty(t, agentCLIs[typ].InstallNote, typ)
	}
}

// #5885: each command was checked against the vendor's own install page (Claude Code's setup page, Codex's README, pi.dev, Antigravity's download page). pi moved to a new npm
// scope: the old package is deprecated on npm ("please use @earendil-works/pi-coding-agent"), so a person who ran it was told to install something that is no longer kept.
// The dashboard's own copy of the table (cli_check.js) must say the same as the server's.
func TestInstallCommandsAreThoseOfTheVendorsPagesAndBothTablesAgree(t *testing.T) {
	assert.Equal(t, "npm install -g --ignore-scripts @earendil-works/pi-coding-agent", agentCLIs[at.Pi].InstallCommand)
	assert.Equal(t, "npm install -g @anthropic-ai/claude-code", agentCLIs[at.Claude].InstallCommand)
	assert.Equal(t, "npm install -g @openai/codex", agentCLIs[at.Codex].InstallCommand)
	raw, err := os.ReadFile(filepath.Join("..", "..", "pkg", "frontend", "static", "cli_check.js"))
	require.NoError(t, err)
	js := string(raw)
	for typ, info := range agentCLIs {
		assert.Contains(t, js, "'"+info.InstallCommand+"'", typ+": the dashboard's table says what the server's does")
	}
	assert.NotContains(t, js, "mariozechner")
	for typ, info := range agentCLIs {
		assert.NotContains(t, info.InstallCommand, "mariozechner", typ)
	}
}

// #5885, Windows: the macOS line is "curl ... | bash" and its note says export PATH in ~/.zshrc, none of which works there (no bash; curl in PowerShell is Invoke-WebRequest). The vendor's page gives
// Windows its own PowerShell line; the server answers with it on a Windows server, and the dashboard's table carries the same value as its fallback.
func TestWindowsGetsTheVendorsPowerShellLineAndNoUnixAdvice(t *testing.T) {
	const ps = "irm https://antigravity.google/cli/install.ps1 | iex"
	for _, typ := range []string{at.Agy, at.Antigravity, at.Gemini} {
		win, mac := cliInfoFor("windows", typ), cliInfoFor("darwin", typ)
		require.NotNil(t, win, typ)
		assert.Equal(t, ps, win.InstallCommand, typ)
		assert.Contains(t, win.InstallNote, `%LOCALAPPDATA%\agy\bin`, typ)
		assert.Contains(t, win.InstallNote, "new terminal", typ)
		assert.Equal(t, "curl -fsSL https://antigravity.google/cli/install.sh | bash", mac.InstallCommand, typ+": macOS and Linux keep their line")
		assert.Equal(t, mac, cliInfoFor("linux", typ), typ)
	}
	for typ := range agentCLIs {
		win := cliInfoFor("windows", typ)
		for _, text := range []string{win.InstallCommand, win.InstallNote} {
			for _, unix := range []string{"bash", "export ", "~/.local", ".zshrc", ".bashrc"} {
				assert.NotContains(t, text, unix, typ+": a Windows value has no Unix advice")
			}
		}
	}
	for _, typ := range []string{at.Claude, at.Codex, at.Pi} {
		assert.Equal(t, cliInfoFor("darwin", typ), cliInfoFor("windows", typ), typ+": the npm lines are the same on Windows")
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "pkg", "frontend", "static", "cli_check.js"))
	require.NoError(t, err)
	js := string(raw)
	assert.Contains(t, js, "winCmd: '"+ps+"'", "the dashboard's table has the Windows command")
	assert.Contains(t, js, strings.ReplaceAll(cliInfoFor("windows", at.Agy).InstallNote, `\`, `\\`), "and the Windows note, word for word")
	assert.Nil(t, cliInfoFor("windows", "nope"))
}

// Where a CLI is looked for on Windows when it is not on PATH includes where Antigravity's installer puts agy.exe, so a Coral that was running before the install finds it.
func TestWindowsCommonDirsHaveAntigravitysFolder(t *testing.T) {
	dirs := windowsCommonDirs(`C:\Users\a\AppData\Roaming`, `C:\Users\a\AppData\Local`)
	assert.Contains(t, dirs, filepath.Join(`C:\Users\a\AppData\Local`, "agy", "bin"))
	assert.Contains(t, dirs, filepath.Join(`C:\Users\a\AppData\Local`, "npm"), "npm's folders stay")
	assert.Contains(t, dirs, filepath.Join(`C:\Users\a\AppData\Roaming`, "npm"))
	assert.Empty(t, windowsCommonDirs("", ""), "no base variable: no folder relative to the working directory")
}

func TestFindCLIInCommonPathsFindsAgyInItsWindowsFolder(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the Windows lookup")
	}
	local := t.TempDir()
	t.Setenv("LOCALAPPDATA", local)
	t.Setenv("APPDATA", t.TempDir())
	dir := filepath.Join(local, "agy", "bin")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	exe := filepath.Join(dir, "agy.exe")
	require.NoError(t, os.WriteFile(exe, []byte("x"), 0o755))
	assert.Equal(t, exe, FindCLIInCommonPaths("agy"))
}

// The old Antigravity address (/install.sh, which is "not found" now) must not come back anywhere Coral shows or documents it.
func TestOldAntigravityInstallAddressIsGone(t *testing.T) {
	old := "antigravity.google/" + "install.sh"
	root := filepath.Join("..", "..")
	n := 0
	require.NoError(t, filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if base := info.Name(); base == "node_modules" || base == ".git" || base == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(path) {
		case ".go", ".js", ".mjs", ".md", ".html", ".json", ".yml", ".yaml", ".sh", ".txt":
		default:
			return nil
		}
		b, rerr := os.ReadFile(path)
		require.NoError(t, rerr)
		n++
		assert.NotContains(t, string(b), old, path)
		return nil
	}))
	assert.Greater(t, n, 100, "the scan really walked the tree")
}

// The dashboard's static table says what the server's does (cli_check.js mirrors agentCLIs).
func TestDashboardInstallTableMirrorsTheServer(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "pkg", "frontend", "static", "cli_check.js"))
	require.NoError(t, err)
	js := string(b)
	for typ, info := range agentCLIs {
		assert.Contains(t, js, info.InstallCommand, typ)
		assert.Contains(t, js, "'"+info.InstallURL+"'", typ)
	}
	assert.Contains(t, js, agyInstallNote, "the agy PATH note is the same words")
	urls := regexp.MustCompile(`https://[^'"\s]+`).FindAllString(js, -1)
	for _, u := range urls {
		if strings.Contains(u, "install.sh") || strings.Contains(u, "install.ps1") || strings.HasPrefix(u, "https://registry") { // an installer line, not a page
			continue
		}
		found := false
		for _, info := range agentCLIs {
			found = found || info.InstallURL == u
		}
		assert.True(t, found, "cli_check.js links %s, which is no CLI's official page in agentCLIs", u)
	}
}

// agy installs into ~/.local/bin: Coral looks there itself when nothing is configured, so it is found with that folder off PATH;
// a configured path that cannot be run is still an error, never a fall back to it (#4354).
func TestAgyInLocalBinIsFoundWithoutPathAndConfiguredPathsStayClosed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("PATH", t.TempDir())
	if runtime.GOOS == "windows" {
		t.Skip("the installer is for macOS and Linux")
	}
	bin := filepath.Join(home, ".local", "bin")
	require.NoError(t, os.MkdirAll(bin, 0o755))
	agy := filepath.Join(bin, "agy")
	require.NoError(t, os.WriteFile(agy, []byte("#!/bin/sh\nexit 0\n"), 0o755))

	got, err := FindCLI(map[string]string{}, at.Agy)
	require.NoError(t, err)
	assert.Equal(t, agy, got)

	_, err = FindCLI(map[string]string{CLIPathSettingKey(at.Agy): filepath.Join(t.TempDir(), "missing-agy")}, at.Agy)
	assert.Error(t, err, "a configured path that cannot be run does not fall back to ~/.local/bin")
}

// The Launch dialog's "CLI not found" modal shows the vendor's page and the PATH note from the same table (cli_dialogs.js builds it
// from CLI_INSTALL_INSTRUCTIONS; a source check, since the modal opens only from a failed launch).
func TestNotFoundModalShowsTheOfficialPageAndNote(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "pkg", "frontend", "static", "cli_dialogs.js"))
	require.NoError(t, err)
	js := string(b)
	i := strings.Index(js, "function _showCLINotFoundModal")
	require.Greater(t, i, 0)
	body := js[i : i+3000]
	assert.Contains(t, body, "info.url")
	assert.Contains(t, body, "official install page")
	assert.Contains(t, body, "info.note")
	assert.Contains(t, body, `rel="noopener noreferrer"`)
	assert.Contains(t, body, "escapeHtml(info.note)")
}
