// Package agent provides agent implementations for Claude, Antigravity (agy), Codex, and Pi.
package agent

import (
	"fmt"
	"github.com/subgentic/coral/internal/logsafe"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	at "github.com/subgentic/coral/internal/agenttypes"
	setreg "github.com/subgentic/coral/internal/settings"
)

var coralSessionIDPattern = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}`)

// cliNames maps board_type to CLI command name. The nil/empty key is the default.
var cliNames = map[string]string{
	"":      "coral-board",
	"coral": "coral-board",
}

// GetCLIName returns the CLI command name for the given board type.
func GetCLIName(boardType string) string {
	if cli, ok := cliNames[boardType]; ok {
		return cli
	}
	return cliNames[""]
}

// LaunchParams holds all parameters for building a launch command.
type LaunchParams struct {
	SessionID       string
	SessionName     string // tmux session name (e.g. "claude-<uuid>"), used for CORAL_SESSION_NAME
	ProtocolPath    string
	ResumeSessionID string
	Flags           []string
	WorkingDir      string
	BoardName       string
	BoardLabel      string // the board's display name (#3536); the prompt shows it, BoardName (the id) stays the key
	Role            string
	Prompt          string
	PromptOverrides map[string]string // user overrides for orchestrator/worker prompts
	BoardType       string
	Capabilities    *Capabilities
	Tools           []string               // allowed tools (e.g. ["TodoWrite", "Bash(npm *)"])
	Hooks           map[string]interface{} // per-agent hooks to merge into settings (Claude-native) or fire via runner (Antigravity/Codex)
	CLIPath         string                 // custom path to agent binary (empty = default from PATH)
	PermissionMode  string                 // --permission-mode value (empty or "default" means omit the flag)
	CoralDir        string                 // path to coral data directory (~/.coral) for CA cert location
	CoralHost       string                 // host this Coral server is bound to
	CoralPort       int                    // port this Coral server is listening on
}

// CoralEnv returns the Coral environment every launched agent needs, as
// ordered name/value pairs. Empty values are omitted.
//
// This exists because the environment was being assembled independently in
// five places — once per agent implementation, plus the tmux session env — and
// they had already drifted: all of them set CORAL_SESSION_NAME and
// CORAL_SUBSCRIBER_ID, none set CORAL_PORT or CORAL_URL, while the workflow
// runner set all four. An agent without CORAL_PORT falls back to
// localhost:8420, so on any other port its coral-board talked to whatever
// server happened to own 8420 — which on a developer's machine is their real
// Coral, not the one that launched it. Every launch path must build this env
// from here so they cannot drift apart again.
func CoralEnv(params LaunchParams) [][2]string {
	var env [][2]string
	add := func(k, v string) {
		if v != "" {
			env = append(env, [2]string{k, v})
		}
	}
	add("CORAL_SESSION_NAME", params.SessionName)
	add("CORAL_SUBSCRIBER_ID", params.Role)
	if params.CoralPort > 0 {
		port := strconv.Itoa(params.CoralPort)
		host := params.CoralHost
		// 0.0.0.0 means "every interface"; an agent has to dial a real one.
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = "127.0.0.1"
		}
		add("CORAL_PORT", port)
		add("CORAL_HOST", host)
		add("CORAL_URL", fmt.Sprintf("http://%s:%s", host, port))
	}
	add("CORAL_DIR", params.CoralDir)
	add("CORAL_DATA_DIR", params.CoralDir)
	return env
}

// IndexedSession holds extracted session data from a history file.
type IndexedSession struct {
	// Excluded removes derived History/search rows for this source, never the source file.
	Excluded       bool
	SessionID      string
	SourceType     string
	SourceFile     string  // path to the history file this session came from
	FileMtime      float64 // Unix timestamp of file modification time
	FirstTimestamp *string
	LastTimestamp  *string
	MessageCount   int
	DisplaySummary string
	FTSBody        string
	// FTSUnchanged: FTSBody is the one this scanner returned for the file last time, so the
	// indexer needn't write it again (#3728).
	FTSUnchanged bool
}

// ftsBodyLimit caps how much of one session's text enters the search index.
// A long session can run to megabytes; indexing all of it across hundreds of
// sessions would cost far more disk than the search is worth. 256 KiB is
// roughly a hundred thousand words, which is more than enough for the "what
// was that session about" query this feature exists to answer.
const ftsBodyLimit = 256 * 1024

// FTSBodyBuilder accumulates the searchable text of one session, bounded.
//
// It exists so every agent's extractor builds the body the same way. FTSBody
// was declared and read but never assigned by any of the four extractors, so
// the guard in the indexer was never true, UpsertFTS was never called, and
// session_fts stayed empty — in production, across 56 indexed sessions, for
// months. Building it in one place is what stops three of the four being
// fixed and the fourth quietly not.
type FTSBodyBuilder struct {
	b    strings.Builder
	full bool
}

// Add appends one message's text. Empty strings are ignored, and everything
// after the limit is dropped rather than truncated mid-append, so the body
// never ends in half a word.
func (f *FTSBodyBuilder) Add(text string) {
	if f.full || text == "" {
		return
	}
	if f.b.Len() > 0 {
		f.b.WriteByte('\n')
	}
	f.b.WriteString(text)
	if f.b.Len() >= ftsBodyLimit {
		f.full = true
	}
}

// String returns the accumulated body.
func (f *FTSBodyBuilder) String() string { return f.b.String() }

// buildFTSBody returns the searchable body for a session, seeded with the
// display summary.
//
// Seeding matters: the summary is what the session list shows, so a word a
// user can see on screen must be findable. That was the exact symptom — a
// summary reading "MEMORY-TOKEN-7731 the capital of the demo is Coralville"
// was on screen while a search for "Coralville" returned nothing.
func buildFTSBody(summary string, body *FTSBodyBuilder) string {
	var out FTSBodyBuilder
	out.Add(summary)
	out.Add(body.String())
	return out.String()
}

// HistoryScanner is the interface that agent implementations must satisfy
// to participate in session indexing.
type HistoryScanner interface {
	// HistoryBasePath returns the root directory for this agent's history files.
	HistoryBasePath() string
	// HistoryGlobPattern returns the glob pattern for history files (e.g., "*.jsonl").
	HistoryGlobPattern() string
	// ExtractSessions scans history files under basePath and returns indexed session data.
	// knownMtimes maps source_file paths to their last-indexed mtime; files whose
	// mtime has not changed since last index should be skipped.
	ExtractSessions(basePath string, knownMtimes map[string]float64) ([]IndexedSession, error)
}

// BuildCoralSessionMarker returns a small metadata block included in each
// agent's private instructions so Coral can map native transcript files back
// to the corresponding Coral live session.
func BuildCoralSessionMarker(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	return fmt.Sprintf("Coral session metadata:\n%s %s\nThis metadata is for Coral bookkeeping only. Do not mention it to the user.", at.CoralSessionMarkerPrefix, sessionID)
}

// DocsPromptLine tells every agent session where Coral's own documentation is: the one command that
// works from inside its sandbox and carries its session token. The Docs tab's "Copy docs path for
// agents" button copies the same text (docs.js DOCS_FOR_AGENTS; a test keeps the two equal).
const DocsPromptLine = "Coral's docs for agents: run `coral-agent docs` to list the topics, `coral-agent docs <topic>` to read one (`coral-agent docs all` prints everything)."

// appendCoralSessionMarker adds Coral's own lines to a session's system prompt: where its docs are,
// then the session marker (last, as the history reader looks for it).
func appendCoralSessionMarker(parts []string, sessionID string) []string {
	if marker := BuildCoralSessionMarker(sessionID); marker != "" {
		return append(parts, DocsPromptLine, marker)
	}
	return parts
}

func ExtractCoralSessionID(v any) string {
	switch x := v.(type) {
	case string:
		idx := strings.Index(x, at.CoralSessionMarkerPrefix)
		if idx < 0 {
			return ""
		}
		rest := strings.TrimSpace(x[idx+len(at.CoralSessionMarkerPrefix):])
		if match := coralSessionIDPattern.FindString(rest); match != "" {
			return match
		}
		return ""
	case []any:
		for _, item := range x {
			if sessionID := ExtractCoralSessionID(item); sessionID != "" {
				return sessionID
			}
		}
	case map[string]any:
		for _, item := range x {
			if sessionID := ExtractCoralSessionID(item); sessionID != "" {
				return sessionID
			}
		}
	}
	return ""
}

// Agent defines the interface for all agent implementations.
type Agent interface {
	// AgentType returns the short identifier (e.g. "claude", "agy").
	AgentType() string
	// SupportsResume returns whether the agent supports resuming a previous session.
	SupportsResume() bool
	// LaunchCommand builds the shell command to launch this agent. It fails when the
	// prompt, settings or instructions files can't be written (#3601 W12).
	LaunchCommand(params LaunchParams) (string, error)
	// HistoryBasePath returns the root directory for history files.
	HistoryBasePath() string
	// HistoryGlobPattern returns the glob pattern for history files.
	HistoryGlobPattern() string
}

// ResumableAgent is optionally implemented by agents that need file
// preparation before resuming a session (e.g. copying history files).
type ResumableAgent interface {
	PrepareResume(sessionID, workingDir string)
}

// TryPrepareResume calls PrepareResume on the agent if it implements
// ResumableAgent. This avoids forcing agents without resume preparation
// to carry empty stubs.
func TryPrepareResume(ag Agent, sessionID, workingDir string) {
	if r, ok := ag.(ResumableAgent); ok {
		r.PrepareResume(sessionID, workingDir)
	}
}

// GetAgent returns the agent implementation for the given type.
func GetAgent(agentType string) Agent {
	switch agentType {
	case at.Agy, at.Antigravity:
		return &AgyAgent{}
	case at.Gemini:
		return &AgyAgent{customType: at.Gemini}
	case at.Codex:
		return &CodexAgent{}
	case at.Pi:
		return &PiAgent{}
	case at.Claude, "":
		return &ClaudeAgent{}
	case at.Terminal:
		// A plain shell has no agent of its own, but launch and restart still ask
		// for the implementation before skipping the command for it. The Claude
		// one stands in, as the fallback below did, without calling a supported
		// type "unknown" (#3259).
		return &ClaudeAgent{}
	default:
		// Reached only if a caller skipped ValidateAgentType. Falling back to
		// Claude is kept so a stored session with an unexpected type still
		// resolves to something, but it is no longer silent: this used to
		// start a real Claude process for a type the user never asked for.
		slog.Warn("unknown agent type, falling back to claude",
			"agent_type", agentType, "supported", LaunchableAgentTypes())
		return &ClaudeAgent{}
	}
}

// LaunchableAgentTypes lists every agent_type Coral accepts, in a stable order
// suitable for an error message.
//
// "terminal" is included deliberately. It is a plain shell rather than an
// agent, so it has no Agent implementation of its own (GetAgent hands back the
// Claude one, and the launch path skips command building for it entirely), but
// it is a valid agent_type at the API and rejecting it would break terminal
// sessions.
func LaunchableAgentTypes() []string {
	return []string{at.Claude, at.Codex, at.Agy, at.Gemini, at.Pi, at.Terminal}
}

// ValidateAgentType checks an agent_type supplied by a caller.
//
// An empty type is allowed and means "use the default", which is a different
// case from a type that was specified and is not recognised. Only the second
// is an error: it used to fall through to Claude, so a request naming an agent
// Coral does not support returned 200 ok:true and started a real Claude
// session, spending the user's tokens on an agent they never asked for.
func ValidateAgentType(agentType string) error {
	if agentType == "" || agentType == at.Antigravity {
		return nil // not specified; the caller's default applies
	}
	for _, known := range LaunchableAgentTypes() {
		if agentType == known {
			return nil
		}
	}
	return fmt.Errorf("unsupported agent_type %q — supported types are %s",
		agentType, strings.Join(LaunchableAgentTypes(), ", "))
}

// CLIInfo holds the CLI binary name and install instructions for an agent type.
type CLIInfo struct {
	Binary         string `json:"binary"`
	InstallCommand string `json:"install_command"`
	// InstallURL is the vendor's own install page (never a third-party guide), shown as a link where the CLI is missing.
	InstallURL string `json:"install_url"`
	// InstallNote is what to do after installing, when the installer leaves the CLI where PATH may not reach (agy: ~/.local/bin).
	InstallNote string `json:"install_note,omitempty"`
}

// agyInstallNote: the Antigravity installer puts agy in ~/.local/bin. Coral looks there itself (FindCLIInCommonPaths), so it finds
// agy even when that folder is not on PATH; a terminal of your own needs it on PATH.
const agyInstallNote = "The installer puts agy in ~/.local/bin. Coral looks there too, so it finds agy either way. To run agy in your own terminal, put that folder on your PATH: add export PATH=\"$HOME/.local/bin:$PATH\" to ~/.zshrc or ~/.bashrc, then open a new terminal."

// Antigravity's official installer (the script moved from /install.sh to /cli/install.sh) and its official page.
const (
	agyInstallCommand = "curl -fsSL https://antigravity.google/cli/install.sh | bash"
	agyInstallURL     = "https://antigravity.google"
)

// On Windows the vendor's page gives its own lines (PowerShell and CMD): the macOS and Linux line needs bash, and curl in PowerShell is Invoke-WebRequest. PowerShell is what Coral's Windows
// terminals use. The installer puts agy.exe in %LOCALAPPDATA%\agy\bin (FindCLIInCommonPaths looks there) and sets up the shell, which a terminal opened before it does not see.
const (
	agyWindowsInstallCommand = "irm https://antigravity.google/cli/install.ps1 | iex"
	agyWindowsInstallNote    = "Run this in PowerShell. The installer puts agy.exe in %LOCALAPPDATA%\\agy\\bin. Coral looks there too, so it finds agy either way. To run agy in a terminal of your own, open a new terminal after the install so it picks up the PATH change."
)

// agentCLIs maps agent types to their required CLI tools and install instructions (for macOS and Linux; GetCLIInfo gives the Windows line of agy on Windows).
var agentCLIs = map[string]CLIInfo{
	at.Claude:      {Binary: "claude", InstallCommand: "npm install -g @anthropic-ai/claude-code", InstallURL: "https://code.claude.com/docs/en/overview"},
	at.Agy:         {Binary: "agy", InstallCommand: agyInstallCommand, InstallURL: agyInstallURL, InstallNote: agyInstallNote},
	at.Antigravity: {Binary: "agy", InstallCommand: agyInstallCommand, InstallURL: agyInstallURL, InstallNote: agyInstallNote},
	at.Gemini:      {Binary: "agy", InstallCommand: agyInstallCommand, InstallURL: agyInstallURL, InstallNote: agyInstallNote},
	at.Codex:       {Binary: "codex", InstallCommand: "npm install -g @openai/codex", InstallURL: "https://github.com/openai/codex"},
	at.Pi:          {Binary: "pi", InstallCommand: "npm install -g --ignore-scripts @earendil-works/pi-coding-agent", InstallURL: "https://pi.dev"},
}

// GetCLIInfo returns CLI info for an agent type, or nil if unknown.
func GetCLIInfo(agentType string) *CLIInfo { return cliInfoFor(runtime.GOOS, agentType) }

// cliInfoFor is GetCLIInfo for the OS goos: agy (and the types that run it) has its own command and note on Windows, with no bash, export or ~/.local in them.
func cliInfoFor(goos, agentType string) *CLIInfo {
	info, ok := agentCLIs[agentType]
	if !ok {
		return nil
	}
	if goos == "windows" && info.Binary == "agy" {
		info.InstallCommand, info.InstallNote = agyWindowsInstallCommand, agyWindowsInstallNote
	}
	return &info
}

// CLIPathSettingKey returns the settings key for the agent's custom CLI path.
func CLIPathSettingKey(agentType string) string {
	return setreg.CLIPathPrefix + agentType
}

// ResolveCLIPath returns the configured CLI path for an agent type from settings,
// with cross-compatibility fallback between "agy" and "gemini".
func ResolveCLIPath(settings map[string]string, agentType string) string {
	if settings == nil {
		return ""
	}
	if p := setreg.TextFamily(settings, CLIPathSettingKey(agentType)); p != "" {
		return p
	}
	if agentType == at.Agy || agentType == at.Antigravity {
		return setreg.TextFamily(settings, CLIPathSettingKey(at.Gemini))
	}
	if agentType == at.Gemini {
		return setreg.TextFamily(settings, CLIPathSettingKey(at.Agy))
	}
	return ""
}

// CLIPathError is a configured cli_path_<type> that can't be run (#115). Error names the setting and the path; HubText names the
// setting only, for an answer that goes through Coral Hub (as the CLI check's does).
type CLIPathError struct {
	AgentType string
	Path      string
	Err       error
}

func (e *CLIPathError) Error() string {
	return fmt.Sprintf("%s CLI path \"%s\" (the setting %s, in Settings > CLI paths) can't be run (%v): fix it or clear it to use the one on PATH; Coral does not run another program in its place", e.AgentType, e.Path, CLIPathSettingKey(e.AgentType), e.Err)
}

func (e *CLIPathError) Unwrap() error { return e.Err }

func (e *CLIPathError) HubText() string {
	return fmt.Sprintf("the %s CLI path set in Settings > CLI paths (%s) can't be run; Coral does not run another program in its place: fix it or clear it to use the one on PATH", e.AgentType, CLIPathSettingKey(e.AgentType))
}

// CLINotFoundError is an agent CLI found nowhere: not set in Settings > CLI paths, not on PATH, not in the common install places (#5838). A launch answers it with a 422 and the fixed
// code cli_not_found, so a client says what to do (install it, or point Settings at it) instead of showing a server fault. Error keeps the sentence "<binary> CLI not found" (the
// launch failure's analytics category and the dashboard read it) and adds the setting.
type CLINotFoundError struct {
	AgentType      string
	Binary         string
	InstallCommand string
	InstallURL     string
}

func (e *CLINotFoundError) Error() string {
	msg := fmt.Sprintf("%s CLI not found. Install it: %s", e.Binary, e.InstallCommand)
	if e.InstallURL != "" {
		msg += fmt.Sprintf(" (the vendor's page: %s)", e.InstallURL)
	}
	return msg + fmt.Sprintf(". Already installed? Set its path in Settings > CLI paths (the setting %s).", CLIPathSettingKey(e.AgentType))
}

// FindCLI returns the executable to run for agentType, the way launch finds
// it: the configured path (cli_path_<type> in settings), then the binary on
// PATH, then the common install locations. The macOS app started from Finder
// has no Homebrew PATH, so a bare exec of "claude" fails there even though
// agents launch fine; every caller that runs an agent CLI uses this (#3169).
// A configured path that is not an executable is an error, as at launch (#4350):
// the PATH and the common locations are searched only when no path is set, so a
// broken wrapper never has the unwrapped CLI run background jobs instead
// (Security's #4431 SHOULD 4).
func FindCLI(settings map[string]string, agentType string) (string, error) {
	p, err := FindCLIForLaunch(settings, agentType)
	if err != nil {
		return "", err
	}
	// A CLI that needs node, with none Coral can use for it, fails every run it makes itself (version, sign-in check, models) with "env: node: No such file or directory": say so.
	if _, nerr := NodeDirFor(p, os.Environ()); nerr != nil {
		return "", nerr
	}
	return p, nil
}

// FindCLIForLaunch is FindCLI without the node check: an agent's launch line carries its own (NodePathPrefix), and the agent's shell may have a node Coral's process does not.
func FindCLIForLaunch(settings map[string]string, agentType string) (string, error) {
	if p := ResolveCLIPath(settings, agentType); p != "" {
		resolved, err := exec.LookPath(p)
		if err == nil {
			err = CheckRunnable(resolved)
		}
		if err == nil {
			err = CheckNotRealGemini(agentType, resolved)
		}
		if err != nil {
			return "", &CLIPathError{AgentType: agentType, Path: p, Err: err}
		}
		return resolved, nil
	}
	bin := agentType
	if info := GetCLIInfo(agentType); info != nil && info.Binary != "" {
		bin = info.Binary
	}
	if p, err := exec.LookPath(bin); err == nil {
		return p, nil
	}
	if p := FindCLIInCommonPaths(bin); p != "" {
		return p, nil
	}
	return "", fmt.Errorf("%s CLI %q not found: install it or set its path in Settings > CLI paths", agentType, bin)
}

// LaunchCLI is the program a restart, resume, wake or background launch runs for agentType (#115). A configured cli_path_<type> must be
// usable: FindCLI's answer, so one that can't be run is an error naming the setting and the path, never a silent fall back to whatever agent
// CLI is first on PATH. With none configured it is the absolute path exec.LookPath finds (which refuses the current folder), rather than
// a bare name that is looked up again, in the agent's shell, when it runs; when nothing is found it is "" and the launch line keeps the bare
// name, as before (the launch route itself reports a missing CLI). An agent type with no CLI of its own (a plain terminal) gets whatever
// is configured, unchecked.
func LaunchCLI(settings map[string]string, agentType string) (string, error) {
	info := GetCLIInfo(agentType)
	if info == nil || info.Binary == "" {
		return ResolveCLIPath(settings, agentType), nil
	}
	if ResolveCLIPath(settings, agentType) != "" {
		return FindCLIForLaunch(settings, agentType)
	}
	if p, err := exec.LookPath(info.Binary); err == nil && filepath.IsAbs(p) {
		return p, nil
	}
	return "", nil
}

// resolveBinary returns cliPath if non-empty, otherwise the default binary name.
func resolveBinary(cliPath, defaultBin string) string {
	if cliPath != "" {
		return cliPath
	}
	return defaultBin
}

// readProtocolFile reads the protocol file and returns its content, or "" on error.
func readProtocolFile(path string) string {
	if path == "" {
		return ""
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(content)
}

// shellQuote quotes one word for the platform shell, and ShellJoin builds a launch
// line from parts: shell_quote_unix.go and shell_quote_windows.go.

// joinQuoted quotes every part except those named in raw. callOp prefixes the first
// quoted command word with PowerShell's call operator.
func joinQuoted(parts, raw []string, quote func(string) string, callOp bool) string {
	isRaw := make(map[string]bool, len(raw))
	for _, r := range raw {
		isRaw[r] = true
	}
	out := make([]string, len(parts))
	seenCommand := false
	for i, p := range parts {
		if isRaw[p] {
			out[i] = p
			continue
		}
		q := quote(p)
		if callOp && !seenCommand && q != p {
			q = "& " + q
		}
		seenCommand = true
		out[i] = q
	}
	return strings.Join(out, " ")
}

// writeTempFile creates a temp file with an unpredictable name using os.CreateTemp
// (O_CREATE|O_EXCL) to prevent symlink attacks. The file name includes the session
// ID for later cleanup via CleanupTempFiles. Returns the file path. When the file
// can't be created or fully written it returns an error naming the temp folder: there
// is no fallback to a predictable name in the shared temp folder, which another user
// could plant first, and a half-written file is removed (#3601 W12).
func writeTempFile(prefix, id, ext string, content []byte) (string, error) {
	// The path is typed into the launch line inside $(cat …): the id (a session id,
	// possibly from a request) keeps only safe characters (#3362).
	id = safeFileID(id)
	pattern := fmt.Sprintf("coral_%s_%s_*.%s", prefix, id, ext)
	dir := LaunchTempDir()
	f, err := createTemp(dir, pattern)
	if err != nil {
		return "", fmt.Errorf("can't create the agent's %s file in the temp folder %s: %w", prefix, dir, err)
	}
	_, err = f.Write(content)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("can't write the agent's %s file in the temp folder %s: %w", prefix, dir, err)
	}
	rememberLaunchTemp(id, f.Name())
	return f.Name(), nil
}

// launchFiles collects the temp files one launch command writes. After the first failure
// it writes nothing more, and done removes the files already made and returns the error,
// so a launch never starts without its prompt, settings or instructions.
type launchFiles struct {
	made []string
	dirs []string
	err  error
}

func (l *launchFiles) write(prefix, id, ext string, content []byte) string {
	if l.err != nil {
		return ""
	}
	p, err := writeTempFile(prefix, id, ext, content)
	if err != nil {
		l.err = err
		return ""
	}
	l.made = append(l.made, p)
	return p
}

// dir records a temp folder made for the launch (removed with the files on a failure), or
// the error making it.
func (l *launchFiles) dir(d string, err error) string {
	if l.err != nil {
		if d != "" {
			os.RemoveAll(d)
		}
		return ""
	}
	if err != nil {
		l.err = err
		return ""
	}
	l.dirs = append(l.dirs, d)
	return d
}

func (l *launchFiles) done(cmd string) (string, error) {
	if l.err != nil {
		for _, p := range l.made {
			os.Remove(p)
		}
		for _, d := range l.dirs {
			os.RemoveAll(d)
		}
		return "", l.err
	}
	return cmd, nil
}

// createTemp is os.CreateTemp; tests replace it to make the create or the write fail.
var createTemp = os.CreateTemp

// safeFileID keeps letters, digits, '-' and '_' (a UUID is unchanged); anything else
// becomes '_'.
func safeFileID(id string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, id)
}

// CleanupTempFiles removes the Coral temp files of one session: its prompt and settings files
// (coral_<kind>_<id>[_<random>].<ext>) and agy's hooks folder (coral_agyhooks_<id>_<random>),
// called during teardown so nothing sensitive stays in the temp folder. sessionID must be a
// canonical UUID and each name must carry exactly it (followed by '_', '.' or nothing): a
// glob on an id like "a" matched every other session's files too (weak site W10, #3598).
//
// It removes the paths this process wrote for the session (wherever they are), and the session's
// files in Coral's private launch temp folder (<data dir>/tmp, written before a restart). It
// never reads the system temp folder (#90): files a launch before #4381 left there, or one while
// the private folder couldn't be made before a restart, are the startup sweep's.
func CleanupTempFiles(sessionID string) {
	if !isCanonicalUUID(sessionID) {
		return
	}
	if SessionTokenFileRemover != nil {
		SessionTokenFileRemover(sessionID) // a Codex agent's kept token file (#13 review N2)
	}
	var paths []string
	for _, p := range takeLaunchTemps(sessionID) {
		if inLaunchTempFolder(p) {
			paths = append(paths, p)
		}
	}
	if dir := privateLaunchTempDir(); dir != "" {
		if m, err := filepath.Glob(filepath.Join(dir, "coral_*_"+sessionID+"*")); err == nil {
			paths = append(paths, m...)
		}
	}
	for _, path := range paths {
		base := filepath.Base(path)
		i := strings.Index(base, "_"+sessionID)
		if !strings.HasPrefix(base, "coral_") || i < len("coral_x") {
			continue
		}
		if rest := base[i+1+len(sessionID):]; rest != "" && rest[0] != '_' && rest[0] != '.' {
			continue // another id that starts with this one
		}
		if strings.HasPrefix(base, "coral_agyhooks_") {
			os.RemoveAll(path) // agy's hooks folder (#3481)
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			slog.Debug("failed to remove temp file", "path", logsafe.Dir(path), "error", err)
		}
	}
}

// DefaultBoardPostingGuidance keeps the four default board prompts consistent.
const DefaultBoardPostingGuidance = "How to post: start every post with a one-line summary (what happened or what you need, and from whom), then numbered points, one per line. Answer numbered posts by number. Name people with @Name; put SHAs, branches and paths in backticks. Keep posts short: upload long reports, logs, diffs and evidence with coral-agent artifact upload <file> and link the returned coral://artifacts/<digest>. For short posts containing backticks, use coral-board post - <<'EOF' ... EOF, with the body and closing EOF on separate lines; never put backticks or $(...) inside a double-quoted post argument."

// Default board system-prompt fragments (used by all agents).
const DefaultOrchestratorSystemPrompt = "Post a message with coral-board post \"<your introduction>\" that introduces yourself, " +
	"then discuss your proposed plan with the operator (the human user) before posting assignments to the team.\n\n" +
	DefaultBoardPostingGuidance + "\n\n" +
	"When posting messages to specific agents, you MUST @mention them by name (e.g. @Lead Developer) so they receive a notification. " +
	"You can also use the --to flag: coral-board post --to \"Agent1,Agent2\" \"message\" which auto-prepends @mentions. " +
	"Messages without @mentions will NOT notify agents.\n\n" +
	"You can peek at any agent's terminal to check their progress without waiting for them to post:\n" +
	"  coral-board peek \"Agent Name\"           — see the last 30 lines of their terminal\n" +
	"  coral-board peek \"Agent Name\" --lines 50 — see more lines\n" +
	"Use this to monitor progress, diagnose stuck agents, or verify work before asking for a status update.\n\n" +
	"Use coral-board post for general conversation, discussion, status updates, and questions. " +
	"Use coral-board task for specific, defined work assignments. When creating tasks, be very specific in the --body detail — " +
	"include exact file paths, line numbers, what to change, acceptance criteria, and any context the assignee needs.\n" +
	"  coral-board task add \"title\" --body \"detailed description\" --assignee \"Agent Name\" — create and assign a task\n" +
	"  coral-board task list — see all tasks and their status\n" +
	"  coral-board task complete <id> --message \"summary of what was done\" — agents should do this when done\n" +
	TaskCapabilityGuidance + "\n" +
	"Treat the latest authoritative task detail and linked artifacts as the source of scope. Before correcting, reassigning, or reminding, reread the task and its upstream results. Keep ordinary review corrections with the current owner, branch/worktree, prerequisites, and task. The orchestrator owns shared handoffs and integration by default; allow direct worker coordination only as an explicit operator/orchestrator exception. Create a linked task only when scope, owner, prerequisite, or immutable acceptance input materially changes; record why and rewire affected dependents explicitly. For each material handoff, report the task ID, owner, integration owner when different, status (implemented, verified, merged, or deployed as applicable), tested revision, and durable artifact identity. A task notice, registered test, or mutable checkout is not acceptance evidence. Ask the operator when intent or acceptance is ambiguous.\n" +
	DefaultTaskWorkflowGuidance

const DefaultWorkerSystemPrompt = "Post a message with coral-board post \"<your introduction>\" that introduces yourself, " +
	"then STOP and wait. Do NOT poll the message board in a loop. Coral will notify you when there are new messages.\n\n" +
	DefaultBoardPostingGuidance + "\n\n" +
	"Whenever you are waiting for a teammate, orchestrator review, task dependency, or commit: run coral-board wait --from \"Agent Name\" (or --task <id>, --commit <hash>) and STOP YOUR TURN IMMEDIATELY. Never run sleep, polling loops, or wait in code. Coral will automatically notify and wake you up when the event occurs.\n\n" +
	"You can check for assigned tasks with coral-board task list or coral-board task claim. " +
	"When you finish a task, mark it complete: coral-board task complete <id> --message \"what was done\"\n" +
	DefaultTaskWorkflowGuidance

const DefaultTaskWorkflowGuidance = "Only the Operator or orchestrator creates or reassigns shared team tasks. Workers keep ownership through corrections and report shared handoffs to the orchestrator by default; direct worker coordination requires an explicit operator/orchestrator exception. Personal task planning is unchanged. " +
	"Use task detail/current to read requirements, dependency results, and upstream context. " +
	"Keep ordinary corrections with the current owner, task, branch/worktree, and prerequisites. " +
	"If a correction changes scope, owner, prerequisite, or acceptance input, tell the orchestrator the exact decision needed; do not create a duplicate shared task. " +
	"After a material handoff, report useful evidence (task ID, status, command/result, revision, and artifact when applicable), acknowledge an actionable handoff through the resulting action or evidence, then continue authorized work or wait for a named dependency; do not send empty acknowledgements or poll. " +
	"Carry the assigned outcome across turns and corrections: a smaller slice is progress, not proof that the whole task is complete. Follow the latest explicit Operator/orchestrator amendment, reread current task detail and artifacts before relying on summaries, and take the next safe authorized action when available. " +
	"Report success only after checking the assigned requirements against current evidence; otherwise report the limitation or failure honestly. A wait needs a specific current condition and next action; an observation timeout does not prove that work stopped and must not trigger a duplicate restart. " +
	"Complete with --message for results, --outcome failed for unsuccessful work, and --artifacts manifest.json when outputs are required. " +
	"Artifacts may use inline content or durable links; upload shared files with coral-agent artifact upload <file>. Download coral://artifacts/<digest> links with coral-agent artifact download <uri>; it prints a verified local path with a media extension. A coral://artifacts/<digest> link is not a file path or a web address: the file is stored by Coral on this computer, and the only way to get it is coral-agent artifact download coral://artifacts/<digest> (run it, then open the path it prints; never guess a path, and never try to open the link itself). An image the user pastes or attaches in the dashboard reaches you as that link alone, followed by the exact download command to run: run it first. For image artifacts, open that path with your image-viewing tool before reviewing the image; a URI or uploader description alone is not visual evidence. " +
	"\"Publish\", \"share\", or \"put it up as an artifact\" means a Coral-visible artifact unless the user explicitly asks for an external service: write the file in the repository, run coral-agent artifact upload <file>, and attach the returned coral://artifacts/<digest> URI to the task result (--artifacts manifest.json) so it appears in Coral's Artifacts and Team Artifacts views; for interactive content publish a Coral UI panel with coral-agent ui publish instead. Do not use an external artifact or cloud-hosting tool (for example claude.ai artifacts) for these requests unless the user names it. Linking an external reference is fine, but an external URL alone is not a Coral-managed artifact: Coral does not host or manage its content. If there is no task, upload the file and give the user the coral://artifacts/<digest> URI (browser URL /api/artifacts/<digest>); it is reachable by that link but is listed in the Artifacts and Team Artifacts views only after it is attached to a task result."

// TaskCapabilityGuidance is intentionally compact: it gives orchestrators a
// capability map without turning every task claim into a mandatory checklist.
// Keep this in the canonical prompt so all providers receive the same queue
// contract.
const TaskCapabilityGuidance = "Task capability map: use task detail/current before acting; claim [id] for a named ready task (bare claim uses ready assignment/FIFO); add dependencies with --blocked-by, outputs with --outputs, and optional completion gates with --completion-gates. Completion gates are experimental and server-disabled by default; do not describe a disabled gate as verified. Gate evidence must include candidate-revision when required. Use reassign/cancel for planning changes, and amend <id> --revision N --reason ... only for authoritative body, instruction, or gate changes. Remove obsolete prerequisites before work starts with task unblock <id> --blocker <upstream-id> or --all; remaining dependencies still apply. Use a linked task when dependency scope changes after work has started. submit-review records a candidate but keeps the task active; release-review frees execution capacity without accepting the result. Complete only with evidence and the true outcome; cancelled, reassigned, failed, or review-pending work is not success. Board guidance cannot override a provider or sandbox approval denial."

// Default action prompts (appended to user prompt as CLI positional arg).
const DefaultOrchestratorActionPrompt = `IMPORTANT: You were automatically joined to message board "{board_name}". Do NOT run coral-board join. Post a message with coral-board post "<your introduction>" that introduces yourself, then discuss your proposed plan with the operator (the human user) before posting assignments.

