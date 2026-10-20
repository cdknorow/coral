package apply

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cdknorow/coral/internal/updater/runrecord"
	"github.com/cdknorow/coral/internal/updater/verify"
)

// The Linux part of the apply (#5231 section 6): a tarball install, a folder that holds only Coral's own programs. It is replaced by building the
// new folder beside it and exchanging the two in one step (renameat2 RENAME_EXCHANGE); the old one is kept for the rollback. Anything this program
// does not own (a package manager's files, a folder shared with other tools, one the user cannot write) is refused, and says why.
//
// A service manager that restarts Coral by itself (systemd with Restart=always) can end the updater together with Coral (the same control group).
// The apply is then finished by the next one: it finds the new install running (recoverInterrupted) and keeps it.

// linuxPrograms is the set of programs a Linux package ships: tests/release/commands.txt plus coral-update (a test keeps them equal).
var linuxPrograms = []string{
	"coral", "launch-coral", "coral-board", "coral-agent",
	"coral-hook-agentic-state", "coral-hook-message-check", "coral-hook-session-start", "coral-hook-task-sync",
	"coral-update",
}

// linuxNotices are the other files a package may hold.
var linuxNotices = []string{"THIRD_PARTY_NOTICES.md", "LICENSE", "LICENSE.txt", "NOTICE"}

func isProgram(name string) bool {
	for _, p := range linuxPrograms {
		if p == name {
			return true
		}
	}
	return false
}

func allowedName(name string) bool {
	if isProgram(name) {
		return true
	}
	for _, n := range linuxNotices {
		if n == name {
			return true
		}
	}
	return false
}

// Linux is the Linux Platform.
type Linux struct {
	DataDir string
	// Exe is the running program's path; Parent is the pid that started it; Self is its own pid.
	Exe    string
	Parent int
	Self   int
	// Arch is the machine's architecture (runtime.GOARCH).
	Arch string
	// Exec runs a program with exactly the given environment (a test replaces it); Spawn starts Coral detached.
	Exec  func(ctx context.Context, env []string, name string, args ...string) (string, error)
	Spawn func(argv, env []string, cwd string) error
	// Manager reports that a service manager restarts Coral itself (systemd: INVOCATION_ID is set).
	Manager func() bool
	// ProcDir is /proc; Uid is the user's id (tests replace them).
	ProcDir string
	Uid     int
	// SwapSupported probes the volume for the atomic exchange.
	SwapSupported func(parent string) bool
	// ManagedPrefixes are the folders a package manager owns (default /usr, /opt, /snap, /nix, /home/linuxbrew and $HOMEBREW_PREFIX).
	ManagedPrefixes []string
	WaitFor         time.Duration
	HealthFor       time.Duration
	Sleep           func(time.Duration)

	mu    sync.Mutex
	built map[string]map[string]string // the new folder -> the sha256 of each file as it was unpacked
}

var _ Platform = (*Linux)(nil)

func (l *Linux) sleep(d time.Duration) {
	if l.Sleep != nil {
		l.Sleep(d)
		return
	}
	time.Sleep(d)
}

func (l *Linux) Pinned() bool { return true } // the sha256 of the verified sums file is the trust: there is no platform signature to pin

func (l *Linux) proc() string {
	if l.ProcDir != "" {
		return l.ProcDir
	}
	return "/proc"
}

func (l *Linux) uid() int {
	if l.Uid != 0 {
		return l.Uid
	}
	return os.Getuid()
}

// Install is the folder this program runs from, when it is one of the package's programs.
func (l *Linux) Install() (string, error) {
	exe := l.Exe
	if exe == "" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return "", err
		}
	}
	real, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", err
	}
	if filepath.Base(real) != "coral-update" {
		return "", errors.New("this program is not coral-update")
	}
	return filepath.Dir(real), nil
}

func (l *Linux) managed() []string {
	if l.ManagedPrefixes != nil {
		return l.ManagedPrefixes
	}
	p := []string{"/usr", "/opt", "/snap", "/nix", "/home/linuxbrew"}
	if h := os.Getenv("HOMEBREW_PREFIX"); h != "" {
		p = append(p, h)
	}
	return p
}

