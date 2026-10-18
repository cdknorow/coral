package server

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
)

// The agent docs this binary serves at /api/agent-docs (agents and coral-hub's mirror read them) are a copy:
// tools/sync-agent-docs.sh fills the gitignored static/agent_docs from agent_docs/ before a build (the Makefile, release.yml
// and run_cdp_suites.mjs run it), and a plain go build embeds whatever the checkout held then, nothing in a fresh worktree
// or an old copy (#4533, Security's SHOULD on #4528). The startup log says which.

// agentDocsDigest is the number of docs in fsys and the first 8 hex digits of a hash over their names and bytes, in walk
// (lexical) order.
func agentDocsDigest(fsys fs.FS) (int, string) {
	h := sha256.New()
	n := 0
	_ = fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil
		}
		n++
		io.WriteString(h, name)
		h.Write([]byte{0})
		h.Write(data)
		h.Write([]byte{0})
		return nil
	})
	return n, hex.EncodeToString(h.Sum(nil))[:8]
}

// agentDocsStartupLine is the startup log line for the agent docs this build embeds: their count and hash, or a warning
// when there are none.
func agentDocsStartupLine(fsys fs.FS) string {
	n, sum := agentDocsDigest(fsys)
	if n == 0 {
		return "[STARTUP] WARNING: this build embeds no agent docs, so /api/agent-docs serves none. " +
			"Run tools/sync-agent-docs.sh before go build (make and the release builds do)."
	}
	return fmt.Sprintf("[STARTUP] agent docs %d files, sha %s", n, sum)
}
