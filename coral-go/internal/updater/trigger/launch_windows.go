//go:build windows

package trigger

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"

	"github.com/subgentic/coral/internal/safefs"
	"github.com/subgentic/coral/internal/secfile"
)

// The updater's own program, and the folder its copy runs from: <data>\updates\apply, private to this user (secfile.ProtectDir).
const (
	updaterExe      = "coral-update.exe"
	updaterApplyDir = "apply"
)

// updaterInstallDir is the folder the running server's program is in (symbolic links resolved): coral-update.exe is expected beside it. A variable for a test.
var updaterInstallDir = func() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	real, err := safefs.Resolve(exe)
	if err != nil {
		return "", err
	}
	return filepath.Dir(real), nil
}

// Launch starts "coral-update apply" detached from this server, from a COPY of the installed coral-update.exe in <data>\updates\apply: the
// install folder is what the MSI replaces, and a program running from it would be held open (or replaced under itself). The copy is hashed against
// the installed program before it is started, from the copy's own bytes, and it is a plain file this user made (never a link). The updater takes
// nothing from here: its argument is the fixed word "apply", and no handle, pipe or environment of the server's is passed on that it did not
// already have in its environment (no handle at all: bInheritHandles is false).
func Launch(ctx context.Context, dataDir, version string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	installDir, err := updaterInstallDir()
	if err != nil {
		return fmt.Errorf("where Coral is installed: %w", err)
	}
	dir := filepath.Join(dataDir, UpdatesDir, updaterApplyDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("the updater's folder: %w", err)
	}
	if err := secfile.ProtectDir(dir); err != nil {
		return fmt.Errorf("making the updater's folder private: %w", err)
	}
	removeOldCopies(dir)
	copyPath, hold, err := copyUpdater(filepath.Join(installDir, updaterExe), dir)
	if err != nil {
		return err
	}
	// The copy stays open (readable by others, not writable or deletable) from the hash check until the process has been created: no same-user process can swap
	// the file between the check and CreateProcess.
	defer hold.Close()
	// The marker goes down last, so a failure above leaves none behind; it is for this start only and is removed if the start fails.
	if err := WriteMarker(dataDir, version, time.Now()); err != nil {
		return fmt.Errorf("the install marker: %w", err)
	}
	if err := startDetached(copyPath, dir); err != nil {
		RemoveMarker(dataDir)
		return err
	}
	return nil
}

// copyUpdater copies the installed updater into dir and returns the copy's path and an open handle on it (FILE_SHARE_READ, see openShareRead); the caller closes it. The copy is written to a fresh name, flushed, closed, and hashed again
// from disk; it is used only if that hash is the installed program's.
func copyUpdater(src, dir string) (string, *os.File, error) {
	in, err := secfile.OpenNoFollow(src, os.O_RDONLY, 0)
	if err != nil {
		return "", nil, fmt.Errorf("the installed updater: %w", err)
	}
	defer in.Close()
	if fi, err := in.Stat(); err != nil || !fi.Mode().IsRegular() {
		return "", nil, errors.New("the installed updater is not a plain file")
	}
	out, err := os.CreateTemp(dir, "coral-update-*.exe")
	if err != nil {
		return "", nil, fmt.Errorf("the updater's copy: %w", err)
	}
	dst := out.Name()
	ok := false
	defer func() {
		if !ok {
			out.Close()
			os.Remove(dst)
		}
	}()
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(out, h), in); err != nil {
		return "", nil, fmt.Errorf("copying the updater: %w", err)
	}
	if err := out.Sync(); err != nil {
		return "", nil, err
	}
	if err := out.Close(); err != nil {
		return "", nil, err
	}
	back, err := openShareRead(dst)
	if err != nil {
		return "", nil, err
	}
	closeBack := true
	defer func() {
		if closeBack {
			back.Close()
		}
	}()
	h2 := sha256.New()
	if _, err := io.Copy(h2, back); err != nil {
		return "", nil, err
	}
	if fmt.Sprintf("%x", h.Sum(nil)) != fmt.Sprintf("%x", h2.Sum(nil)) {
		return "", nil, errors.New("the updater's copy does not match the installed updater")
	}
	ok, closeBack = true, false
	return dst, back, nil
}

