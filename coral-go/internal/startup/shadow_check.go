package startup

import (
	"os"
	"os/exec"
	"path/filepath"
)

// ShadowWarning describes a `coral` on PATH that is not this executable.
type ShadowWarning struct {
	// OnPath is what a user typing "coral" would actually run.
	OnPath string
	// Running is the executable currently running.
	Running string
}

// CheckPATHShadowing reports whether typing "coral" would run something other
// than the binary that is running now.
//
// This exists because an abandoned PyPI package ships binaries with the same
// six names as ours, uses the same ~/.coral directory, the same database
// filenames and the same port. pip --user installs into ~/.local/bin, which
// comes before /usr/local/bin on a default macOS PATH. A user who followed the
// old docs site, ran that pip install, and then installed our app could type
// "coral" and silently start the other product — which comes up, serves a
// dashboard on 8420, and looks like it worked.
//
// It returns nil when there is nothing to warn about: no "coral" on PATH, or
// the one on PATH is this same file. Symlinks are resolved on both sides, so
// linking our binary into a directory on PATH — exactly what install-cli.sh
// does — is correctly seen as the same program, not as a conflict.
func CheckPATHShadowing() *ShadowWarning {
	running, err := os.Executable()
	if err != nil {
		return nil
	}
	onPath, err := exec.LookPath("coral")
	if err != nil {
		return nil // nothing named coral on PATH; nothing to shadow
	}
	if sameFile(running, onPath) {
		return nil
	}
	return &ShadowWarning{OnPath: resolvePath(onPath), Running: resolvePath(running)}
}

// sameFile reports whether two paths refer to the same file, following symlinks
// and falling back to an inode comparison for hard links.
func sameFile(a, b string) bool {
	ra, rb := resolvePath(a), resolvePath(b)
	if ra == rb {
		return true
	}
	fa, err := os.Stat(ra)
	if err != nil {
		return false
	}
	fb, err := os.Stat(rb)
	if err != nil {
		return false
	}
	return os.SameFile(fa, fb)
}

func resolvePath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}
