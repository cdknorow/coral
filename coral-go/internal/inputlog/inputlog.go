// Package inputlog is an opt-in debug log of what Coral writes into agent
// terminals: every tmux send-keys and paste, and every PTY write, with the code
// path that sent it (#3256).
//
// It is OFF by default and records nothing until it is turned on, either by the
// "Log terminal input (debug)" setting (debug_input_log, applied live) or, for
// headless use, CORAL_DEBUG_INPUT=1 / --debug-input. It writes typed text, which
// may include secrets, to <data dir>/input-debug.log (mode 0600, capped, one
// rotated copy), and says so in coral.log whenever it turns on or off.
//
// Logging never blocks the sender: lines go through a bounded queue and are
// dropped (and counted) when it is full, as with the terminal copy of coral.log
// (#3226).
package inputlog

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/safego"
	"github.com/cdknorow/coral/internal/secfile"
)

const (
	// FileName is the debug log's name inside the data directory.
	FileName = "input-debug.log"
	// maxFileBytes caps input-debug.log; at the cap it is renamed to .1 (replacing
	// the previous .1) and a new file starts.
	maxFileBytes = 5 << 20
	// maxText is how much of the sent text a line shows.
	maxText    = 200
	queueDepth = 4096
	// SettingKey is the user setting that turns the log on.
	SettingKey = "debug_input_log"
)

// Kind says how the input was sent.
type Kind string

const (
	Text  Kind = "text"  // literal text (tmux send-keys -l)
	Keys  Kind = "keys"  // tmux key names (Enter, C-c, Escape, ...)
	Paste Kind = "paste" // a tmux buffer pasted into the pane
	Raw   Kind = "raw"   // bytes written to a PTY
)

var (
	enabled atomic.Bool // the effective state: forced || setting
	mu      sync.Mutex  // serialises state changes
	path    string
	forced  bool // CORAL_DEBUG_INPUT / --debug-input
	setting bool // the debug_input_log setting
	w       atomic.Pointer[fileWriter]
	now     = time.Now
)

// Setup sets where the log goes (<dataDir>/input-debug.log) and whether the
// environment or a flag forces it on. It stays off unless forced or the setting
// is on (SetSetting).
func Setup(dataDir string, force bool) {
	mu.Lock()
	defer mu.Unlock()
	if dataDir != "" {
		path = filepath.Join(dataDir, FileName)
	}
	forced = force
	applyLocked("startup")
}

// SetSetting applies the debug_input_log setting; it takes effect at once.
func SetSetting(on bool) {
	mu.Lock()
	defer mu.Unlock()
	setting = on
	applyLocked("setting")
}

// Status is the log's state, for the Settings page.
type Status struct {
	Enabled bool   `json:"enabled"`
	Forced  bool   `json:"forced"` // on through CORAL_DEBUG_INPUT / --debug-input
	Path    string `json:"path"`
	// FileExists says the file (or its rotated copy) is on disk: turning the log off
	// keeps it, so Settings offers to delete it.
	FileExists bool  `json:"file_exists"`
	FileBytes  int64 `json:"file_bytes"`
}

// ErrOn refuses Delete while the log is on.
var ErrOn = errors.New("the terminal input debug log is on: turn it off before deleting it")

// State reports whether the log is on, whether the environment forces it, and
// where it writes.
func State() Status {
	mu.Lock()
	defer mu.Unlock()
	st := Status{Enabled: enabled.Load(), Forced: forced, Path: path}
	if path == "" {
		return st
	}
	for _, f := range []string{path, path + ".1"} {
		if fi, err := os.Lstat(f); err == nil && fi.Mode().IsRegular() {
			st.FileExists = true
			st.FileBytes += fi.Size()
		}
	}
	return st
}

// Delete removes the log file and its rotated copy. It refuses while the log is
// on (ErrOn). It reports how many files it removed.
func Delete() (int, error) {
	mu.Lock()
	defer mu.Unlock()
	if enabled.Load() {
		return 0, ErrOn
	}
	if path == "" {
		return 0, nil
	}
	if fw := w.Load(); fw != nil {
		fw.closeFile()
	}
	n := 0
	for _, f := range []string{path, path + ".1"} {
		if fi, err := os.Lstat(f); err != nil || !fi.Mode().IsRegular() {
			continue // never follow or remove a link someone put there
		}
		if err := os.Remove(f); err != nil {
			return n, err
		}
		n++
	}
	if n > 0 {
		log.Printf("[input] terminal input debug log deleted (%s)", path)
	}
	return n, nil
}

func applyLocked(why string) {
	on := (forced || setting) && path != ""
	was := enabled.Load()
	// A writer for another path, or one for a log turned off, is stopped (what it queued is written
	// first): it was left running with its file open, one goroutine per data folder or toggle (#4167).
	if fw := w.Load(); fw != nil && (!on || fw.path != path) {
		w.Store(nil)
		fw.stop()
	}
	if on && w.Load() == nil {
		w.Store(newFileWriter(path, maxFileBytes, queueDepth))
	}
	enabled.Store(on)
	switch {
	case on && !was:
		via := "the Log terminal input setting"
		if forced {
			via = "CORAL_DEBUG_INPUT / --debug-input"
		}
		log.Printf("[input] terminal input debug log is ON (%s): every write to an agent terminal, including typed text that may contain secrets, is recorded in %s. Turn it off when you are done.", via, path)
	case !on && was:
		log.Printf("[input] terminal input debug log is OFF (%s). %s is kept: delete it (Settings, or by hand) when you no longer need it.", why, path)
	}
}

// Flush waits until every line queued so far is in the debug file.
func Flush() {
	if fw := w.Load(); fw != nil {
		fw.flush()
	}
}