func under(path, prefix string) bool {
	prefix = filepath.Clean(prefix)
	return path == prefix || strings.HasPrefix(path, prefix+string(filepath.Separator))
}

// Unsupported refuses what this program does not own, with the reason: a package manager's folder, a folder another user owns (a root-owned
// /usr/local/bin extraction), a folder shared with other tools (anything in it that is not Coral's own), a symlinked install (a follow-up),
// and a volume without the atomic exchange.
func (l *Linux) Unsupported(install string) string {
	for _, p := range l.managed() {
		if under(install, p) {
			return "package_manager"
		}
	}
	fi, err := os.Stat(install)
	if err != nil {
		return "unreadable_install"
	}
	if owner, ok := ownerOf(fi); ok && owner != l.uid() {
		return "not_owned"
	}
	entries, err := os.ReadDir(install)
	if err != nil {
		return "unreadable_install"
	}
	hasCoral := false
	for _, e := range entries {
		if e.Name() == "coral" {
			hasCoral = true
		}
		if e.Name() == licensesDir && e.IsDir() {
			if _, err := plainTree(filepath.Join(install, licensesDir)); err != nil {
				return "shared_folder"
			}
			continue
		}
		if !e.Type().IsRegular() || !allowedName(e.Name()) {
			return "shared_folder"
		}
	}
	if !hasCoral {
		return "shared_folder"
	}
	realInstall, rerr := filepath.EvalSymlinks(install)
	if rerr != nil {
		return "unreadable_install"
	}
	// A link in the parent that leads to this folder is a versioned install behind a "current" link: that shape swaps a link, not the folder.
	if links, _ := os.ReadDir(filepath.Dir(install)); links != nil {
		for _, e := range links {
			if e.Type()&os.ModeSymlink != 0 {
				if t, err := filepath.EvalSymlinks(filepath.Join(filepath.Dir(install), e.Name())); err == nil && t == realInstall {
					return "symlinked_install"
				}
			}
		}
	}
	probe := l.SwapSupported
	if probe == nil {
		probe = swapSupported
	}
	if !probe(filepath.Dir(install)) {
		return "no_atomic_swap"
	}
	return ""
}

// Writable: the user can create files next to the install and in it.
func (l *Linux) Writable(install string) bool {
	for _, d := range []string{filepath.Dir(install), install} {
		f, err := os.CreateTemp(d, ".coral-update-probe-*")
		if err != nil {
			return false
		}
		name := f.Name()
		f.Close()
		os.Remove(name)
	}
	return true
}

func linuxNewName(install string) string {
	return filepath.Join(filepath.Dir(install), "."+filepath.Base(install)+".new")
}

// Build extracts the package, strictly, into a hidden sibling folder of the install (the same volume).
func (l *Linux) Build(ctx context.Context, pkg, install, work string) (string, func(), error) {
	dst := linuxNewName(install)
	if err := os.RemoveAll(dst); err != nil {
		return "", nil, err
	}
	if err := os.Mkdir(dst, 0o755); err != nil {
		return "", nil, err
	}
	_, hashes, err := extractTarGz(pkg, dst, allowedName, DefaultExtractLimits)
	if err != nil {
		_ = os.RemoveAll(dst)
		return "", nil, fmt.Errorf("unpacking the package: %w", err)
	}
	l.mu.Lock()
	l.built = map[string]map[string]string{dst: hashes}
	l.mu.Unlock()
	return dst, func() {}, nil
}

func (l *Linux) machine() elf.Machine {
	arch := l.Arch
	if arch == "" {
		arch = "amd64"
	}
	switch arch {
	case "arm64":
		return elf.EM_AARCH64
	default:
		return elf.EM_X86_64
	}
}

func (l *Linux) exec(ctx context.Context, env []string, name string, args ...string) (string, error) {
	if l.Exec != nil {
		return l.Exec(ctx, env, name, args...)
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, name, args...)
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

// isolatedEnv is the environment of the new program's version question: a folder of its own, nothing of the user's.
func isolatedEnv(dir string) []string {
	return []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "CORAL_DATA_DIR=" + filepath.Join(dir, "d"), "CORAL_AUTO_UPDATE=off", "LANG=C"}
}

