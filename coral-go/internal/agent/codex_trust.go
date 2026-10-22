package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"

	"github.com/subgentic/coral/internal/secfile"
)

// Trusting Coral's own Codex hooks (#153). Codex 0.161 runs a hook only once the person has trusted it: until then a
// Coral-launched Codex opens on "Hooks need review" and Coral gets no hook events from it (#147). Codex reads that trust,
// a hooks.state.<key>.trusted_hash entry, from the user's config.toml and from the session's -c flags only (never from a
// project, a plugin or managed config: a repository can't trust its own hooks), so Coral gives it per launch with -c, for
// exactly the hooks it passes, and never writes CODEX_HOME. Every other hook Codex loads (the person's own, a repository's,
// a plugin's) still asks. Codex's --dangerously-bypass-hook-trust is never used: it skips the review for all of them.
//
// Coral trusts its hooks only where nobody but an administrator can change them (codexHooksFolderSafe): otherwise anything
// running as this user, another agent included, could replace a hook binary and Codex would run it outside its sandbox
// with no dialog. That is an installed Coral (Program Files); a portable or dev build keeps the dialog. Windows only: on
// macOS and Linux an app or an unpacked tarball is owned by the person, so no check tells a protected install from a
// folder this user can change, and the dialog stays there.

// codexHookEventLabel is Codex's key label for each event Coral installs a hook for (hook_event_key_label).
var codexHookEventLabel = map[string]string{
	"SessionStart":     "session_start",
	"UserPromptSubmit": "user_prompt_submit",
	"PreToolUse":       "pre_tool_use",
	"PostToolUse":      "post_tool_use",
	"Stop":             "stop",
}

// codexSessionFlagsSource is the key source Codex gives hooks set with -c: synthetic_layer_path("<session-flags>/config.toml"),
// an absolute path made against C:\ on Windows and / elsewhere.
func codexSessionFlagsSource(goos string) string {
	if goos == "windows" {
		return `C:\<session-flags>\config.toml`
	}
	return "/<session-flags>/config.toml"
}

// codexHookStateKey is Codex's hook_key for Coral's hook on event: the source, the event's label, then the matcher group and
// the handler's indexes (Coral passes one group with one handler per event).
func codexHookStateKey(goos, event string) string {
	return codexSessionFlagsSource(goos) + ":" + codexHookEventLabel[event] + ":0:0"
}

// codexHookTrustHash is Codex's hash of Coral's hook on event (version_for_toml of NormalizedHookIdentity): the sha256 of
// the compact JSON, keys sorted, of the event's label and the matcher group as Codex normalizes it, the handler with its
// default timeout (600 s) and async false, and no matcher, commandWindows or statusMessage, as codexHookArgs passes it.
// The JSON is written without HTML escaping, as serde_json writes it ("&" stays "&").
func codexHookTrustHash(event, command string) string {
	identity := map[string]any{
		"event_name": codexHookEventLabel[event],
		"hooks": []any{map[string]any{
			"type":    "command",
			"command": command,
			"timeout": 600,
			"async":   false,
		}},
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(identity) // maps encode with sorted keys
	sum := sha256.Sum256(bytes.TrimSuffix(b.Bytes(), []byte("\n")))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// codexHookTrustArgs is the -c flag that trusts Coral's hook command on each of codexHookEvents, for this launch only:
// hooks.state set to an inline table of the five entries. One flag for the table, because Codex splits a -c key on every "."
// (apply_toml_override) and the entries' keys hold one ("config.toml"); inside the value the TOML parser reads them as
// literal ('...') keys, backslashes and all.
func codexHookTrustArgs(goos, command string) []string {
	entries := make([]string, 0, len(codexHookEvents))
	for _, event := range codexHookEvents {
		entries = append(entries, fmt.Sprintf("'%s'={trusted_hash=%s}", codexHookStateKey(goos, event), tomlString(codexHookTrustHash(event, command))))
	}
	return []string{"-c", "hooks.state={" + strings.Join(entries, ",") + "}"}
}

// codexTrustSwitch reads the setting "Trust Coral's hooks in Codex" (codex_trust_coral_hooks): set once at startup from the
// settings store, read on each launch. nil is on (the default).
var codexTrustSwitch atomic.Pointer[func() bool]

// SetCodexHookTrust sets the switch codexHookArgs reads.
func SetCodexHookTrust(on func() bool) { codexTrustSwitch.Store(&on) }

func codexTrustOn() bool {
	if f := codexTrustSwitch.Load(); f != nil && *f != nil {
		return (*f)()
	}
	return true
}

// codexHooksFolderSafe reports whether the hook tool at toolPath, and the folder it is in, can be changed by nobody but
// SYSTEM, Administrators and TrustedInstaller (secfile.Writers names everyone else, this user included, and an access list
// it can't read is an error), and why not. A test replaces it.
var codexHooksFolderSafe = func(toolPath string) (bool, string) {
	for _, p := range []string{filepath.Dir(toolPath), toolPath} {
		who, err := secfile.Writers(p)
		if err != nil {
			return false, fmt.Sprintf("its access list could not be read (%s: %v)", p, err)
		}
		if len(who) > 0 {
			return false, fmt.Sprintf("%s can be changed by %s, not only by an administrator", p, strings.Join(who, ", "))
		}
	}
	return true, ""
}

// CodexHookTrust says whether Coral would trust its Codex hooks on a launch now, and why not: the switch, the platform, a
// hook tool with no absolute path, or a folder others can change. For the settings page and the launch.
func CodexHookTrust() (trusted bool, why string) {
	if !codexTrustOn() {
		return false, "turned off in Settings"
	}
	if runtime.GOOS != "windows" {
		return false, "only on Windows: an app folder here is owned by its user, so no check tells an installed Coral from one this user can change"
	}
	toolPath := hookToolPath("coral-hook-agentic-state")
	if toolPath == "" {
		return false, "the hook tool has no absolute path (no tools folder, or no tool in it)"
	}
	if ok, why := codexHooksFolderSafe(filepath.FromSlash(toolPath)); !ok {
		return false, "Coral's hooks folder isn't protected: " + why
	}
	return true, ""
}

// codexHookTrustFor is the trust flags for command on this launch, or none with one log line saying why.
func codexHookTrustFor(sessionID, command string) []string {
	if ok, why := CodexHookTrust(); !ok {
		slog.Info("codex asks the person to review Coral's hooks: Coral doesn't trust them for it", "session", sessionID, "why", why)
		return nil
	}
	return codexHookTrustArgs(runtime.GOOS, command)
}