CRITICAL: Do NOT poll or loop on 'coral-board read'. After posting your introduction, continue authorized planning or work; after a progress post, continue or wait for a named dependency. Stop only when you are genuinely waiting. Coral will send you a notification (as a user message) when new messages arrive. Only run 'coral-board read' after receiving such a notification.

When posting messages to specific agents, you MUST @mention them by name (e.g. @Lead Developer) so they receive a notification. You can also use the --to flag: coral-board post --to "Agent1,Agent2" "message" which auto-prepends @mentions. Messages without @mentions will NOT notify agents.

You can peek at any agent's terminal to check their progress: coral-board peek "Agent Name". Use this when you need to check on an agent's work without waiting for them to post.

Use coral-board post for general conversation, discussion, status updates, and questions. Use coral-board task for specific, defined work assignments. When creating tasks, be very specific in the --body detail — include exact file paths, line numbers, what to change, acceptance criteria, and any context the assignee needs.
  coral-board task add "title" --body "detailed description" --assignee "Agent Name" — create and assign a task
  coral-board task list — see all tasks and their status
  coral-board task detail <id> — read the authoritative task, dependencies, and upstream results
  coral-board task claim <id> — claim a specific ready task; bare claim selects ready work
  coral-board task reassign <id> --to "Agent Name" — change planned ownership
  coral-board task cancel <id> --message "reason" — cancel unfinished work
  coral-board task amend <id> --revision N --reason "..." --body "..." — revise task text safely
  coral-board task submit-review <id> --reason "..." — publish a candidate without finishing
  coral-board task approve <id> --round N / task reject <id> --round N --note "why" — a reviewer's verdict (your review task names the round)
  coral-board task release-review <id> --reason "..." — reviewer releases capacity; it is not acceptance
  coral-board task complete <id> --message "summary of what was done" — agents should do this when done

