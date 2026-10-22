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
	copyPath, err := copyUpdater(filepath.Join(installDir, updaterExe), dir)
	if err != nil {
		return err
	}
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

// copyUpdater copies the installed updater into dir and returns the copy's path. The copy is written to a fresh name, flushed, closed, and hashed again
// from disk; it is used only if that hash is the installed program's.
func copyUpdater(src, dir string) (string, error) {
	in, err := secfile.OpenNoFollow(src, os.O_RDONLY, 0)
	if err != nil {
		return "", fmt.Errorf("the installed updater: %w", err)
	}
	defer in.Close()
	if fi, err := in.Stat(); err != nil || !fi.Mode().IsRegular() {
		return "", errors.New("the installed updater is not a plain file")
	}
	out, err := os.CreateTemp(dir, "coral-update-*.exe")
	if err != nil {
		return "", fmt.Errorf("the updater's copy: %w", err)
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
		return "", fmt.Errorf("copying the updater: %w", err)
	}
	if err := out.Sync(); err != nil {
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	back, err := os.Open(dst)
	if err != nil {
		return "", err
	}
	defer back.Close()
	h2 := sha256.New()
	if _, err := io.Copy(h2, back); err != nil {
		return "", err
	}
	if fmt.Sprintf("%x", h.Sum(nil)) != fmt.Sprintf("%x", h2.Sum(nil)) {
		return "", errors.New("the updater's copy does not match the installed updater")
	}
	ok = true
	return dst, nil
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
		cmd.Env = os.Environ()
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
