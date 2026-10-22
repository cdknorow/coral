//go:build windows

package apply

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/subgentic/coral/internal/windowmark"
)

// The Windows MSI apply's system side (#162, apply_msi.go): Windows Installer's registration of the Coral product, the processes of its
// folder, the held package file, msiexec and the restart.

// CoralUpgradeCode is tools/coral.wxs's UpgradeCode: the one Coral product, whatever its version.
const CoralUpgradeCode = "{B8F2E3A1-7C4D-4E5F-9A1B-2C3D4E5F6A7B}"

// coralExeComponent is tools/coral.wxs's CoralExe component: its key path is coral.exe in the install folder.
const coralExeComponent = "{A1B2C3D4-E5F6-7890-ABCD-EF1234567890}"

var (
	modMsi                     = windows.NewLazySystemDLL("msi.dll")
	procMsiEnumRelatedProducts = modMsi.NewProc("MsiEnumRelatedProductsW")
	procMsiGetComponentPathEx  = modMsi.NewProc("MsiGetComponentPathExW")
	procMsiGetProductInfoEx    = modMsi.NewProc("MsiGetProductInfoExW")
	procMsiOpenPackageEx       = modMsi.NewProc("MsiOpenPackageExW")
	procMsiGetProductProperty  = modMsi.NewProc("MsiGetProductPropertyW")
	procMsiCloseHandle         = modMsi.NewProc("MsiCloseHandle")
)

const (
	msiOpenPackageIgnoreMachineState = 1
	msiInstallContextMachine         = 4   // MSIINSTALLCONTEXT_MACHINE
	errorNoMoreItems                 = 259 // ERROR_NO_MORE_ITEMS
)

// WindowsMSI is the real MSI.
type WindowsMSI struct {
	DataDir   string
	WaitFor   time.Duration
	HealthFor time.Duration
	// MessageWait bounds how long a message waits for the person before the apply goes on (the box stays up until the process ends).
	MessageWait time.Duration
}

var _ MSI = (*WindowsMSI)(nil)

// relatedProducts lists every product registered with CoralUpgradeCode that this user can see, per machine and per user alike; a test
// replaces it.
var relatedProducts = func() ([]string, error) {
	upgrade, err := windows.UTF16PtrFromString(CoralUpgradeCode)
	if err != nil {
		return nil, err
	}
	var codes []string
	for i := uintptr(0); ; i++ {
		buf := make([]uint16, 39)
		r, _, _ := procMsiEnumRelatedProducts.Call(uintptr(unsafe.Pointer(upgrade)), 0, i, uintptr(unsafe.Pointer(&buf[0])))
		if r == errorNoMoreItems {
			return codes, nil
		}
		if r != 0 {
			return nil, fmt.Errorf("MsiEnumRelatedProducts %d", r)
		}
		codes = append(codes, windows.UTF16ToString(buf))
	}
}

// machineProperty is a product's property in the PER-MACHINE install context, and whether the product is installed there; a test replaces it.
var machineProperty = func(code, property string) (string, bool) {
	p, err1 := windows.UTF16PtrFromString(code)
	prop, err2 := windows.UTF16PtrFromString(property)
	if err1 != nil || err2 != nil {
		return "", false
	}
	size := uint32(256)
	buf := make([]uint16, size)
	r, _, _ := procMsiGetProductInfoEx.Call(uintptr(unsafe.Pointer(p)), 0, msiInstallContextMachine, uintptr(unsafe.Pointer(prop)), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if r != 0 {
		return "", false
	}
	return windows.UTF16ToString(buf), true
}

// RegisteredProduct is the ProductCode of the Coral product registered PER MACHINE for CoralUpgradeCode. The UpgradeCode is public, so a
// same-user process could install a per-user product with it; such a product is never the one updated or removed (N1). None, or more than one
// per machine, is an error (fail closed).
func (w *WindowsMSI) RegisteredProduct() (string, error) {
	codes, err := relatedProducts()
	if err != nil {
		return "", err
	}
	var machine []string
	for _, c := range codes {
		if _, ok := machineProperty(c, "VersionString"); ok {
			machine = append(machine, c)
		}
	}
	switch len(machine) {
	case 1:
		return machine[0], nil
	case 0:
		return "", errors.New("no Coral product is registered for this machine")
	}
	return "", fmt.Errorf("%d Coral products are registered for this machine", len(machine))
}

// RegisteredVersion is the registered per-machine product's version (its ProductVersion).
func (w *WindowsMSI) RegisteredVersion() (string, error) {
	code, err := w.RegisteredProduct()
	if err != nil {
		return "", err
	}
	v, ok := machineProperty(code, "VersionString")
	if !ok {
		return "", errors.New("the registered product has no version")
	}
	return v, nil
}

// Install is the folder of the registered product: where its CoralExe component (coral.exe) is installed.
func (w *WindowsMSI) Install() (string, error) {
	product, err := w.RegisteredProduct()
	if err != nil {
		return "", err
	}
	p, _ := windows.UTF16PtrFromString(product)
	c, _ := windows.UTF16PtrFromString(coralExeComponent)
	size := uint32(windows.MAX_LONG_PATH)
	buf := make([]uint16, size)
	// In the per-machine context only (N1), as RegisteredProduct.
	r, _, _ := procMsiGetComponentPathEx.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(c)), 0, msiInstallContextMachine, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	const installStateLocal = 3
	if int32(r) != installStateLocal {
		return "", fmt.Errorf("coral.exe is not installed locally (state %d)", int32(r))
	}
	return filepath.Dir(windows.UTF16ToString(buf)), nil
}