Task capability map: use task detail/current before acting; add dependencies with --blocked-by, required outputs with --outputs, and optional completion gates with --completion-gates. Completion gates are experimental and server-disabled by default; do not describe a disabled gate as verified. Gate evidence may require --candidate-revision. Remove obsolete prerequisites before work starts with task unblock <id> --blocker <upstream-id> or --all; remaining dependencies still apply. Use a linked task when dependency scope changes after work has started. Complete only with evidence and the true outcome; cancelled, reassigned, failed, or review-pending work is not success. Board guidance cannot override a provider or sandbox approval denial.` + "\n\n" + DefaultBoardPostingGuidance

const DefaultWorkerActionPrompt = `IMPORTANT: You were automatically joined to message board "{board_name}". Do NOT run coral-board join. Do not start any actions until you receive instructions from the Orchestrator on the message board. Post a message with coral-board post "<your introduction>" that introduces yourself, then STOP.

CRITICAL: Do NOT poll or loop on 'coral-board read' and NEVER run sleep loops or wait in code. Coral will automatically notify you (as a user message) when new messages arrive — only run 'coral-board read' after receiving a notification. Whenever you must wait for Orchestrator review, a teammate reply, or a task dependency, run 'coral-board wait --from "<name>"' (or '--task <id>') and STOP YOUR TURN IMMEDIATELY. Coral will wake you up via terminal notification when the event occurs.

