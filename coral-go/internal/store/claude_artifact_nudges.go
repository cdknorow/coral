package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

// ClaudeArtifactNudgeKeep is how long a claude.ai artifact stays remembered as seen (#6583).
const ClaudeArtifactNudgeKeep = 30 * 24 * time.Hour

// maxClaudeArtifactKeyBytes is how much of a key is read before it is hashed.
const maxClaudeArtifactKeyBytes = 1 << 10

// hashedClaudeArtifactKey keeps a key's kind and the sha256 of the rest (cut to 1 KiB first): a file path carries the home folder and a url the page, and the
// table is plain data, so neither is stored.
func hashedClaudeArtifactKey(key string) string {
	kind, rest, _ := strings.Cut(key, ":")
	if len(rest) > maxClaudeArtifactKeyBytes {
		rest = rest[:maxClaudeArtifactKeyBytes]
	}
	sum := sha256.Sum256([]byte(rest))
	return kind + ":" + hex.EncodeToString(sum[:])
}

// ClaimClaudeArtifactSeen records that the artifact named by key was seen, and reports whether this is the first time (so the caller reminds once).
// key is "tool:<session>:<artifact key>" or "link:<claude.ai artifact id>" (see the table). Rows past ClaudeArtifactNudgeKeep are deleted first.
func (s *SessionStore) ClaimClaudeArtifactSeen(ctx context.Context, key, sessionID string, now time.Time) (bool, error) {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM claude_artifact_nudges WHERE seen_at < ?`, now.Add(-ClaudeArtifactNudgeKeep).UTC().Format(ISOFormat)); err != nil {
		return false, err
	}
	res, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO claude_artifact_nudges (key, session_id, seen_at) VALUES (?, ?, ?)`, hashedClaudeArtifactKey(key), sessionID, now.UTC().Format(ISOFormat))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return err == nil && n == 1, err
}
