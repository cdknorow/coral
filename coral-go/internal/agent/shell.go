package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ShellType identifies the type of shell being used for agent sessions.
type ShellType string

const (
	ShellBash       ShellType = "bash"
	ShellZsh        ShellType = "zsh"
	ShellPowerShell ShellType = "powershell"
	ShellCmd        ShellType = "cmd"
)

// DetectShell determines the shell type from CORAL_SHELL env var,
// falling back to platform defaults.
func detectShell() ShellType {
	if env := os.Getenv("CORAL_SHELL"); env != "" {
		return classifyShell(env)
	}

	if runtime.GOOS == "windows" {
		return ShellPowerShell
	}

	// Unix: check $SHELL
	if sh := os.Getenv("SHELL"); sh != "" {
		return classifyShell(sh)
	}
	return ShellBash
}

// classifyShell maps a shell path or name to a ShellType.
func classifyShell(shell string) ShellType {
	base := strings.ToLower(filepath.Base(shell))
	// Strip .exe suffix for Windows
	base = strings.TrimSuffix(base, ".exe")

	switch {
	case base == "pwsh" || base == "powershell":
		return ShellPowerShell
	case base == "cmd":
		return ShellCmd
	case base == "zsh":
		return ShellZsh
	default:
		// bash, sh, git-bash, wsl, etc.
		return ShellBash
	}
}

// AppBundleBinDir returns the directory containing hook binaries if the
// current executable is running from a macOS .app bundle or a similar
// packaged install. Returns empty string if not in a bundle.
func appBundleBinDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return ""
	}
	dir := filepath.Dir(exe)

	// macOS .app bundle: .../Coral.app/Contents/MacOS/coral
	if strings.Contains(dir, ".app"+string(os.PathSeparator)+"Contents"+string(os.PathSeparator)+"MacOS") ||
		strings.HasSuffix(dir, ".app/Contents/MacOS") {
		return dir
	}

	// Windows/Linux installed: check if hook binaries exist alongside the executable
	hookPath := filepath.Join(dir, "coral-hook-agentic-state")
	if runtime.GOOS == "windows" {
		hookPath += ".exe"
	}
	if _, err := os.Stat(hookPath); err == nil {
		return dir
	}

	return ""
}

// CoralToolsDir returns the directory containing coral-board and other Coral
// CLI tools. Tries appBundleBinDir() first, then checks known install locations.
func CoralToolsDir() string {
	if dir := appBundleBinDir(); dir != "" {
		return dir
	}

	// Fallback: check known locations for coral-board
	candidates := []string{
		"/Applications/Coral.app/Contents/MacOS",
	}
	// Also check next to the current executable
	if exe, err := os.Executable(); err == nil {
		if exe, err = filepath.EvalSymlinks(exe); err == nil {
			candidates = append([]string{filepath.Dir(exe)}, candidates...)
		}
	}

	for _, dir := range candidates {
		boardPath := filepath.Join(dir, "coral-board")
		if _, err := os.Stat(boardPath); err == nil {
			return dir
		}
	}
	return ""
}

// singleQuote wraps s in single quotes unconditionally, escaping any embedded
// single quote with the POSIX apostrophe-backslash-apostrophe-apostrophe sequence.
// Inside single quotes, POSIX shells perform no expansion at all, so this is
// the right way to pass arbitrary values such as
// URLs and paths through an `export` line. Prefer this over SanitizeShellValue
// for anything that may legitimately contain ':' or '/'.
func singleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shellQuoteLiteral wraps s in the platform shell's literal quoting style.
// On POSIX shells this is single-quoting with escaped apostrophes; on
// PowerShell it is single-quoting with doubled apostrophes.
func shellQuoteLiteral(s string) string {
	switch detectShell() {
	case ShellPowerShell:
		return "'" + strings.ReplaceAll(s, "'", "''") + "'"
	default:
		return singleQuote(s)
	}
}

// formatEnvExport returns a shell fragment that sets the given environment
// variable for subsequent commands on the same line.
// On bash/zsh: export K='V' &&
// On PowerShell: $env:K='V';
// On cmd.exe: set "K=V" &&
func formatEnvExport(key, value string) string {
	switch detectShell() {
	case ShellPowerShell:
		return fmt.Sprintf("$env:%s=%s;", key, shellQuoteLiteral(value))
	case ShellCmd:
		return fmt.Sprintf(`set "%s=%s" &&`, key, value)
	default:
		return fmt.Sprintf(`export %s=%s &&`, key, singleQuote(value))
	}
}

