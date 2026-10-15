package background

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// A workflow run's folder (#3587). Every step gets the run folder's path in its
// environment (CORAL_WORKFLOW_RUN_DIR), so a step, an agent step under its CLI's
// permission prompts included, can put a symlink where a later file will go: a step folder,
// stdout.txt, stderr.txt, exit_code. Coral writes and reads those files itself, outside any
// such prompt, so it never does it by path:
//
//   - the run folder is made fresh (0700) and opened once as an os.Root, before any step runs;
//   - every step file is created through that root with O_CREATE|O_EXCL (0600): a planted
//     name fails the step instead of being followed, and the root refuses any path, symlink
//     or not, that leads out of the run folder;
//   - output tails are read through the root with a size cap, and a FIFO or device a step
//     put there is refused (opened non-blocking, then required to be a regular file).

// tailBytes caps how much of a step's output Coral reads back for its result.
const tailBytes = 256 << 10

// runFiles is a run's folder, held open.
type runFiles struct {
	root *os.Root
	dir  string // its path, for the steps' environment and the file list
}

// openRunFolder makes <dataDir>/workflows/runs/<id> and opens it. A folder left by an
// earlier database (the same id) is moved aside, never reused; a symlink or a file there is
// refused.
func openRunFolder(dataDir string, runID int64) (*runFiles, error) {
	runs := filepath.Join(dataDir, "workflows", "runs")
	if err := os.MkdirAll(runs, 0o700); err != nil {
		return nil, err
	}
	dir := filepath.Join(runs, strconv.FormatInt(runID, 10))
	if fi, err := os.Lstat(dir); err == nil {
		if !fi.IsDir() {
			return nil, fmt.Errorf("the run folder %s exists and isn't a folder", dir)
		}
		if err := os.Rename(dir, dir+".stale-"+strconv.FormatInt(time.Now().UnixNano(), 10)); err != nil {
			return nil, err
		}
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	// The folder opened is the one just made (not a symlink swapped in after the Mkdir).
	made, err1 := os.Lstat(dir)
	opened, err2 := root.Stat(".")
	if err1 != nil || err2 != nil || !made.IsDir() || !os.SameFile(made, opened) {
		root.Close()
		return nil, fmt.Errorf("the run folder %s changed while it was being made", dir)
	}
	return &runFiles{root: root, dir: dir}, nil
}

func (rf *runFiles) Close() error { return rf.root.Close() }

// writeNew writes a file that must not exist yet, inside the run folder.
func (rf *runFiles) writeNew(rel string, data []byte) error {
	f, err := rf.root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL|oNoFollow, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// step makes step_<i> and step_<i>/artifacts, which must not exist yet.
func (rf *runFiles) step(i int) (stepFiles, error) {
	sf := stepFiles{run: rf, rel: fmt.Sprintf("step_%d", i)}
	sf.dir = filepath.Join(rf.dir, sf.rel)
	if err := rf.root.Mkdir(sf.rel, 0o700); err != nil {
		return sf, fmt.Errorf("the step folder %s already exists or can't be made: %w", sf.rel, err)
	}
	if err := rf.root.Mkdir(filepath.Join(sf.rel, "artifacts"), 0o700); err != nil {
		return sf, fmt.Errorf("the step's artifacts folder can't be made: %w", err)
	}
	return sf, nil
}

// stepFiles is one step's folder inside a run folder.
type stepFiles struct {
	run *runFiles
	rel string // step_<i>, inside the run folder
	dir string // its path, for the step's environment
}

// create makes a new file in the step folder (O_EXCL, 0600).
func (sf stepFiles) create(name string) (*os.File, error) {
	return sf.run.root.OpenFile(filepath.Join(sf.rel, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL|oNoFollow, 0o600)
}

func (sf stepFiles) writeNew(name string, data []byte) error {
	return sf.run.writeNew(filepath.Join(sf.rel, name), data)
}

// open opens a regular file in the step folder for reading.
func (sf stepFiles) open(name string) (*os.File, error) {
	f, err := sf.run.root.OpenFile(filepath.Join(sf.rel, name), os.O_RDONLY|oNoFollow|oNonBlock, 0)
	if err != nil {
		return nil, err
	}
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("%s is not a regular file", name)
	}
	return f, nil
}

// readTail returns the last maxLines lines of a step file, reading at most tailBytes.
func (sf stepFiles) readTail(name string, maxLines int) string {
	f, err := sf.open(name)
	if err != nil {
		return ""
	}
	defer f.Close()
	return tailOf(f, maxLines)
}

func tailOf(f *os.File, maxLines int) string {
	if fi, err := f.Stat(); err == nil && fi.Size() > tailBytes {
		if _, err := f.Seek(fi.Size()-tailBytes, io.SeekStart); err != nil {
			return ""
		}
	}
	data, err := io.ReadAll(io.LimitReader(f, tailBytes))
	if err != nil {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.Join(lines, "\n")
}

// appendNote adds a line to an existing step file (never creating or following one).
func (sf stepFiles) appendNote(name, line string) {
	f, err := sf.run.root.OpenFile(filepath.Join(sf.rel, name), os.O_WRONLY|os.O_APPEND|oNoFollow, 0)
	if err != nil {
		return
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Mode().IsRegular() {
		fmt.Fprintln(f, line)
	}
}

// writeArtifact copies the step's stdout to its output_artifact, a relative path inside the
// step folder, through the run folder's root.
func (sf stepFiles) writeArtifact(rel string) error {
	if !filepath.IsLocal(rel) {
		return errArtifactPath
	}
	p := filepath.Join(sf.rel, rel)
	if err := sf.run.root.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	if fi, err := sf.run.root.Lstat(p); err == nil && !fi.Mode().IsRegular() {
		return fmt.Errorf("%s exists and is not a regular file", rel)
	}
	src, err := sf.open("stdout.txt")
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := sf.run.root.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|oNoFollow, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(dst, src)
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	return err
}

// readArtifact reads a step's output_artifact (at most max bytes) through the run folder's
// root.
func (sf stepFiles) readArtifact(rel string, max int64) ([]byte, error) {
	if !filepath.IsLocal(rel) {
		return nil, errArtifactPath
	}
	f, err := sf.open(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, max))
}

var errNotInRun = errors.New("not a file in the run folder")

// readInRun reads p, a path inside runDir, through an os.Root of runDir: what a template
// reads ({{prev_stdout_content}} and the like) can't be led out of the run folder. runDir
// itself must be a real folder, not a symlink.
func readInRun(runDir, p string, max int64) ([]byte, error) {
	rel, err := filepath.Rel(runDir, p)
	if runDir == "" || err != nil || !filepath.IsLocal(rel) {
		return nil, errNotInRun
	}
	if fi, err := os.Lstat(runDir); err != nil || !fi.IsDir() {
		return nil, errNotInRun
	}
	root, err := os.OpenRoot(runDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.OpenFile(rel, os.O_RDONLY|oNoFollow|oNonBlock, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", filepath.Base(p))
	}
	return io.ReadAll(io.LimitReader(f, max))
}
