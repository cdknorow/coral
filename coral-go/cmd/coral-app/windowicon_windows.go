//go:build windows

package main

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// The window's icon (title bar, taskbar, Alt-Tab). The exe carries the Coral icon as resource
// group 1 (rsrc_windows_amd64.syso, tools/gen_windows_resources.sh), which Explorer shows for the
// file; a window shows it only when its class or the window itself is given one, and webview's
// window class has none, so the title bar showed the generic application icon.
var (
	user32icon    = windows.NewLazySystemDLL("user32.dll")
	kernel32icon  = windows.NewLazySystemDLL("kernel32.dll")
	procLoadImage = user32icon.NewProc("LoadImageW")
	procSendMsg   = user32icon.NewProc("SendMessageW")
	procSetClass  = user32icon.NewProc("SetClassLongPtrW")
	procMetrics   = user32icon.NewProc("GetSystemMetrics")
	procModule    = kernel32icon.NewProc("GetModuleHandleW")
)

const (
	appIconResourceID = 1 // the icon group go-winres writes (checked by TestExeCarriesTheAppIcon)

	imageIcon   = 1
	lrShared    = 0x8000
	wmSetIcon   = 0x0080
	iconSmall   = 0
	iconBig     = 1
	smCxIcon    = 11
	smCyIcon    = 12
	smCxSmIcon  = 49
	smCySmIcon  = 50
	gclpHIcon   = -14
	gclpHIconSm = -34
)

// loadAppIcon loads the exe's own icon at cx x cy pixels (the closest size in the icon group,
// scaled if none matches). It returns an error when the exe has no icon resource.
func loadAppIcon(cx, cy int) (uintptr, error) {
	hinst, _, _ := procModule.Call(0)
	if hinst == 0 {
		return 0, fmt.Errorf("GetModuleHandle failed")
	}
	h, _, err := procLoadImage.Call(hinst, uintptr(appIconResourceID), imageIcon, uintptr(cx), uintptr(cy), lrShared)
	if h == 0 {
		return 0, fmt.Errorf("LoadImage(icon %d, %dx%d): %v", appIconResourceID, cx, cy, err)
	}
	return h, nil
}

func metric(index int) int {
	v, _, _ := procMetrics.Call(uintptr(index))
	return int(v)
}

// setWindowIcon gives hwnd the Coral icon at the system's small and large sizes, and its window
// class too so windows made later from it get it. Shared icons are owned by the system: nothing
// to free.
func setWindowIcon(hwnd uintptr) error {
	small, err := loadAppIcon(metric(smCxSmIcon), metric(smCySmIcon))
	if err != nil {
		return err
	}
	big, err := loadAppIcon(metric(smCxIcon), metric(smCyIcon))
	if err != nil {
		return err
	}
	procSendMsg.Call(hwnd, wmSetIcon, iconSmall, small)
	procSendMsg.Call(hwnd, wmSetIcon, iconBig, big)
	for _, c := range []struct {
		index int
		icon  uintptr
	}{{gclpHIconSm, small}, {gclpHIcon, big}} {
		procSetClass.Call(hwnd, uintptr(c.index), c.icon)
	}
	return nil
}
