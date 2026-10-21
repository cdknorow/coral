//go:build windows

package dllpreload

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func moduleDir(t *testing.T, h windows.Handle) string {
	t.Helper()
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetModuleFileName(h, &buf[0], uint32(len(buf)))
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(windows.UTF16ToString(buf[:n]))
}

// The package's own init (it has run by the time a test starts: nothing here loads anything) left each DLL loaded, and from the system folder.
func TestPreloadedModulesAreTheSystemFoldersOwn(t *testing.T) {
	sysDir, err := windows.GetSystemDirectory()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range Names {
		name16, err := windows.UTF16PtrFromString(name)
		if err != nil {
			t.Fatal(err)
		}
		var h windows.Handle
		if err := windows.GetModuleHandleEx(windows.GET_MODULE_HANDLE_EX_FLAG_UNCHANGED_REFCOUNT, name16, &h); err != nil {
			t.Errorf("%s: not loaded after the package's init: %v", name, err)
			continue
		}
		if dir := moduleDir(t, h); !strings.EqualFold(dir, sysDir) {
			t.Errorf("%s loaded from %s, not the system folder %s", name, dir, sysDir)
		}
	}
}

// The attack itself (#5710 finding 3): a file named dwmapi.dll beside the program. dwmapi is not a KnownDLL, so a bare-name LoadLibrary
// searches the program's folder first. The webview does exactly that bare-name call; after the preload it must get the system's module, and the decoy
// (not a DLL at all, so loading it would fail) must never be touched.
func TestADecoyBesideTheProgramIsNotLoadedByABareName(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	decoy := filepath.Join(filepath.Dir(exe), "dwmapi.dll")
	if err := os.WriteFile(decoy, []byte("not a dll"), 0o600); err != nil {
		t.Skipf("cannot plant the decoy beside the test program: %v", err)
	}
	t.Cleanup(func() { os.Remove(decoy) })
	sysDir, err := windows.GetSystemDirectory()
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.LoadLibrary("dwmapi.dll") // the webview's call: LoadLibraryW with a bare name
	if err != nil {
		t.Fatalf("a bare-name load of dwmapi.dll failed with a decoy beside the program: %v", err)
	}
	defer windows.FreeLibrary(h)
	if dir := moduleDir(t, h); !strings.EqualFold(dir, sysDir) {
		t.Fatalf("dwmapi.dll came from %s, not the system folder %s", dir, sysDir)
	}
}
