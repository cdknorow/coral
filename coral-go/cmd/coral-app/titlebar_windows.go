//go:build webview && windows

package main

import (
	"log"
	"os"
	"syscall"
	"time"
	"unsafe"
)

var (
	dwmapi   = syscall.NewLazyDLL("dwmapi.dll")
	user32tb = syscall.NewLazyDLL("user32.dll")

	procEnumWindows              = user32tb.NewProc("EnumWindows")
	procGetWindowThreadProcessId = user32tb.NewProc("GetWindowThreadProcessId")
	procIsWindowVisible          = user32tb.NewProc("IsWindowVisible")
	procDwmSetWindowAttribute    = dwmapi.NewProc("DwmSetWindowAttribute")
)

const (
	dwmwaUseImmersiveDarkMode  = 20
	dwmwaWindowCornerPreference = 33
	dwmwaBorderColor           = 34
	dwmwaCaptionColor          = 35
)

// setupNativeTitlebar styles the Windows title bar to match the Coral dark
// theme using Desktop Window Manager attributes.
func setupNativeTitlebar() {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[TITLEBAR] panic: %v", r)
			}
		}()

		time.Sleep(300 * time.Millisecond)

		if err := procDwmSetWindowAttribute.Find(); err != nil {
			log.Printf("[TITLEBAR] DwmSetWindowAttribute unavailable: %v", err)
			return
		}

		hwnd := findMainWindow()
		if hwnd == 0 {
			log.Println("[TITLEBAR] window not found")
			return
		}
		log.Printf("[TITLEBAR] found window HWND=%x", hwnd)

		setDWMAttr(hwnd, dwmwaUseImmersiveDarkMode, int32(1))

		// Caption background: #111114 (--bg-primary) as COLORREF 0x00BBGGRR
		setDWMAttr(hwnd, dwmwaCaptionColor, uint32(0x00141111))

		// Border: #2e2e36 (--border) as COLORREF
		setDWMAttr(hwnd, dwmwaBorderColor, uint32(0x00362e2e))

		// Rounded corners on Windows 11 (DWMWCP_ROUND = 2)
		setDWMAttr(hwnd, dwmwaWindowCornerPreference, int32(2))

		log.Println("[TITLEBAR] dark title bar configured")
	}()
}

func setDWMAttr[T int32 | uint32](hwnd uintptr, attr int, val T) {
	procDwmSetWindowAttribute.Call(hwnd,
		uintptr(attr),
		uintptr(unsafe.Pointer(&val)),
		unsafe.Sizeof(val))
}

func findMainWindow() uintptr {
	pid := uint32(os.Getpid())
	var result uintptr
	cb := syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
		var windowPID uint32
		procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&windowPID)))
		if windowPID == pid {
			if vis, _, _ := procIsWindowVisible.Call(hwnd); vis != 0 {
				result = hwnd
				return 0
			}
		}
		return 1
	})
	procEnumWindows.Call(cb, 0)
	return result
}