// formatCatSubstitution returns a command substitution that reads a file's
// contents inline. On bash/zsh: $(cat 'path'). On PowerShell, embedded
// double-quotes are escaped so the C runtime parses them correctly (see
// FormatPromptFileArg for the full explanation).
func formatCatSubstitution(path string) string {
	switch detectShell() {
	case ShellPowerShell:
		return fmt.Sprintf("$((Get-Content -Raw '%s') -replace '\"', '\\\"')", path)
	default:
		return fmt.Sprintf("$(cat '%s')", path)
	}
}

// SanitizeShellValue strips characters that could enable shell injection.
// Only allows alphanumeric characters, hyphens, underscores, dots, and spaces.
// This is used for values interpolated into shell command strings.
func SanitizeShellValue(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '-' || r == '_' || r == '.' || r == ' ' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// PrefixWithPathEnv returns a shell command prefix that prepends the given
// directory to PATH. Returns empty string if binDir is empty.
func prefixWithPathEnv(binDir string) string {
	if binDir == "" {
		return ""
	}
	shell := detectShell()
	switch shell {
	case ShellPowerShell:
		return fmt.Sprintf(`$env:PATH="%s;$env:PATH"; `, binDir)
	case ShellCmd:
		return fmt.Sprintf(`set "PATH=%s;%%PATH%%" && `, binDir)
	default:
		return fmt.Sprintf(`export PATH="%s:$PATH" && `, binDir)
	}
}

// WrapWithBundlePath prepends the app bundle bin directory to PATH in the
// given command string, if running from a packaged install. No-op otherwise.
func WrapWithBundlePath(cmd string) string {
	if prefix := prefixWithPathEnv(CoralToolsDir()); prefix != "" {
		return prefix + cmd
	}
	return cmd
}

// FindCLIInCommonPaths searches common install locations for a CLI binary.
// Returns the full path if found, empty string if not.
func FindCLIInCommonPaths(binary string) string {
	home, _ := os.UserHomeDir()
	if home == "" {
		return ""
	}

	var candidates []string

	switch runtime.GOOS {
	case "darwin":
		candidates = []string{
			"/usr/local/bin/" + binary,
			"/opt/homebrew/bin/" + binary,
			filepath.Join(home, ".local", "bin", binary),
		}
		// nvm installs
		nvmDir := filepath.Join(home, ".nvm", "versions", "node")
		if entries, err := filepath.Glob(filepath.Join(nvmDir, "*", "bin", binary)); err == nil {
			candidates = append(candidates, entries...)
		}
		// fnm installs
		fnmDir := filepath.Join(home, "Library", "Application Support", "fnm", "node-versions")
		if entries, err := filepath.Glob(filepath.Join(fnmDir, "*", "installation", "bin", binary)); err == nil {
			candidates = append(candidates, entries...)
		}
	case "linux":
		candidates = []string{
			"/usr/local/bin/" + binary,
			filepath.Join(home, ".local", "bin", binary),
			"/snap/bin/" + binary,
		}
		nvmDir := filepath.Join(home, ".nvm", "versions", "node")
		if entries, err := filepath.Glob(filepath.Join(nvmDir, "*", "bin", binary)); err == nil {
			candidates = append(candidates, entries...)
		}
	case "windows":
		exts := []string{"", ".exe", ".cmd", ".bat"}
		basePaths := []string{
			filepath.Join(os.Getenv("APPDATA"), "npm"),
			filepath.Join(os.Getenv("LOCALAPPDATA"), "npm"),
		}
		for _, base := range basePaths {
			for _, ext := range exts {
				candidates = append(candidates, filepath.Join(base, binary+ext))
			}
		}
	}

	// Also check app bundle directory
	if binDir := appBundleBinDir(); binDir != "" {
		candidates = append(candidates, filepath.Join(binDir, binary))
	}

	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

// FormatPromptFileArg returns shell-appropriate syntax for reading a prompt
// file and passing its content as a single CLI argument.
//
// On PowerShell 5.1, passing strings with embedded double-quotes to native
// executables is broken: PS constructs the process command line without
// escaping internal `"`, so the C runtime's argv parser splits at them.
// The workaround is to `-replace '"', '\"'` so the command line contains
// `\"` which the C runtime interprets as a literal `"`.
func FormatPromptFileArg(promptFile string) string {
	shell := detectShell()
	switch shell {
	case ShellPowerShell, ShellCmd:
		return fmt.Sprintf(`"$((Get-Content -Raw '%s') -replace '"', '\"')"`, promptFile)
	default:
		// bash, zsh, sh
		return fmt.Sprintf("\"$(cat '%s')\"", promptFile)
	}
}
