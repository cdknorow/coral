package server

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/store"
)

// applyBoardRekeys carries the board store's re-keys (#3541: a legacy board named "." or
// ".." got a fresh id) into what lives outside messageboard.db: the sessions database's
// board columns (live sessions, teams, token usage, call metrics) and the agents' coral-board
// state files. Idempotent: it runs at every start, and a key already moved matches nothing.
func applyBoardRekeys(ctx context.Context, bs *board.Store, db *store.DB, coralDir string) {
	if bs == nil {
		return
	}
	rekeys, err := bs.BoardRekeys(ctx)
	if err != nil || len(rekeys) == 0 {
		return
	}
	for old, id := range rekeys {
		if db != nil {
			for _, q := range []string{
				`UPDATE live_sessions SET board_name = ? WHERE board_name = ?`,
				`UPDATE teams SET name = ? WHERE name = ?`,
				`UPDATE token_usage SET board_name = ? WHERE board_name = ?`,
				`UPDATE call_metrics SET board_name = ? WHERE board_name = ?`,
			} {
				if res, err := db.ExecContext(ctx, q, id, old); err != nil {
					log.Printf("[boards] re-key %s in the sessions database: %v", id, err)
				} else if n, _ := res.RowsAffected(); n > 0 {
					log.Printf("[boards] a board whose name can't be an id is now %s (%d rows in the sessions database)", id, n)
				}
			}
		}
		rekeyStateFiles(coralDir, old, id)
	}
}

// rekeyStateFiles points coral-board state files (board_state_<session>.json) at the new id:
// only regular files (never a symlink) whose project is exactly the old key, rewritten
// atomically (a temporary file in the same folder, then a rename) with the file's own mode.
func rekeyStateFiles(coralDir, old, id string) {
	files, _ := filepath.Glob(filepath.Join(coralDir, "board_state_*.json"))
	for _, f := range files {
		fi, err := os.Lstat(f)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		data, err := readNoFollow(f)
		if err != nil {
			continue
		}
		// Raw values: every other field is written back byte-exact.
		var st map[string]json.RawMessage
		var project string
		if json.Unmarshal(data, &st) != nil || json.Unmarshal(st["project"], &project) != nil || project != old {
			continue
		}
		st["project"], _ = json.Marshal(id)
		out, err := json.Marshal(st)
		if err != nil {
			continue
		}
		tmp, err := os.CreateTemp(filepath.Dir(f), ".board_state_rekey_*")
		if err != nil {
			continue
		}
		_, werr := tmp.Write(out)
		cerr := tmp.Close()
		if werr != nil || cerr != nil || os.Chmod(tmp.Name(), fi.Mode().Perm()) != nil || os.Rename(tmp.Name(), f) != nil {
			os.Remove(tmp.Name())
		}
	}
}

// readNoFollow reads a file without following a symlink at its last element.
func readNoFollow(path string) ([]byte, error) {
	fh, err := os.OpenFile(path, os.O_RDONLY|noFollowFlag, 0)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	return io.ReadAll(io.LimitReader(fh, 1<<20))
}
