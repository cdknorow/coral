package startup

import (
	"context"
	"log"
	"os"
	"path/filepath"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/secfile"
	"github.com/subgentic/coral/internal/store"
)

// setupSessionTokens turns on per-session agent tokens (#3387): every launch line
// built from now on mints a token for its session and hands it over through a file
// in <data>/session_tokens (0700), which the agent's shell reads and deletes. Files
// left by a launch that never ran are removed here, at startup.
//
// Threat model: the token stops mistakes and prompt-injected impersonation through
// coral-board and the hooks. It does not stop a malicious process running as the same
// user, which can read another process's environment (/proc/<pid>/environ, ps -E).
func setupSessionTokens(db *store.DB, coralDir string) {
	dir := filepath.Join(coralDir, "session_tokens")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Printf("[identity] session tokens off: %v", err)
		return
	}
	_ = os.Chmod(dir, 0o700)
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	agent.SessionTokenMinter = func(sessionID, sessionName string) string {
		token, err := db.MintSessionToken(context.Background(), sessionID, sessionName)
		if err != nil {
			log.Printf("[identity] could not mint a session token for %s: %v", sessionName, err)
			return ""
		}
		// CreateTemp: O_CREATE|O_EXCL, mode 0600, an unpredictable name.
		f, err := os.CreateTemp(dir, "t-*")
		if err != nil {
			log.Printf("[identity] could not write a session token file: %v", err)
			return ""
		}
		if err := secfile.Protect(f.Name()); err != nil { // Windows ignores the mode (#3905)
			log.Printf("[identity] could not make a session token file private: %v", err)
			f.Close()
			os.Remove(f.Name())
			return ""
		}
		_, werr := f.WriteString(token)
		cerr := f.Close()
		if werr != nil || cerr != nil {
			os.Remove(f.Name())
			return ""
		}
		return f.Name()
	}
}
