package server

import (
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"runtime"

	"github.com/subgentic/coral/internal/config"
)

// tightenDataPermissions makes Coral's data readable by this user only (#3552, Security's
// #3551 audit; #3628 F6): the data folder and the folders of dataTrees 0700; dataFiles (the
// databases and their -wal/-shm, which SQLite creates with the database's own mode, the logs,
// ...) and every file in dataTrees (artifact objects, sidecars, upload records, changes diffs,
// knowledge notes, workflow runs, themes) 0600. New ones are
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
	// Every other file directly in the folder (keys, tokens, state an older Coral left): the 0700 folder keeps others out, but a copy
	// or a restore of the folder would carry a loose mode with it (#96).
	if entries, err := os.ReadDir(coralDir); err == nil {
		for _, e := range entries {
			if e.Type().IsRegular() {
				tighten(filepath.Join(coralDir, e.Name()), 0o600)
			}
		}
	}
	for _, pattern := range dataFiles {
		matches, _ := filepath.Glob(filepath.Join(coralDir, pattern))
		for _, m := range matches {
			tighten(m, 0o600)
		}
	}
	// The artifacts tree and the dashboard's uploads (#3602 W28: they were 0755 and 0644), and
	// the folders of text distilled from transcripts and of agents' working data (#3628 F6).
	for _, tree := range dataTrees {
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

// dataFiles are the data folder's files kept this user's only: the databases, the logs and their
// rotated copies, the last generated team, the server's lock and the board state files (#3628 F6:
// the logs, the team, the lock and older board state files were 0644).
var dataFiles = []string{
	"*.db", "*.db-wal", "*.db-shm",
	"coral.log", "coral.log.*", "app.log", "tray.log",
	"last_generated_team.json", "coral-server.lock", "board_state_*.json",
}

// dataTrees are the data folder's folders whose every folder is 0700 and every file 0600.
var dataTrees = []string{"artifacts", "uploads", "agent_knowledge", "workflows", "pi-sessions", "themes", "session_tokens"}

// warnBroadDataFolder: on Windows the data folder's access list is the protection for the API
// key, the command-line token and the databases; a folder others can read is reported loudly at
// every start (#3637). EnsureDataDir has already made a folder this user owns private (#96); what is left here is a folder it could
// not change and that --allow-shared-data-dir let run.
func warnBroadDataFolder(coralDir string) {
	if msg := config.DataDirWarning(coralDir); msg != "" {
		log.Printf("[security] %s", msg)
	}
}