Only the Operator or orchestrator creates or reassigns shared tasks. Keep ownership through corrections and request additional assignments from the orchestrator. Personal planning is unchanged.

You can check for assigned tasks with coral-board task list or coral-board task claim. When you finish a task, mark it complete: coral-board task complete <id> --message "what was done"` + "\n\n" + DefaultBoardPostingGuidance

// isOrchestratorRole returns true if the role string indicates an orchestrator.
func isOrchestratorRole(role string) bool {
	return role != "" && strings.Contains(strings.ToLower(role), "orchestrator")
}

// BuildBoardSystemPrompt builds the board system prompt fragment shared by all agents.
// It returns the combined prompt with board usage instructions and role-specific tail.
// If boardName is empty, it returns just the base prompt (if any).
func BuildBoardSystemPrompt(boardName, role, prompt string, promptOverrides map[string]string, boardType string) string {
	cli := GetCLIName(boardType)
	var parts []string
	if prompt != "" {
		parts = append(parts, prompt)
	}
	if boardName != "" {
		roleLabel := ""
		if role != "" {
			roleLabel = fmt.Sprintf(" Your role is: %s.", role)
		}
		boardIntro := fmt.Sprintf(
			"You were automatically joined to message board \"%s\".%s "+
				"Do NOT run %s join — you are already subscribed.\n\n"+
				"Use the %s CLI to communicate with your teammates:\n"+
				"  %s read          — read new messages from teammates\n"+
				"  %s post \"msg\"    — post a message to the board\n"+
				"  %s read --last 5 — see the 5 most recent messages\n"+
				"  %s subscribers   — see who is on the board\n"+
				"When you have a new message, Coral will notify you.\n\n",
			boardName, roleLabel, cli, cli, cli, cli, cli, cli)

		var tail string
		if isOrchestratorRole(role) {
			if v, ok := promptOverrides["default_prompt_orchestrator"]; ok && v != "" {
				tail = v
			} else {
				tail = DefaultOrchestratorSystemPrompt
			}
		} else {
			if v, ok := promptOverrides["default_prompt_worker"]; ok && v != "" {
				tail = v
			} else {
				tail = DefaultWorkerSystemPrompt
			}
		}
		boardIntro += tail
		parts = append(parts, boardIntro)
	}
	return strings.Join(parts, "\n\n")
}

// BuildBoardActionPrompt builds the action prompt (CLI positional arg) for board sessions.
// It appends the appropriate orchestrator/worker action text to the base prompt.
// Returns the combined prompt, or empty string if no board is configured.
func BuildBoardActionPrompt(boardName, role, basePrompt string, promptOverrides map[string]string, boardType string) string {
	if boardName == "" {
		return basePrompt
	}
	cli := GetCLIName(boardType)
	overrides := promptOverrides
	if overrides == nil {
		overrides = map[string]string{}
	}
	var template string
	if isOrchestratorRole(role) {
		if v, ok := overrides["default_prompt_orchestrator"]; ok && v != "" {
			template = v
		} else {
			template = DefaultOrchestratorActionPrompt
		}
	} else {
		if v, ok := overrides["default_prompt_worker"]; ok && v != "" {
			template = v
		} else {
			template = DefaultWorkerActionPrompt
		}
	}
	actionText := strings.ReplaceAll(template, "{board_name}", boardName)
	actionText = strings.ReplaceAll(actionText, "coral-board", cli)
	if basePrompt != "" {
		return basePrompt + "\n\n" + actionText
	}
	return actionText
}

// boardPromptName is the name an agent's prompt shows for its board: the label when known,
// else the board key (#3536).
func (p LaunchParams) boardPromptName() string {
	if p.BoardLabel != "" {
		return p.BoardLabel
	}
	return p.BoardName
}