// Verify checks the new folder where it is: every program is there, is a regular, executable, not set-id ELF of this machine, no file is a link
// or anything else, and the new coral says it is the approved version (asked with an environment of its own: --version is answered before
// anything else runs).
func (l *Linux) Verify(ctx context.Context, newPath, version string) error {
	entries, err := os.ReadDir(newPath)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, e := range entries {
		fi, err := os.Lstat(filepath.Join(newPath, e.Name()))
		if err != nil {
			return err
		}
		if e.Name() == licensesDir && fi.IsDir() {
			files, err := plainTree(filepath.Join(newPath, licensesDir))
			if err != nil {
				return err
			}
			for _, f := range files {
				have[licensesDir+"/"+f] = true
			}
			continue
		}
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("%s is not a plain file", e.Name())
		}
		if fi.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			return fmt.Errorf("%s is set-id", e.Name())
		}
		have[e.Name()] = true
	}
	for _, p := range linuxPrograms {
		if !have[p] {
			return fmt.Errorf("the package has no %s", p)
		}
		path := filepath.Join(newPath, p)
		fi, _ := os.Stat(path)
		if fi.Mode()&0o111 == 0 {
			return fmt.Errorf("%s is not executable", p)
		}
		ef, err := elf.Open(path)
		if err != nil {
			return fmt.Errorf("%s is not an ELF program: %w", p, err)
		}
		m, class := ef.Machine, ef.Class
		ef.Close()
		if m != l.machine() || class != elf.ELFCLASS64 {
			return fmt.Errorf("%s is for another machine (%v)", p, m)
		}
	}
	if err := l.sameAsUnpacked(newPath, have); err != nil {
		return err
	}
	got, err := l.versionOf(ctx, newPath)
	if err != nil {
		return err
	}
	if got != version {
		return fmt.Errorf("the new coral is version %q, the approved one is %q", got, version)
	}
	return nil
}

// sameAsUnpacked compares the folder with what Build unpacked: the same files with the same hashes. The folder sits beside the install until the
// swap, so the check just before the swap catches a file that was changed in between (a valid ELF of another content passes every other check).
func (l *Linux) sameAsUnpacked(newPath string, have map[string]bool) error {
	l.mu.Lock()
	want := l.built[newPath]
	l.mu.Unlock()
	if want == nil {
		return nil // not built by this run (a test of the other checks)
	}
	if len(want) != len(have) {
		return errors.New("the files of the new folder are not the ones unpacked")
	}
	for name, sum := range want {
		if !have[name] {
			return fmt.Errorf("%s is missing from the new folder", name)
		}
		f, err := os.Open(filepath.Join(newPath, filepath.FromSlash(name)))
		if err != nil {
			return err
		}
		h := sha256.New()
		_, cerr := io.Copy(h, f)
		f.Close()
		if cerr != nil {
			return cerr
		}
		if hex.EncodeToString(h.Sum(nil)) != sum {
			return fmt.Errorf("%s changed after it was unpacked", name)
		}
	}
	return nil
}

