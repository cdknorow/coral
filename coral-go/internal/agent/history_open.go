package agent

import (
	"os"
	"path/filepath"

	"github.com/cdknorow/coral/internal/safefs"
)

// maxHistoryJSONBytes caps a legacy Gemini session file, which is read whole.
const maxHistoryJSONBytes = 64 << 20

// openHistoryFile opens a CLI's history file for indexing (#3601, weak site W21): a regular
// file only, opened non-blocking, never through a symlink as its last name. The history folders
// are the agents' own, so a symlink to another file or a FIFO planted there is skipped instead
// of read (or waited on, which stalled the indexer).
func openHistoryFile(fpath string) (*os.File, error) {
	root, err := safefs.OpenRoot(filepath.Dir(fpath))
	if err != nil {
		return nil, err
	}
	defer root.Close() // the file stays open
	f, _, err := root.Open(filepath.Base(fpath))
	return f, err
}

// readHistoryFile reads a whole history file the same way, at most maxHistoryJSONBytes.
func readHistoryFile(fpath string) ([]byte, error) {
	root, err := safefs.OpenRoot(filepath.Dir(fpath))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.ReadFile(filepath.Base(fpath), maxHistoryJSONBytes)
}
