package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// The Windows tray loads icon.ico and the shell reads the exe's icon resource. A white or
// single-size icon is invisible or blurry on a taskbar, so pin the sizes, and pin that every
// user-facing exe links the icon (tools/gen_windows_resources.sh).
func TestWindowsIconHasAllSizes(t *testing.T) {
	data, err := os.ReadFile("icon.ico")
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 6 || binary.LittleEndian.Uint16(data[0:2]) != 0 || binary.LittleEndian.Uint16(data[2:4]) != 1 {
		t.Fatal("icon.ico is not an ICO file")
	}
	n := int(binary.LittleEndian.Uint16(data[4:6]))
	got := map[int]bool{}
	for i := 0; i < n; i++ {
		e := data[6+16*i : 22+16*i]
		size := int(e[0])
		if size == 0 {
			size = 256
		}
		got[size] = true
		length := int(binary.LittleEndian.Uint32(e[8:12]))
		off := int(binary.LittleEndian.Uint32(e[12:16]))
		if off+length > len(data) {
			t.Fatalf("entry %d runs past the end of the file", size)
		}
		img := data[off : off+length]
		if size <= 48 && binary.LittleEndian.Uint32(img[0:4]) != 40 {
			t.Errorf("%dpx entry is not a BMP/DIB image", size)
		}
	}
	for _, want := range []int{16, 24, 32, 48, 256} {
		if !got[want] {
			t.Errorf("icon.ico has no %dpx image", want)
		}
	}
}

func TestUserFacingExesLinkTheIconResource(t *testing.T) {
	ico, err := os.ReadFile("icon.ico")
	if err != nil {
		t.Fatal(err)
	}
	// A slice from the middle of the 48px DIB (artwork pixels, not header or transparent
	// corners) must appear in each resource.
	e := ico[6+16*3 : 22+16*3]
	img := ico[binary.LittleEndian.Uint32(e[12:16]):][:binary.LittleEndian.Uint32(e[8:12])]
	chunk := img[len(img)/2-64 : len(img)/2+64]
	for _, cmd := range []string{"coral", "launch-coral", "coral-tray", "coral-app"} {
		syso, err := os.ReadFile(filepath.Join("..", cmd, "rsrc_windows_amd64.syso"))
		if err != nil {
			t.Errorf("%s: %v (run tools/gen_windows_resources.sh)", cmd, err)
			continue
		}
		if !bytes.Contains(syso, chunk) {
			t.Errorf("%s: rsrc_windows_amd64.syso does not contain the current icon (run tools/gen_windows_resources.sh)", cmd)
		}
	}
}
