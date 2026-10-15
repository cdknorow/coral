package server

import (
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"runtime"
)

// tightenDataPermissions makes Coral's data readable by this user only (#3552, Security's
// #3551 audit): the data folder and the artifacts tree's folders 0700; the databases (and
// their -wal/-shm, which SQLite creates with the database's own mode), coral.log and every
// artifact file (objects, sidecars, upload records, changes diffs) 0600. New ones are
// created that way; this pass fixes what an older Coral left, at every start (it only
// touches what is looser, and logs once when it changed something). Symlinks are never
// followed; Windows has no such modes and is skipped.
func tightenDataPermissions(coralDir string) {
	if runtime.GOOS == "windows" || coralDir == "" {
		return
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
	_ = filepath.WalkDir(filepath.Join(coralDir, "artifacts"), func(path string, d fs.DirEntry, err error) error {
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
	if changed > 0 {
		log.Printf("[data] made %d files and folders in the data folder readable by this user only (folders 0700, files 0600)", changed)
	}
}
