//go:build windows

package dllpreload

import (
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// After the package's init, a bare-name lookup of each DLL finds the module already loaded, and it is the one in the system folder.
func TestPreloadedModulesAreTheSystemFoldersOwn(t *testing.T) {
	sysDir, err := windows.GetSystemDirectory()
	if err != nil {
		t.Fatal(err)
	}
	if failed := load(); len(failed) > 0 {
		t.Fatalf("not loaded: %v", failed)
	}
	for _, name := range Names {
		// By name and without loading, as a bare LoadLibraryW finds a module that is already loaded.
		name16, err := windows.UTF16PtrFromString(name)
		if err != nil {
			t.Fatal(err)
		}
		var h windows.Handle
		err = windows.GetModuleHandleEx(windows.GET_MODULE_HANDLE_EX_FLAG_UNCHANGED_REFCOUNT, name16, &h)
		if err != nil {
			t.Errorf("%s: not loaded after the preload: %v", name, err)
			continue
		}
		buf := make([]uint16, windows.MAX_LONG_PATH)
		n, err := windows.GetModuleFileName(h, &buf[0], uint32(len(buf)))
		if err != nil {
			t.Errorf("%s: module path: %v", name, err)
			continue
		}
		path := windows.UTF16ToString(buf[:n])
		if !strings.EqualFold(filepath.Dir(path), sysDir) {
			t.Errorf("%s loaded from %s, not the system folder %s", name, path, sysDir)
		}
	}
}