// Unsupported refuses an install that isn't under Program Files (a portable copy beside a registered product, or one moved).
func (w *WindowsMSI) Unsupported(install string) string {
	pf, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, 0)
	if err != nil {
		return "unreadable_install"
	}
	if !underFold(install, pf) {
		if pfx86, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFilesX86, 0); err != nil || !underFold(install, pfx86) {
			return "portable_install"
		}
	}
	return ""
}

func underFold(path, dir string) bool {
	path, dir = strings.ToLower(filepath.Clean(path)), strings.ToLower(filepath.Clean(dir))
	return path == dir || strings.HasPrefix(path, dir+`\`)
}

// running lists the processes whose image is a file under install (this process excluded).
func (w *WindowsMSI) running(install string) ([]string, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	var found []string
	self := uint32(os.Getpid())
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if e.ProcessID == 0 || e.ProcessID == self {
			continue
		}
		h, oerr := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, e.ProcessID)
		if oerr != nil {
			continue // a process this user can't open isn't one of this user's Corals
		}
		buf := make([]uint16, windows.MAX_LONG_PATH)
		size := uint32(len(buf))
		qerr := windows.QueryFullProcessImageName(h, 0, &buf[0], &size)
		windows.CloseHandle(h)
		if qerr == nil {
			if image := windows.UTF16ToString(buf[:size]); underFold(image, install) {
				found = append(found, filepath.Base(image))
			}
		}
	}
	return found, nil
}

// WaitCoralGone waits for every process of the install folder to end (coral, coral-tray, coral-app, an agent's hook), never stopping one.
func (w *WindowsMSI) WaitCoralGone(ctx context.Context, install string) bool {
	wait := w.WaitFor
	if wait == 0 {
		wait = 2 * time.Minute
	}
	end := time.Now().Add(wait)
	for {
		if procs, err := w.running(install); err == nil && len(procs) == 0 {
			return true
		}
		if time.Now().After(end) || ctx.Err() != nil {
			return false
		}
		time.Sleep(time.Second)
	}
}

// heldFile is a package file held open: read access, shared for reading only, so no process can write, delete or rename it until Close.
type heldFile struct {
	path string
	f    *os.File
}

func (h *heldFile) Path() string { return h.path }
func (h *heldFile) Close() error { return h.f.Close() }

// Hold opens path without following a reparse point at it or at any folder above it in the updates folder, shared for reading only, and
// checks its sha256 through that handle (D).
func (w *WindowsMSI) Hold(path, sum string) (Held, error) {
	clean := filepath.Clean(path)
	updates := filepath.Join(w.DataDir, "updates")
	for dir := filepath.Dir(clean); underFold(dir, updates); dir = filepath.Dir(dir) {
		if err := noReparse(dir); err != nil {
			return nil, err
		}
		if dir == filepath.Dir(dir) {
			break
		}
	}
	name, err := windows.UTF16PtrFromString(clean)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, fmt.Errorf("hold %s: %w", filepath.Base(clean), err)
	}
	f := os.NewFile(uintptr(h), clean)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		f.Close()
		return nil, err
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 || info.NumberOfLinks != 1 {
		f.Close()
		return nil, errors.New("the package is not a plain file with one name")
	}
	hsh := sha256.New()
	if _, err := io.Copy(hsh, f); err != nil {
		f.Close()
		return nil, err
	}
	if hex.EncodeToString(hsh.Sum(nil)) != sum {
		f.Close()
		return nil, errors.New("the package is not the one that was verified")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return nil, err
	}
	return &heldFile{path: clean, f: f}, nil
}

// noReparse refuses a folder that is a reparse point (a junction or a symbolic link).
func noReparse(dir string) error {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	attrs, err := windows.GetFileAttributes(p)
	if err != nil {
		return err
	}
	if attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("%s is a link", dir)
	}
	return nil
}

// Authenticode checks the held package's signature (authenticode_windows.go).
func (w *WindowsMSI) Authenticode(h Held) (bool, error) {
	hf, ok := h.(*heldFile)
	if !ok {
		return false, errors.New("not a held file")
	}
	return verifyAuthenticode(hf)
}

// ProductCode is the held package's ProductCode property (read by Windows Installer from the file, which the hold keeps unchanged).
func (w *WindowsMSI) ProductCode(h Held) (string, error) {
	p, err := windows.UTF16PtrFromString(h.Path())
	if err != nil {
		return "", err
	}
	var handle uint32
	if r, _, _ := procMsiOpenPackageEx.Call(uintptr(unsafe.Pointer(p)), msiOpenPackageIgnoreMachineState, uintptr(unsafe.Pointer(&handle))); r != 0 {
		return "", fmt.Errorf("MsiOpenPackageEx %d", r)
	}
	defer procMsiCloseHandle.Call(uintptr(handle))
	prop, _ := windows.UTF16PtrFromString("ProductCode")
	size := uint32(64)
	buf := make([]uint16, size)
	if r, _, _ := procMsiGetProductProperty.Call(uintptr(handle), uintptr(unsafe.Pointer(prop)), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size))); r != 0 {
		return "", fmt.Errorf("MsiGetProductProperty %d", r)
	}
	return windows.UTF16ToString(buf), nil
}

// msiexecPath is the system's msiexec, from the system folder Windows reports (never %SystemRoot% from the environment: E).
func msiexecPath() (string, error) {
	sys, err := windows.GetSystemDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(sys, "msiexec.exe"), nil
}

// Msiexec runs msiexec with args as this user, not elevated: for a per-machine package Windows Installer asks for consent itself.
func (w *WindowsMSI) Msiexec(ctx context.Context, args ...string) (int, error) {
	exe, err := msiexecPath()
	if err != nil {
		return 0, err
	}
	cmd := exec.CommandContext(ctx, exe, args...)
	err = cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	if err != nil {
		return 0, err
	}
	return 0, nil
}

// trayEnv is the environment the restarted Coral gets: this user's, without anything Coral's server put there (CORAL_*: the session and
// its values) except the data folder.
func trayEnv(environ []string) []string {
	var out []string
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(strings.ToUpper(name), "CORAL_") && !strings.EqualFold(name, "CORAL_DATA_DIR") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// Start starts coral-tray from the install folder as this user (this process is never elevated), detached, as the Start Menu does: the window
// opens only if it was open when the update began.
func (w *WindowsMSI) Start(ctx context.Context, install string) error {
	args := []string{}
	if !windowmark.Present(w.DataDir) {
		args = append(args, "--no-browser")
	}
	cmd := exec.Command(filepath.Join(install, "coral-tray.exe"), args...)
	cmd.Dir = install
	cmd.Env = trayEnv(os.Environ())
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// Healthy: a process of the install is up in three checks in a row, within the time.
func (w *WindowsMSI) Healthy(ctx context.Context, install string) error {
	deadline := w.HealthFor
	if deadline == 0 {
		deadline = 45 * time.Second
	}
	end := time.Now().Add(deadline)
	seen := 0
	for time.Now().Before(end) && ctx.Err() == nil {
		if procs, err := w.running(install); err == nil && len(procs) > 0 {
			seen++
			if seen >= 3 {
				return nil
			}
		} else {
			seen = 0
		}
		time.Sleep(2 * time.Second)
	}
	return errors.New("the new install did not stay up")
}

// Notify shows a message box and waits for the person, up to MessageWait (the box stays up until this process ends).
func (w *WindowsMSI) Notify(title, text string) {
	wait := w.MessageWait
	if wait == 0 {
		wait = 10 * time.Minute
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		t, _ := windows.UTF16PtrFromString(title)
		m, _ := windows.UTF16PtrFromString(text)
		_, _ = windows.MessageBox(0, m, t, windows.MB_OK|windows.MB_ICONINFORMATION|windows.MB_SETFOREGROUND|windows.MB_TOPMOST)
	}()
	select {
	case <-done:
	case <-time.After(wait):
	}
}
