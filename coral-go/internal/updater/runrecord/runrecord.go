// Package runrecord is how Coral says, for the updater, how it was started (#5339): its own command line and working directory, written by the
// server at startup into <data dir>/updates/run.json (0600 in a 0700 folder) and read by coral-update's Linux apply to start Coral again after
// the swap. It is the server's own record, never a value from a request. It has no dependency on the verifier, so the server may link it.
package runrecord

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// File is the record's name in <data dir>/updates.
const File = "run.json"

// Record is what is kept. Env is the allowlisted part of the server's environment (AllowedEnv), as NAME=value: the restarted Coral gets exactly that, never the
// updater's own environment (an agent's variables, a token) and never a variable the allowlist does not name.
type Record struct {
	Argv []string `json:"argv"`
	Env  []string `json:"env,omitempty"`
	Cwd  string   `json:"cwd"`
}

// envAllowed are the variables that decide how Coral runs (where its data is, how it listens, its mode) and the ones any program needs to find its tools, a home and a
// language. Each is a name, a path, a flag or a mode, never a secret: a key or a token is read from a file or the keyring, not from here. Nothing else is recorded.
var envAllowed = map[string]bool{
	"HOMEBREW_PREFIX": true, // where a Homebrew cask is looked for, by the updater's install check (#5280 step 3 B)
	"CORAL_DATA_DIR":  true, "CORAL_DIR": true, "CORAL_HOST": true, "CORAL_HUB": true, "CORAL_STRICT_LOCAL_AUTH": true, "CORAL_DB_ENCRYPTION": true,
	"CORAL_CLOUD_URL": true, "CORAL_CLOUD_CONNECT": true, "CORAL_CLOUD_MAX_MODE": true, "CORAL_TLS_CERT": true, "CORAL_TLS_KEY": true, "CORAL_TMUX_BIN": true,
	"CORAL_NO_AUTO_LOOPBACK": true, "CORAL_STALL_WATCHDOG": true,
	"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "SHELL": true, "LANG": true, "LC_ALL": true, "TZ": true, "TMPDIR": true, "TERM": true,
	"XDG_RUNTIME_DIR": true, "XDG_DATA_HOME": true, "XDG_CONFIG_HOME": true, "XDG_STATE_HOME": true,
}

// hasUserinfo reports whether v holds a URL with credentials in it ("https://user:secret@host"): a value on the allowlist is a name, a path, a flag or a mode, and a
// URL that carries a password is not recorded whatever variable it is in (#5414).
func hasUserinfo(v string) bool {
	for rest := v; ; {
		i := strings.Index(rest, "://")
		if i < 0 {
			return false
		}
		rest = rest[i+3:]
		end := strings.IndexAny(rest, "/?# \t")
		if end < 0 {
			end = len(rest)
		}
		if strings.Contains(rest[:end], "@") {
			return true
		}
	}
}

// maxEnvValue bounds one recorded value (a PATH can be long, a record is not for payloads).
const maxEnvValue = 4096

// AllowedEnv returns the entries of environ (NAME=value) whose name is on the allowlist, in their order, once each, with a value that is not empty and not over
// maxEnvValue.
func AllowedEnv(environ []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, kv := range environ {
		name, val, ok := strings.Cut(kv, "=")
		if !ok || !envAllowed[name] || val == "" || len(val) > maxEnvValue || seen[name] || strings.ContainsAny(kv, "\x00\n") || hasUserinfo(val) {
			continue
		}
		seen[name] = true
		out = append(out, kv)
	}
	return out
}

const maxSize = 64 << 10

// ErrSecretFlag is returned for a command line with a flag that carries a secret: it is not recorded (the record is a file; a restart without
// the record is refused, not done without the flag). Start Coral with the secret in its environment or a file instead.
var ErrSecretFlag = errors.New("the command line carries a secret in a flag, so it is not recorded")

var secretFlag = regexp.MustCompile(`(?i)^--?[a-z0-9_-]*(token|secret|password|passwd|api[-_]?key|credential|private[-_]?key)[a-z0-9_-]*(=|$)`)

// Write records argv, the allowlisted part of environ and the working directory for the data folder, atomically, 0600. A flag named for a secret (--token,
// --api-key=..., --password) makes it refuse: the record must not hold one (Security's #5341 LOW 1).
func Write(dataDir string, argv, environ []string, cwd string) error {
	if len(argv) == 0 {
		return errors.New("no command line")
	}
	for _, a := range argv[1:] {
		if secretFlag.MatchString(a) || hasUserinfo(a) { // a flag named for a secret, or any argument that is a URL with a password in it
			return ErrSecretFlag
		}
	}
	dir := filepath.Join(dataDir, "updates")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// The folder is the updater's own, so it is not a link (the record would be written wherever the link leads) and it is owner-only: the reader refuses a record in a
	// folder others can enter, and a folder some earlier run left looser is tightened here (#5414).
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return errors.New("the updates folder is a link or not a folder")
	}
	if fi.Mode().Perm()&0o077 != 0 && runtime.GOOS != "windows" {
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
	}
	b, err := json.Marshal(Record{Argv: argv, Env: AllowedEnv(environ), Cwd: cwd})
	if err != nil {
		return err
	}
	if len(b) > maxSize {
		return errors.New("the command line is too long to record")
	}
	tmp, err := os.CreateTemp(dir, ".run-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(dir, File))
}
