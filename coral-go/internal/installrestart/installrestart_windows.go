//go:build windows

// Package installrestart remembers, across an MSI install, that Coral was running when the installer closed it (#335), so the installer
// starts it again afterwards, and only then. It is one small file in this user's %LOCALAPPDATA%\Coral, written when Coral is closed for the
// install and taken (read and removed) after it; a mark older than MaxAge is for no install running now and starts nothing.
package installrestart

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

// MaxAge is the longest an install may take between closing Coral and starting it again.
const MaxAge = 30 * time.Minute

const fileName = "restart-after-install"

// folder is this user's %LOCALAPPDATA%\Coral: Windows' known folder for the user, not the LOCALAPPDATA variable (which an installer's process
// may not carry over); Windows may still expand %USERPROFILE% in it. A variable so a test can put it elsewhere.
var folder = func() (string, error) {
	local, err := windows.KnownFolderPath(windows.FOLDERID_LocalAppData, windows.KF_FLAG_DEFAULT)
	if err != nil {
		return "", err
	}
	return filepath.Join(local, "Coral"), nil
}

// Mark records that Coral was closed for an install, now. The installer's helper may write it with an elevated token, so no link is followed:
// the folder must be a plain folder (not a link or a junction), and the file is created new after removing whatever has its name (a link is
// removed as itself); if something takes the name again in between, the create fails rather than write through it (Security on #335).
func Mark(now time.Time) error {
	dir, err := folder()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if fi, err := os.Lstat(dir); err != nil {
		return err
	} else if !fi.IsDir() {
		return errors.New("the mark's folder is not a plain folder")
	}
	p := filepath.Join(dir, fileName)
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.WriteString(now.UTC().Format(time.RFC3339) + "\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// Take removes the mark and reports whether it was there and recent (written within MaxAge before now, never after it). A missing,
// unreadable or stale mark is false; the file goes in every case, so it starts Coral once at most.
func Take(now time.Time) bool {
	dir, err := folder()
	if err != nil {
		return false
	}
	p := filepath.Join(dir, fileName)
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	_ = os.Remove(p)
	if err != nil {
		return false
	}
	at, err := time.Parse(time.RFC3339, strings.TrimSpace(string(b)))
	if err != nil {
		return false
	}
	age := now.Sub(at)
	return age >= -time.Minute && age <= MaxAge
}
