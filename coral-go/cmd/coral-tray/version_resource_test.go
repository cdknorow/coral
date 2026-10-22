package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

// Windows names an app in the taskbar, Task Manager and the jump list by its exe's FileDescription; with
// no version resource it falls back to the file name ("coral-app.exe"). Pin that every user-facing exe
// links one (tools/gen_windows_resources.sh).
func TestUserFacingExesLinkVersionInfo(t *testing.T) {
	want := map[string]map[string]string{
		"coral":        {"FileDescription": "Coral server", "OriginalFilename": "coral.exe"},
		"launch-coral": {"FileDescription": "Coral launcher", "OriginalFilename": "launch-coral.exe"},
		"coral-tray":   {"FileDescription": "Coral tray", "OriginalFilename": "coral-tray.exe"},
		"coral-app":    {"FileDescription": "Coral", "OriginalFilename": "coral-app.exe"},
	}
	for cmd, fields := range want {
		syso, err := os.ReadFile(filepath.Join("..", cmd, "rsrc_windows_amd64.syso"))
		if err != nil {
			t.Errorf("%s: %v (run tools/gen_windows_resources.sh)", cmd, err)
			continue
		}
		fields["ProductName"] = "Coral"
		fields["CompanyName"] = "Subgentic LLC"
		fields["InternalName"] = cmd
		for key, val := range fields {
			if got, ok := versionString(syso, key); !ok || got != val {
				t.Errorf("%s: %s = %q (found %v), want %q (run tools/gen_windows_resources.sh)", cmd, key, got, ok, val)
			}
		}
		if got, _ := versionString(syso, "LegalCopyright"); !strings.HasPrefix(got, "Copyright ") {
			t.Errorf("%s: LegalCopyright = %q", cmd, got)
		}
	}
}

// versionString finds a string of the VS_VERSION_INFO block (key and value are UTF-16, the value starts
// at the next 32-bit boundary after the key).
func versionString(syso []byte, key string) (string, bool) {
	var pat []byte
	for _, u := range utf16.Encode([]rune(key + "\x00")) {
		pat = append(pat, byte(u), byte(u>>8))
	}
	i := strings.Index(string(syso), string(pat))
	if i < 0 {
		return "", false
	}
	p := i + len(pat)
	for p+1 < len(syso) && syso[p] == 0 && syso[p+1] == 0 { // padding up to the value
		p += 2
	}
	var u []uint16
	for ; p+1 < len(syso); p += 2 {
		c := uint16(syso[p]) | uint16(syso[p+1])<<8
		if c == 0 {
			break
		}
		u = append(u, c)
	}
	return string(utf16.Decode(u)), true
}
