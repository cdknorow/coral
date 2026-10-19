package background

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cdknorow/coral/internal/safefs"
	"github.com/cdknorow/coral/internal/safego"
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
	// Never a file with another name somewhere else: a step could have replaced stderr.txt
	// with a hard link to an outside file it owns (#3587, Security's SHOULD 1).
	// The count comes from the handle (Windows's FileInfo has none, #3615); unknown is refused.
	if fi, err := f.Stat(); err == nil && fi.Mode().IsRegular() {
		if n, ok := safefs.LinkCount(f, fi); ok && n == 1 {
			fmt.Fprintln(f, line)
		}
	}
}

// writeArtifact copies the step's stdout to its output_artifact, a new file at a relative path
// inside the step folder, through the run folder's root.
func (sf stepFiles) writeArtifact(rel string) error {
	if !filepath.IsLocal(rel) {
		return errArtifactPath
	}
	p := filepath.Join(sf.rel, rel)
	if err := sf.run.root.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	src, err := sf.open("stdout.txt")
	if err != nil {
		return err
	}
	defer src.Close()
	// A new file only: an existing name (a symlink, or a hard link to an outside file) is
	// never written through, and fails the step's artifact (#3587).
	dst, err := sf.run.root.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL|oNoFollow, 0o600)
	if err != nil {
		return fmt.Errorf("%s already exists or can't be made: %w", rel, err)
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

// stepOutputCap is how much of each of a step's stdout and stderr is kept on disk (a var for the tests). A command that prints more is not stopped:
// the rest is read and dropped, and the step says so (#5079).
var stepOutputCap int64 = 64 << 20

// stepPipeWait is how long after a step's process ends the runner still waits for what it wrote to reach the files, so a normal step's output is all there
// when its result is read. A process the step left running is not waited for: it keeps writing (capped) and is never killed for it.
const stepPipeWait = 3 * time.Second

// cappedWriter writes the first left bytes it is given to w and drops the rest. It never returns an error (a copier that stopped reading would leave the
// command blocked on a full pipe): a failed write (a full disk) drops the output from there on and is reported once, in the step's note.
type cappedWriter struct {
	w         io.Writer
	left      int64
	truncated atomic.Bool
	writeErr  atomic.Value // error
}

func (c *cappedWriter) Write(p []byte) (int, error) {
	if c.writeErr.Load() != nil {
		return len(p), nil
	}
	if c.left > 0 {
		n := int64(len(p))
		if n > c.left {
			n = c.left
			c.truncated.Store(true)
		}
		if _, err := c.w.Write(p[:n]); err != nil {
			c.writeErr.Store(err)
			return len(p), nil
		}
		c.left -= n
	} else if len(p) > 0 {
		c.truncated.Store(true)
	}
	return len(p), nil
}

// stepStream is one of a step's two output streams: the pipe the child writes to, and the goroutine that copies it into the capped file.
type stepStream struct {
	name string
	pw   *os.File // the child's end
	capw *cappedWriter
	done chan struct{}
}

// stepOutput is a step's stdout and stderr.
type stepOutput struct {
	out, errs *stepStream
	once      sync.Once
}

func newStepStream(sf stepFiles, name string) (*stepStream, error) {
	f, err := sf.create(name)
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", name, err)
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("pipe for %s: %w", name, err)
	}
	st := &stepStream{name: name, pw: pw, capw: &cappedWriter{w: f, left: stepOutputCap}, done: make(chan struct{})}
	go func() {
		defer safego.Recover("background/workflow_runfolder:step stream copier")
		// The file is closed here, at the end of the stream, not by the step: a process the step left running still writes to it.
		defer close(st.done)
		defer f.Close()
		defer pr.Close()
		_, _ = io.Copy(st.capw, pr)
	}()
	return st, nil
}

// newStepOutput makes the step's two files and the pipes and copiers in front of them.
func newStepOutput(sf stepFiles) (*stepOutput, error) {
	out, err := newStepStream(sf, "stdout.txt")
	if err != nil {
		return nil, err
	}
	errs, err := newStepStream(sf, "stderr.txt")
	if err != nil {
		out.pw.Close() // its copier sees the end and closes its file
		return nil, err
	}
	return &stepOutput{out: out, errs: errs}, nil
}

// attach gives the command the pipes' write ends (files: exec starts no copier of its own and Wait returns when the process exits).
func (o *stepOutput) attach(cmd *exec.Cmd) {
	cmd.Stdout = o.out.pw
	cmd.Stderr = o.errs.pw
}

// release closes this process's copies of the write ends: from here the child's (and its children's) are the only ones, and the streams end when the last
// closes. Called once the command started or failed to.
func (o *stepOutput) release() {
	o.once.Do(func() {
		o.out.pw.Close()
		o.errs.pw.Close()
	})
}

// finish waits (at most stepPipeWait) for what the command wrote to reach its files, then says in the step's files and result what was cut at the cap or lost
// to a write error.
func (o *stepOutput) finish(sf stepFiles, result *StepResult) {
	expired := make(chan struct{}) // closed, so it answers for both streams (a timer's channel is read once)
	timer := time.AfterFunc(stepPipeWait, func() { close(expired) })
	defer timer.Stop()
	for _, st := range []*stepStream{o.out, o.errs} {
		select {
		case <-st.done:
		case <-expired:
		}
	}
	var cut, lost []string
	for _, st := range []*stepStream{o.out, o.errs} {
		if st.capw.truncated.Load() {
			cut = append(cut, st.name)
			sf.appendNote(st.name, fmt.Sprintf("coral: output cut here: %s keeps its first %d MiB", st.name, stepOutputCap>>20))
		}
		if err, _ := st.capw.writeErr.Load().(error); err != nil {
			lost = append(lost, st.name+" ("+err.Error()+")")
		}
	}
	var notes []string
	if len(cut) > 0 {
		notes = append(notes, "output truncated: "+strings.Join(cut, " and ")+" kept only its first "+fmt.Sprint(stepOutputCap>>20)+" MiB")
	}
	if len(lost) > 0 {
		notes = append(notes, "output lost after a write error: "+strings.Join(lost, " and "))
	}
	if len(notes) > 0 && result != nil {
		if result.Note != "" {
			notes = append([]string{result.Note}, notes...)
		}
		result.Note = strings.Join(notes, "; ")
	}
}
