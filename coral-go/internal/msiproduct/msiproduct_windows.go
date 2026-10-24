//go:build windows

// Package msiproduct reads Windows Installer's registration of the Coral product: which product is registered per machine, and its folder
// (#162, #147). It is the updater's lookup (internal/updater/apply) and the MSI helper's (cmd/coral-msi-helper, #335), and imports nothing
// of the updater, so the helper carried in the MSI stays small (TestSigstoreIsLinkedOnlyIntoTheUpdater).
package msiproduct

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// CoralUpgradeCode is tools/coral.wxs's UpgradeCode: the one Coral product, whatever its version.
const CoralUpgradeCode = "{B8F2E3A1-7C4D-4E5F-9A1B-2C3D4E5F6A7B}"

// coralExeComponent is tools/coral.wxs's CoralExe component: its key path is coral.exe in the install folder.
const coralExeComponent = "{A1B2C3D4-E5F6-7890-ABCD-EF1234567890}"

var (
	modMsi                     = windows.NewLazySystemDLL("msi.dll")
	procMsiEnumRelatedProducts = modMsi.NewProc("MsiEnumRelatedProductsW")
	procMsiGetComponentPathEx  = modMsi.NewProc("MsiGetComponentPathExW")
	procMsiGetProductInfoEx    = modMsi.NewProc("MsiGetProductInfoExW")
)

const (
	// MachineContext is MSIINSTALLCONTEXT_MACHINE.
	MachineContext   = 4
	errorNoMoreItems = 259 // ERROR_NO_MORE_ITEMS
)

// RelatedProducts lists every product registered with CoralUpgradeCode that this user can see, per machine and per user alike.
func RelatedProducts() ([]string, error) {
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

// MachineProperty is a product's property in the PER-MACHINE install context, and whether the product is installed there.
func MachineProperty(code, property string) (string, bool) {
	p, err1 := windows.UTF16PtrFromString(code)
	prop, err2 := windows.UTF16PtrFromString(property)
	if err1 != nil || err2 != nil {
		return "", false
	}
	size := uint32(256)
	buf := make([]uint16, size)
	r, _, _ := procMsiGetProductInfoEx.Call(uintptr(unsafe.Pointer(p)), 0, MachineContext, uintptr(unsafe.Pointer(prop)), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if r != 0 {
		return "", false
	}
	return windows.UTF16ToString(buf), true
}

// Registered is the ProductCode of the Coral product registered PER MACHINE, from related (RelatedProducts) and property (MachineProperty),
// which a test replaces. The UpgradeCode is public, so a same-user process could install a per-user product with it; such a product is
// never the one updated or removed (N1). None, or more than one per machine, is an error (fail closed).
func Registered(related func() ([]string, error), property func(code, property string) (string, bool)) (string, error) {
	codes, err := related()
	if err != nil {
		return "", err
	}
	var machine []string
	for _, c := range codes {
		if _, ok := property(c, "VersionString"); ok {
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

// Folder is the folder of the per-machine product: where its CoralExe component (coral.exe) is installed.
func Folder(product string) (string, error) {
	p, _ := windows.UTF16PtrFromString(product)
	c, _ := windows.UTF16PtrFromString(coralExeComponent)
	size := uint32(windows.MAX_LONG_PATH)
	buf := make([]uint16, size)
	// In the per-machine context only (N1), as Registered.
	r, _, _ := procMsiGetComponentPathEx.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(c)), 0, MachineContext, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	const installStateLocal = 3
	if int32(r) != installStateLocal {
		return "", fmt.Errorf("coral.exe is not installed locally (state %d)", int32(r))
	}
	return filepath.Dir(windows.UTF16ToString(buf)), nil
}

// Unsupported refuses an install that isn't under Program Files (a portable copy beside a registered product, or one moved).
func Unsupported(install string) string {
	pf, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, 0)
	if err != nil {
		return "unreadable_install"
	}
	if !UnderFold(install, pf) {
		if pfx86, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFilesX86, 0); err != nil || !UnderFold(install, pfx86) {
			return "portable_install"
		}
	}
	return ""
}

// UnderFold reports whether path is dir or inside it, compared as Windows compares paths (case-folded).
func UnderFold(path, dir string) bool {
	path, dir = strings.ToLower(filepath.Clean(path)), strings.ToLower(filepath.Clean(dir))
	return path == dir || strings.HasPrefix(path, dir+`\`)
}
