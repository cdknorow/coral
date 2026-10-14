package background

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Workflow step artifacts and step-output templates (#3423, audit CRIT-01, CRIT-02, MED-03).

// maxArtifactEnv caps CORAL_PREV_ARTIFACT_CONTENT: a bigger artifact is left out of the
// environment (CORAL_PREV_ARTIFACT_TRUNCATED=1; read the file at CORAL_PREV_ARTIFACT), so a
// large one can't make the step fail to start (E2BIG).
const maxArtifactEnv = 64 << 10

// maxArtifactRead bounds what Coral reads back from an artifact for a template.
const maxArtifactRead = 16 << 20

var errArtifactPath = errors.New("output_artifact must be a relative path inside the step's folder")

// artifactPath is where a step's output_artifact lives: inside stepDir, never escaping it
// (no absolute path, no "..", no symlinked folder on the way).
func artifactPath(stepDir, rel string) (string, error) {
	if !filepath.IsLocal(rel) {
		return "", errArtifactPath
	}
	p := filepath.Join(stepDir, rel)
	if err := insideReal(stepDir, filepath.Dir(p)); err != nil {
		return "", err
	}
	return p, nil
}

// insideReal reports whether dir, with symlinks resolved, is root or inside it. A dir
// that doesn't exist yet is checked through its nearest existing parent.
func insideReal(root, dir string) error {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	probe := dir
	for {
		if _, err := os.Lstat(probe); err == nil {
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return errArtifactPath
		}
		probe = parent
	}
	real, err := filepath.EvalSymlinks(probe)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(realRoot, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return errArtifactPath
	}
	return nil
}

// writeArtifact copies a step's stdout to its output_artifact, inside stepDir, without
// following a symlink at the destination.
func writeArtifact(stepDir, rel string) error {
	p, err := artifactPath(stepDir, rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := insideReal(stepDir, filepath.Dir(p)); err != nil {
		return err // a folder on the way was swapped for a link
	}
	src, err := os.Open(filepath.Join(stepDir, "stdout.txt"))
	if err != nil {
		return err
	}
	defer src.Close()
	if fi, err := os.Lstat(p); err == nil && !fi.Mode().IsRegular() {
		return fmt.Errorf("%s exists and is not a regular file", rel)
	}
	dst, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|oNoFollow, 0o644)
	if err != nil {
		return err
	}
	_, err = io.Copy(dst, src)
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	return err
}

// readArtifact reads a step's artifact (at most maxArtifactRead), refusing a path outside
// stepDir or a symlink.
func readArtifact(stepDir, rel string) ([]byte, error) {
	p, err := artifactPath(stepDir, rel)
	if err != nil {
		return nil, err
	}
	return readRegular(p, maxArtifactRead)
}

// readRegular reads a regular file that is not a symlink, at most max bytes.
func readRegular(p string, max int64) ([]byte, error) {
	fi, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", filepath.Base(p))
	}
	f, err := os.OpenFile(p, os.O_RDONLY|oNoFollow, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, max))
}

// stripNUL removes NUL bytes, which neither an environment variable nor a shell word can hold.
func stripNUL(s string) string { return strings.ReplaceAll(s, "\x00", "") }

// shellWord quotes s as one POSIX shell word: inside single quotes nothing is expanded, so
// step output can never change the command's syntax (CRIT-01).
func shellWord(s string) string {
	return "'" + strings.ReplaceAll(stripNUL(s), "'", `'\''`) + "'"
}

// noteArtifactRefused makes a refused output_artifact visible in the step (#3423 F2): a
// note in its result and a line at the end of its stderr, not only the server log.
func noteArtifactRefused(stepDir, rel string, err error, result *StepResult) {
	note := fmt.Sprintf("output_artifact %q was not written: %v", rel, err)
	if result != nil {
		result.Note = note
	}
	if f, ferr := os.OpenFile(filepath.Join(stepDir, "stderr.txt"), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644); ferr == nil {
		fmt.Fprintf(f, "coral: %s\n", note)
		f.Close()
	}
}