// EnabledFromEnv reports whether CORAL_DEBUG_INPUT asks for the debug file.
func EnabledFromEnv() bool {
	return config.Truthy(os.Getenv("CORAL_DEBUG_INPUT"))
}

// Record logs one write to the terminal target (a tmux target or a PTY session
// name) when the debug log is on. For Keys, data is the tmux key names separated
// by spaces.
func Record(target string, kind Kind, data string) {
	if !enabled.Load() {
		return
	}
	fw := w.Load()
	if fw == nil {
		return
	}
	fw.add(fmt.Sprintf("%s target=%s source=%s kind=%s %s\n",
		now().Format("2006-01-02T15:04:05.000Z07:00"), target, Source(), kind, describe(kind, data)))
}

// RecordHexKeys logs a tmux send-keys -H call (one hex byte per argument).
func RecordHexKeys(target string, hexBytes []string) {
	b := make([]byte, 0, len(hexBytes))
	for _, h := range hexBytes {
		if v, err := strconv.ParseUint(h, 16, 8); err == nil {
			b = append(b, byte(v))
		}
	}
	Record(target, Raw, string(b))
}

func describe(kind Kind, data string) string {
	if kind == Keys {
		return "keys=" + strconv.Quote(data)
	}
	s := data
	cut := ""
	if len(s) > maxText {
		s, cut = s[:maxText], fmt.Sprintf(" (+%d bytes)", len(data)-maxText)
	}
	return fmt.Sprintf("len=%d text=%s%s", len(data), strconv.Quote(s), cut)
}

// Source names the code path that sent the input: the first caller outside the
// terminal plumbing (this package, internal/tmux and internal/ptymanager), such as
// "background.(*BoardNotifier).deliver" or "routes.(*SessionsHandler).Send".
func Source() string {
	pcs := make([]uintptr, 32)
	n := runtime.Callers(2, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	for {
		f, more := frames.Next()
		if f.Function != "" && !plumbing(f.Function) {
			return shortFunc(f.Function)
		}
		if !more {
			return "unknown"
		}
	}
}

func plumbing(fn string) bool {
	for _, p := range []string{"/internal/inputlog.", "/internal/tmux.", "/internal/ptymanager.", "runtime."} {
		if strings.Contains(fn, p) && !strings.Contains(fn, ".Test") {
			return true
		}
	}
	return strings.HasPrefix(fn, "runtime.")
}

// shortFunc trims the module path: ".../internal/background.(*X).f" -> "background.(*X).f".
func shortFunc(fn string) string {
	if i := strings.LastIndex(fn, "/"); i >= 0 {
		return fn[i+1:]
	}
	return fn
}

// fileWriter appends lines to a capped file from one goroutine.
type fileWriter struct {
	path    string
	max     int64
	ch      chan string
	dropped atomic.Int64
	mu      sync.Mutex // guards f/size for Flush in tests
	f       *os.File
	size    int64
	done    chan chan struct{}
	quit    chan chan struct{}
	stopped chan struct{} // closed when the goroutine has ended (stop)
}

func newFileWriter(path string, max int64, depth int) *fileWriter {
	fw := &fileWriter{path: path, max: max, ch: make(chan string, depth), done: make(chan chan struct{}), quit: make(chan chan struct{}), stopped: make(chan struct{})}
	safego.Restart("inputlog/inputlog:fw.loop", func() { fw.loop() })
	return fw
}

func (fw *fileWriter) add(line string) {
	select {
	case fw.ch <- line:
	default:
		fw.dropped.Add(1)
	}
}

func (fw *fileWriter) loop() {
	for {
		select {
		case line := <-fw.ch:
			if n := fw.dropped.Swap(0); n > 0 {
				fw.write(fmt.Sprintf("%s [input] %d line(s) dropped: the log could not keep up\n", now().Format(time.RFC3339), n))
			}
			fw.write(line)
		case ack := <-fw.done:
		drain:
			for {
				select {
				case line := <-fw.ch:
					fw.write(line)
				default:
					break drain
				}
			}
			close(ack)
		case ack := <-fw.quit:
			for {
				select {
				case line := <-fw.ch:
					fw.write(line)
					continue
				default:
				}
				break
			}
			fw.closeFile()
			close(fw.stopped)
			close(ack)
			return
		}
	}
}

// stop writes what is queued, closes the file and ends the writer's goroutine.
func (fw *fileWriter) stop() {
	ack := make(chan struct{})
	fw.quit <- ack
	<-ack
}

func (fw *fileWriter) write(line string) {
	fw.mu.Lock()
	defer fw.mu.Unlock()
	if fw.f == nil || fw.size+int64(len(line)) > fw.max {
		if fw.f != nil {
			fw.f.Close()
			os.Rename(fw.path, fw.path+".1")
		}
		// No following a symlink planted at the path, and 0600 even if the file was
		// created with other permissions: it holds typed text.
		f, err := secfile.OpenNoFollow(fw.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			fw.f = nil
			return
		}
		if err := f.Chmod(0o600); err != nil {
			f.Close()
			fw.f = nil
			return
		}
		fw.f = f
		fw.size = 0
		if st, err := f.Stat(); err == nil {
			fw.size = st.Size()
		}
	}
	n, _ := fw.f.WriteString(line)
	fw.size += int64(n)
}

// closeFile closes the open file, so it can be deleted; the next write reopens it.
func (fw *fileWriter) closeFile() {
	fw.mu.Lock()
	defer fw.mu.Unlock()
	if fw.f != nil {
		fw.f.Close()
		fw.f = nil
	}
}

// flush waits until every line queued before it is written.
func (fw *fileWriter) flush() {
	ack := make(chan struct{})
	select {
	case fw.done <- ack:
		<-ack
	case <-fw.stopped: // stopped meanwhile: everything it had is written
	}
}