func (l *Linux) versionOf(ctx context.Context, path string) (string, error) {
	tmp, err := os.MkdirTemp("", "coral-update-version-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	out, err := l.exec(ctx, isolatedEnv(tmp), filepath.Join(path, "coral"), "--version")
	if err != nil {
		return "", fmt.Errorf("coral --version: %w", err)
	}
	f := strings.Fields(out)
	if len(f) != 2 || f[0] != "coral" {
		return "", fmt.Errorf("coral --version answered %q", strings.TrimSpace(out))
	}
	return f[1], nil
}

// Version is the version the folder's coral reports.
func (l *Linux) Version(path string) (string, error) { return l.versionOf(context.Background(), path) }

// Swap exchanges two folders with renameat2(RENAME_EXCHANGE) (swap_linux.go).
func (l *Linux) Swap(a, b string) error { return swapPaths(a, b) }

func (l *Linux) managerRestarts() bool {
	if l.Manager != nil {
		return l.Manager()
	}
	return os.Getenv("INVOCATION_ID") != ""
}

// runRecord reads what the server recorded.
func (l *Linux) runRecord() (runrecord.Record, error) {
	b, err := verify.ReadPrivate(filepath.Join(l.DataDir, "updates", runrecord.File))
	if err != nil {
		return runrecord.Record{}, err
	}
	var rr runrecord.Record
	if err := json.Unmarshal(b, &rr); err != nil {
		return runrecord.Record{}, err
	}
	return rr, nil
}

// Start starts Coral again as the server recorded it, detached, when nothing else will: a service manager restarts it itself and a second copy
// would fight it. argv[0] must be a program of the install (never a path from anywhere else).
func (l *Linux) Start(ctx context.Context, install string) error {
	if l.managerRestarts() {
		return nil
	}
	rr, err := l.runRecord()
	if err != nil {
		return fmt.Errorf("no record of how Coral was started: %w", err)
	}
	if err := runrecord.Validate(rr, install); err != nil {
		return err
	}
	spawn := l.Spawn
	if spawn == nil {
		spawn = spawnDetached
	}
	// The environment the restarted Coral gets is the record's allowlisted part, filtered again here (the file is read, not trusted: a name the allowlist does not
	// carry, an LD_PRELOAD or a token, is not passed on), and never the updater's own, which holds what the server gave it (#5414).
	return spawn(rr.Argv, runrecord.AllowedEnv(rr.Env), rr.Cwd)
}

// CanRestart says why Coral could not be started again after the swap ("" when it can): nothing is started by this program under a service manager, and
// otherwise the run record must be one Start will accept. It is asked before anything is built or ended, so an update that could end Coral and not bring it back
// does not begin (#5419).
func (l *Linux) CanRestart(install string) string {
	if l.managerRestarts() {
		return ""
	}
	rr, err := l.runRecord()
	if err != nil || runrecord.Validate(rr, install) != nil {
		return "no_run_record"
	}
	return ""
}

// running counts the processes whose executable is a file of the install.
func (l *Linux) running(install string) int {
	entries, err := os.ReadDir(l.proc())
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == l.Self || pid == os.Getpid() {
			continue
		}
		exe, err := os.Readlink(filepath.Join(l.proc(), e.Name(), "exe"))
		if err != nil {
			continue
		}
		if under(exe, install) {
			n++
		}
	}
	return n
}

func (l *Linux) Running(install string) bool { return l.running(install) > 0 }

// Healthy: a process of the install is running, and still is a few seconds later.
func (l *Linux) Healthy(ctx context.Context, install string) error {
	deadline := l.HealthFor
	if deadline == 0 {
		deadline = 60 * time.Second
	}
	end := time.Now().Add(deadline)
	seen := 0
	for time.Now().Before(end) {
		if l.running(install) > 0 {
			seen++
			if seen >= 3 {
				return nil
			}
		} else {
			seen = 0
		}
		l.sleep(2 * time.Second)
	}
	return errors.New("the new install did not stay up")
}

// Show: there is nothing to open on a server; an install this user cannot write is told so and left.
func (l *Linux) Show(ctx context.Context, pkg string) error {
	return errors.New("nothing to show on this platform")
}

// WaitCoralGone: the process that started this one is gone, and no process of the install is running.
func (l *Linux) WaitCoralGone(ctx context.Context, install string) bool {
	wait := l.WaitFor
	if wait == 0 {
		wait = 2 * time.Minute
	}
	end := time.Now().Add(wait)
	for {
		if (l.Parent <= 1 || !pidAlive(l.Parent)) && l.running(install) == 0 {
			return true
		}
		if time.Now().After(end) || ctx.Err() != nil {
			return false
		}
		l.sleep(time.Second)
	}
}

// plainTree walks a folder and returns the relative paths ("go/x/LICENSE") of its files, or an error when anything in it is not a plain folder or a
// plain, not set-id regular file: a link, a device, a FIFO, a socket.
func plainTree(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		switch {
		case info.IsDir():
			return nil
		case info.Mode().IsRegular():
			if info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
				return fmt.Errorf("%s is set-id", rel)
			}
			files = append(files, filepath.ToSlash(rel))
			return nil
		}
		return fmt.Errorf("%s is not a plain file or folder", rel)
	})
	return files, err
}
