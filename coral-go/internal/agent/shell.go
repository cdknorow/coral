package agent

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/subgentic/coral/internal/psquote"
	"github.com/subgentic/coral/internal/winshell"
)

// cmdShellIgnored logs once that a CORAL_SHELL naming cmd.exe is ignored (#3692).
var cmdShellIgnored sync.Once

// ShellType identifies the type of shell being used for agent sessions.
type ShellType string

const (
	ShellBash       ShellType = "bash"
	ShellZsh        ShellType = "zsh"
	ShellPowerShell ShellType = "powershell"
)

// DetectShell determines the shell type from CORAL_SHELL env var,
// falling back to platform defaults.
func detectShell() ShellType {
	if env := os.Getenv("CORAL_SHELL"); env != "" {
		if !isCmdShell(env) {
			return classifyShell(env)
		}
		// cmd.exe is never an agent's shell (#3692): the launch line is PowerShell's on Windows
		// (the prompt argument, the long-command handoff), and the terminal starts PowerShell
		// there (ptymanager's pickShell ignores it the same way). Elsewhere the platform's own
		// shell is used.
		cmdShellIgnored.Do(func() {
			log.Printf("[agent] CORAL_SHELL=%q is ignored for agents: Coral starts Windows agents through PowerShell, since cmd.exe can't run their launch line", env)
		})
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
	base := shellBase(shell)
	switch {
	case base == "pwsh" || base == "powershell":
		return ShellPowerShell
	case base == "cmd":
		return ShellPowerShell // never cmd.exe (#3692): see detectShell
	case base == "zsh":
		return ShellZsh
	default:
		// bash, sh, git-bash, wsl, etc.
		return ShellBash
	}
}

// shellBase is a shell's name: its last path element (either slash), lower case, without .exe.
func shellBase(shell string) string {
	base := strings.ToLower(filepath.Base(strings.ReplaceAll(shell, `\`, "/")))
	return strings.TrimSuffix(base, ".exe")
}

// isCmdShell reports whether shell names cmd.exe.
func isCmdShell(shell string) bool { return shellBase(shell) == "cmd" }

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

// HookDirEnv names the folder agents get Coral's tools from (coral-board and the
// coral-hook-* binaries), overriding the search below (#3492).
const HookDirEnv = "CORAL_HOOK_DIR"

// installedToolsDir is the installed app's tools folder, the last resort.
var installedToolsDir = "/Applications/Coral.app/Contents/MacOS"

// installedFallback says whether CoralToolsDir may fall back to the installed app.
// Startup turns it off for a dev build or a server on a non-default data folder (a
// test server): those must run their own build's hooks, never an older installed copy
// against themselves without saying so (#3492).
var installedFallback atomic.Bool

func init() { installedFallback.Store(true) }

// SetInstalledToolsFallback allows or forbids falling back to the installed app.
func SetInstalledToolsFallback(allow bool) { installedFallback.Store(allow) }

// InstalledToolsFallback reports whether falling back to the installed app is allowed.
func InstalledToolsFallback() bool { return installedFallback.Load() }

// CoralToolsDir returns the directory containing coral-board and the coral-hook-*
// binaries that agents launched by this server get on their PATH: see CoralToolsSource.
// PathWithDir is path with dir first, joined with the platform's list separator (':' on Unix, ';' on Windows, where a ':' made
// "<dir>:C:\Windows\system32" one invalid entry, #126 F2).
func PathWithDir(dir, path string) string {
	if path == "" {
		return dir
	}
	return dir + string(os.PathListSeparator) + path
}

func CoralToolsDir() string {
	dir, _ := CoralToolsSource()
	return dir
}

// CoralToolsSource returns CoralToolsDir and where it was found, for the startup log:
//  1. CORAL_HOOK_DIR, when set;
//  2. the app bundle, or hooks next to this executable (appBundleBinDir);
//  3. coral-board next to this executable;
//  4. the installed app, unless a dev or test server forbade it (SetInstalledToolsFallback).
//
// "" means none: agents then run whatever coral-board and coral-hook-* is on their PATH.
func CoralToolsSource() (dir, source string) {
	if d := strings.TrimSpace(os.Getenv(HookDirEnv)); d != "" {
		return d, HookDirEnv
	}
	if d := appBundleBinDir(); d != "" {
		return d, "this build"
	}
	if exe, err := os.Executable(); err == nil {
		if exe, err = filepath.EvalSymlinks(exe); err == nil {
			if _, err := os.Stat(filepath.Join(filepath.Dir(exe), "coral-board")); err == nil {
				return filepath.Dir(exe), "this build (coral-board only)"
			}
		}
	}
	if installedFallback.Load() {
		if _, err := os.Stat(filepath.Join(installedToolsDir, "coral-board")); err == nil {
			return installedToolsDir, "the installed Coral app"
		}
	}
	return "", ""
}

// singleQuote wraps s in single quotes unconditionally, escaping any embedded
// single quote with the POSIX apostrophe-backslash-apostrophe-apostrophe sequence.
// Inside single quotes, POSIX shells perform no expansion at all, so this is
// the right way to pass arbitrary values such as
// URLs and paths through an `export` line.
func singleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shellQuoteLiteral wraps s in the platform shell's literal quoting style.
// On POSIX shells this is single-quoting with escaped apostrophes; on
// PowerShell it is single-quoting with every single-quote character doubled, the curly ones
// too (psquote, #3904).
func shellQuoteLiteral(s string) string {
	switch detectShell() {
	case ShellPowerShell:
		return psquote.Literal(s)
	default:
		return singleQuote(s)
	}
}

// formatEnvExport returns a shell fragment that sets the given environment
// variable for subsequent commands on the same line.
// On bash/zsh: export K='V' &&
// On PowerShell: $env:K='V';
func formatEnvExport(key, value string) string {
	switch detectShell() {
	case ShellPowerShell:
		return fmt.Sprintf("$env:%s=%s;", key, shellQuoteLiteral(value))
	default:
		return fmt.Sprintf(`export %s=%s &&`, key, singleQuote(value))
	}
}

// promptArg writes content into a launch temp file and returns the shell syntax that passes
// lead+content to the CLI as one argument (a prompt, or Codex's developer_instructions=<text>),
// the content read back from the file: lead"$(cat 'file')" on bash/zsh, and on PowerShell
// lead"$(Get-Content … 'file')" with the file holding the content as the shell must pass it,
// read back as it is. For Windows PowerShell 5.1, which escapes nothing for the program's argv
// parser, it is written escaped by psquote.NativeArg, the same rule as every other argument. An
// escape done in PowerShell (a -replace) missed the arguments 5.1 doesn't quote although they
// have spaces, so prompt text with an odd number of quotes before its first space split into
// options ("Review --sandbox ... became three arguments; #29, Security's #20 F1). lead is
// Coral's own text of plain characters (promptLeadOK): no quote, backslash or space, so the
// argv parser reads it the same inside or outside 5.1's quotes and NativeArg(content) after it
// is NativeArg(lead+content)'s reading.
func (l *launchFiles) promptArg(prefix, id, ext, lead, content string) string {
	if !promptLeadOK(lead) {
		panic(fmt.Sprintf("promptArg: lead %q is not plain text", lead))
	}
	if detectShell() == ShellPowerShell && legacyNativeArgs() {
		content = psquote.NativeArg(content)
	}
	return lead + FormatPromptFileArg(l.write(prefix, id, ext, []byte(content)))
}

// promptLeadOK reports whether lead is only letters, digits and _ . = - (see promptArg).
func promptLeadOK(lead string) bool {
	for _, c := range lead {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("_.=-", c)) {
			return false
		}
	}
	return true
}

// legacyNativeArgs reports whether the PowerShell that runs the launch line passes arguments
// to native programs the Windows PowerShell 5.1 way, so the line escapes them itself: that
// shell is winshell.PowerShell, which ptymanager starts the agent's terminal and wraps its
// launch command with. Never on other OSes.
func legacyNativeArgs() bool {
	return runtime.GOOS == "windows" && winshell.LegacyNativeArgs(winshell.PowerShell())
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
		return fmt.Sprintf(`$env:PATH=%s + $env:PATH; `, shellQuoteLiteral(binDir+";"))
	default:
		// binDir is quoted as a literal; only $PATH is expanded (#3362).
		return fmt.Sprintf(`export PATH=%s"$PATH" && `, singleQuote(binDir+":"))
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
// file and passing its content as a single CLI argument. On PowerShell the file is
// read as it is, as UTF-8 (5.1 reads a file without a byte-order mark in the ANSI
// code page: a prompt's "—" arrived as "â€”"), so it must hold the argument as the
// shell is to pass it: write it with promptArg.
func FormatPromptFileArg(promptFile string) string {
	shell := detectShell()
	switch shell {
	case ShellPowerShell:
		return fmt.Sprintf(`"$(Get-Content -Raw -Encoding UTF8 -LiteralPath %s)"`, shellQuoteLiteral(promptFile))
	default:
		// bash, zsh, sh. The path is quoted like any value (#3362): it is Coral's own temp
		// file, but its name carries a session id.
		return fmt.Sprintf("\"$(cat %s)\"", shellQuoteLiteral(promptFile))
	}
}

// toolsWriters is who besides the system and administrators can change the tools folder
// (secfile.Writers), as startup found it (SetToolsFolderWriters); empty when only an
// administrator can, as for an install in Program Files.
var toolsWriters struct {
	sync.Mutex
	dir string
	who []string
}

// SetToolsFolderWriters records what startup found about the tools folder (H5).
func SetToolsFolderWriters(dir string, who []string) {
	toolsWriters.Lock()
	defer toolsWriters.Unlock()
	toolsWriters.dir, toolsWriters.who = dir, append([]string(nil), who...)
}

// ToolsFolderWriters is the tools folder and who besides the system and administrators can
// change it, for the dashboard (H5).
func ToolsFolderWriters() (dir string, who []string) {
	toolsWriters.Lock()
	defer toolsWriters.Unlock()
	return toolsWriters.dir, append([]string(nil), toolsWriters.who...)
}
