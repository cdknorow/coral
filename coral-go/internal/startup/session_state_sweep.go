package startup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/google/uuid"

	"github.com/cdknorow/coral/internal/naming"
	"github.com/cdknorow/coral/internal/safego"
	"github.com/cdknorow/coral/internal/store"
)

// sessionStateMaxAge is how long after a session ended its per-session state files are kept.
const sessionStateMaxAge = 30 * 24 * time.Hour

// The per-session state files in the data folder (#4411, Security's #4371 INFO a), by exact name:
// coral-board's board state (board_state_<agent type>-<session id>.json) and its review rounds
// (board_review_rounds_<16 hex of sha256(session name)>.json), and Pi's session folder
// (pi-sessions/<session id>/). They hold no message text; nothing removed them.
var (
	boardStateFile   = regexp.MustCompile(`^board_state_([a-z][a-z0-9]*)-([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\.json$`)
	reviewRoundsFile = regexp.MustCompile(`^board_review_rounds_([0-9a-f]{16})\.json$`)
)

// staleSession is a session this Coral's database has as ended more than sessionStateMaxAge ago.
type staleSession struct{ agentType string }

// staleSessions are the sessions this Coral's database has as ended (inactive, not sleeping, not
// archived) with a stop time before cutoff. A session the database doesn't have, or has with no
// readable stop time, is not in it.
func staleSessions(ctx context.Context, db *store.DB, cutoff time.Time) (map[string]staleSession, error) {
	var rows []struct {
		ID        string  `db:"session_id"`
		AgentType string  `db:"agent_type"`
		StoppedAt *string `db:"stopped_at"`
	}
	if err := db.Reader().SelectContext(ctx, &rows,
		`SELECT session_id, agent_type, stopped_at FROM live_sessions
		 WHERE status != 'active' AND is_sleeping = 0 AND is_archived = 0 AND stopped_at IS NOT NULL`); err != nil {
		return nil, err
	}
	out := map[string]staleSession{}
	for _, r := range rows {
		if r.StoppedAt == nil {
			continue
		}
		var at time.Time
		var err error
		for _, layout := range []string{store.ISOFormat, time.RFC3339Nano} {
			if at, err = time.Parse(layout, *r.StoppedAt); err == nil {
				break
			}
		}
		if err == nil && at.Before(cutoff) {
			out[r.ID] = staleSession{agentType: r.AgentType}
		}
	}
	return out, nil
}

// sweepSessionStateFiles removes, from dataDir, the per-session state of the stale sessions: each a
// regular file (or, for Pi, the session's own folder) owned by this user, matched by its exact name,
// and removed by that exact path. Nothing is followed through a symlink. It returns how many it
// removed.
func sweepSessionStateFiles(dataDir string, stale map[string]staleSession) int {
	if len(stale) == 0 || dataDir == "" {
		return 0
	}
	roundsOf := make(map[string]bool, len(stale)) // review-round hashes of the stale sessions' names
	for sid, s := range stale {
		sum := sha256.Sum256([]byte(naming.SessionName(s.agentType, sid)))
		roundsOf[hex.EncodeToString(sum[:8])] = true
	}
	removed := 0
	ownedRegular := func(path string) bool {
		info, err := os.Lstat(path)
		return err == nil && info.Mode().IsRegular() && ownedByThisUser(info)
	}
	if entries, err := os.ReadDir(dataDir); err == nil {
		for _, e := range entries {
			name := e.Name()
			gone := false
			if m := boardStateFile.FindStringSubmatch(name); m != nil {
				s, ok := stale[m[2]]
				gone = ok && s.agentType == m[1] && canonicalUUID(m[2])
			} else if m := reviewRoundsFile.FindStringSubmatch(name); m != nil {
				gone = roundsOf[m[1]]
			}
			if path := filepath.Join(dataDir, name); gone && ownedRegular(path) && os.Remove(path) == nil {
				removed++
			}
		}
	}
	piDir := filepath.Join(dataDir, "pi-sessions")
	if info, err := os.Lstat(piDir); err == nil && info.IsDir() {
		if entries, err := os.ReadDir(piDir); err == nil {
			for _, e := range entries {
				sid := e.Name()
				if _, ok := stale[sid]; !ok || !canonicalUUID(sid) {
					continue
				}
				path := filepath.Join(piDir, sid)
				info, err := os.Lstat(path)
				if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !ownedByThisUser(info) {
					continue
				}
				// The session's own folder: RemoveAll removes links inside it, never what they name.
				if os.RemoveAll(path) == nil {
					removed++
				}
			}
		}
	}
	return removed
}

func canonicalUUID(id string) bool {
	u, err := uuid.Parse(id)
	return err == nil && u.String() == id
}

// setupSessionStateSweep removes, in the background, the per-session state files of sessions that
// ended in this Coral more than 30 days ago (#4411). The channel closes when it is over. With no
// such session it lists nothing.
func setupSessionStateSweep(db *store.DB, dataDir string) <-chan struct{} {
	done := make(chan struct{})
	safego.Go("startup: session state sweep", func() {
		defer close(done)
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		stale, err := staleSessions(ctx, db, time.Now().Add(-sessionStateMaxAge))
		if err != nil {
			log.Printf("[startup] session state sweep skipped: %v", err)
			return
		}
		if n := sweepSessionStateFiles(dataDir, stale); n > 0 {
			log.Printf("[startup] removed %d state file(s) or folder(s) of sessions that ended over 30 days ago", n)
		}
	})
	return done
}
