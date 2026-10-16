package gitutil

import (
	"path/filepath"

	"github.com/cdknorow/coral/internal/safefs"
)

// Test-only, kept out of the shipped binary (#3824, Go cleanup 2: audit #3780 section B). The
// product calls NewFileLineCountIn with the root it already holds.

// NewFileLineCount reports how many lines an untracked file adds, matching the
// additions `git diff` reports for it. A trailing newline terminates the last
// line rather than starting another, so only an unterminated final line counts
// extra. Binary files have no line count in a diff and report 0, as do files
// larger than MaxNewFileStatBytes and anything unreadable. Only a regular file is
// read (#3597, weak site W18): never through a symlink, never a FIFO or device
// (which could block the poller or read without end).
func NewFileLineCount(path string) int {
	root, err := safefs.OpenRoot(filepath.Dir(path))
	if err != nil {
		return 0
	}
	defer root.Close()
	return NewFileLineCountIn(root, filepath.Base(path))
}
