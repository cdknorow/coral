package server

import (
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"runtime"

	"github.com/cdknorow/coral/internal/config"
)

// tightenDataPermissions makes Coral's data readable by this user only (#3552, Security's
// #3551 audit): the data folder and the artifacts tree's folders 0700; the databases (and
// their -wal/-shm, which SQLite creates with the database's own mode), coral.log and every
// artifact file (objects, sidecars, upload records, changes diffs) 0600. New ones are
// created that way; this pass fixes what an older Coral left, at every start (it only
// touches what is looser, and logs once when it changed something). Symlinks are never
// followed; Windows has no such modes and is skipped.
func tightenDataPermissions(coralDir string) {
	if coralDir == "" {
		return
	}
	if runtime.GOOS == "windows" {
		warnBroadDataFolder(coralDir)
		return
	}
	// A data folder reached through a symlink: tighten the real folder (resolved once; no
	// link below it is followed).
	if real, err := filepath.EvalSymlinks(coralDir); err == nil {
		coralDir = real
	}
	changed := 0
	tighten := func(path string, mode os.FileMode) {
		fi, err := os.Lstat(path)
		if err != nil || fi.Mode()&os.ModeSymlink != 0 {
			return
		}
		if fi.Mode().Perm()&^mode != 0 {
			if os.Chmod(path, fi.Mode().Perm()&mode) == nil {
				changed++
			}
		}
	}
	tighten(coralDir, 0o700)
	for _, pattern := range []string{"*.db", "*.db-wal", "*.db-shm", "coral.log"} {
		matches, _ := filepath.Glob(filepath.Join(coralDir, pattern))
		for _, m := range matches {
			tighten(m, 0o600)
		}
	}
	// The artifacts tree and the dashboard's uploads (#3602 W28: they were 0755 and 0644).
	for _, tree := range []string{"artifacts", "uploads"} {
		_ = filepath.WalkDir(filepath.Join(coralDir, tree), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			switch {
			case d.Type()&fs.ModeSymlink != 0:
			case d.IsDir():
				tighten(path, 0o700)
			case d.Type().IsRegular():
				tighten(path, 0o600)
			}
			return nil
		})
	}
	if changed > 0 {
		log.Printf("[data] made %d files and folders in the data folder readable by this user only (folders 0700, files 0600)", changed)
	}
}

// warnBroadDataFolder: on Windows the data folder's access list is the protection for the API
// key, the command-line token and the databases; a folder others can read is reported loudly at
// every start (#3637). Coral doesn't change an existing folder's access list itself.
func warnBroadDataFolder(coralDir string) {
	if msg := config.DataDirWarning(coralDir); msg != "" {
		log.Printf("[security] %s", msg)
	}
}