// openShareRead opens path for reading with FILE_SHARE_READ only: while it is open nobody can open the file for writing, nor delete or rename it. (os.Open shares
// write and delete too.) Holding it is what keeps the hash check true until the process is created.
func openShareRead(path string) (*os.File, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}

// Process creation flags (the Go windows package names some of them, not CREATE_BREAKAWAY_FROM_JOB's pair for a service).
const (
	detachedProcess      = 0x00000008
	createNewProcessGrp  = 0x00000200
	createBreakawayJob   = 0x01000000
	updaterFlagsRequired = detachedProcess | createNewProcessGrp
)

// startDetached starts "<exe> apply" so that it outlives this server: no console, its own process group, and out of this server's job objects when
// the job allows it (CREATE_BREAKAWAY_FROM_JOB). A job that does not allow breakaway refuses the start with ACCESS_DENIED: the start is then made
// without the flag, and the log says so (the updater is then killed with that job, which Coral's own kill-on-close jobs never are: they hold agents only).
func startDetached(exe, dir string) error {
	start := func(flags uint32) (*exec.Cmd, error) {
		cmd := exec.Command(exe, "apply")
		cmd.Dir = dir
		cmd.Env = updaterEnv(os.Environ())
		// No standard handles (the null device), and no inherited handle of any kind: a handle of the server's (the database, a socket, the pipe to the tray) held
		// by the updater would stay open after the server quits.
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags, NoInheritHandles: true, HideWindow: true}
		return cmd, cmd.Start()
	}
	cmd, err := start(updaterFlagsRequired | createBreakawayJob)
	if err != nil && isAccessDenied(err) {
		slog.Warn("[update] this server's job object does not allow breakaway; starting the updater inside it", "error", err)
		cmd, err = start(updaterFlagsRequired)
	}
	if err != nil {
		return fmt.Errorf("starting the updater: %w", err)
	}
	return cmd.Process.Release()
}

func isAccessDenied(err error) bool {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno == syscall.Errno(windows.ERROR_ACCESS_DENIED)
	}
	return strings.Contains(strings.ToLower(err.Error()), "access is denied")
}

// removeOldCopies deletes the updater copies earlier installs left in dir. A copy that is still running cannot be deleted (Windows holds it); that one is
// left and the next install tries again.
func removeOldCopies(dir string) {
	old, _ := filepath.Glob(filepath.Join(dir, "coral-update-*.exe"))
	for _, p := range old {
		_ = os.Remove(p)
	}
}

// updaterEnvNames are the variables the updater is started with, besides CORAL_DATA_DIR and CORAL_AUTO_UPDATE (which decide where its data folder is and whether it
// runs): what Windows programs and a folder lookup need. Nothing else of the server's environment (its session values, tokens, ports, settings) reaches it. Compared
// without case, as Windows does.
var updaterEnvNames = map[string]bool{
	"SYSTEMROOT": true, "SYSTEMDRIVE": true, "WINDIR": true, "COMSPEC": true, "PATHEXT": true, "PATH": true,
	"TEMP": true, "TMP": true, "USERPROFILE": true, "HOMEDRIVE": true, "HOMEPATH": true, "APPDATA": true, "LOCALAPPDATA": true,
	"PROGRAMFILES": true, "PROGRAMFILES(X86)": true, "PROGRAMW6432": true, "PROGRAMDATA": true, "COMMONPROGRAMFILES": true,
	"USERNAME": true, "USERDOMAIN": true, "COMPUTERNAME": true, "PROCESSOR_ARCHITECTURE": true, "OS": true, "NUMBER_OF_PROCESSORS": true,
	"CORAL_DATA_DIR": true, "CORAL_AUTO_UPDATE": true,
}

// updaterEnvExtra is for a test that starts a stand-in updater and needs its own variables to reach it; nil in Coral.
var updaterEnvExtra map[string]bool

// updaterEnv is environ cut down to updaterEnvNames (and updaterEnvExtra).
func updaterEnv(environ []string) []string {
	var out []string
	for _, kv := range environ {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || name == "" { // the "=C:=C:\\x" per-drive entries have an empty name here
			continue
		}
		up := strings.ToUpper(name)
		if updaterEnvNames[up] || updaterEnvExtra[up] {
			out = append(out, kv)
		}
	}
	return out
}
