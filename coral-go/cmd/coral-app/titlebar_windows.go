//go:build webview && windows

package main

import (
	"log"
	"os"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/subgentic/coral/internal/winicon"
)

var (
	dwmapi   = windows.NewLazySystemDLL("dwmapi.dll")
	user32tb = windows.NewLazySystemDLL("user32.dll")

	procEnumWindows              = user32tb.NewProc("EnumWindows")
	procGetWindowThreadProcessId = user32tb.NewProc("GetWindowThreadProcessId")
	procIsWindowVisible          = user32tb.NewProc("IsWindowVisible")
	procDwmSetWindowAttribute    = dwmapi.NewProc("DwmSetWindowAttribute")
)

const (
	dwmwaUseImmersiveDarkMode   = 20
	dwmwaWindowCornerPreference = 33
	dwmwaBorderColor            = 34
	dwmwaCaptionColor           = 35
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

		// The window's own icon: the exe's resource is not used for a window unless it is set on it.
		if err := winicon.SetWindowIcon(hwnd); err != nil {
			log.Printf("[TITLEBAR] window icon: %v", err)
		}

		// The dark theme's colours until the page says what its theme is (setTitlebarColors): a page that already did is not painted over.
		if !pageColoredTitlebar.Load() {
			setDWMAttr(hwnd, dwmwaUseImmersiveDarkMode, int32(1))

			// Caption background: #111114 (--bg-primary) as COLORREF 0x00BBGGRR
			setDWMAttr(hwnd, dwmwaCaptionColor, uint32(0x00141111))

			// Border: #2e2e36 (--border) as COLORREF
			setDWMAttr(hwnd, dwmwaBorderColor, uint32(0x00362e2e))
		}

		// Rounded corners on Windows 11 (DWMWCP_ROUND = 2)
		setDWMAttr(hwnd, dwmwaWindowCornerPreference, int32(2))

		log.Println("[TITLEBAR] dark title bar configured")
	}()
}

// pageColoredTitlebar is set once the page has reported its colours: the start-up defaults must not paint over them.
var pageColoredTitlebar atomic.Bool

// colorref is a COLORREF, 0x00BBGGRR.
func colorref(r, g, b uint8) uint32 { return uint32(r) | uint32(g)<<8 | uint32(b)<<16 }

// setTitlebarColors paints the caption and border from the page's own colours (--bg-primary and --border), and switches the caption's text between light and dark
// with them, so a light page does not sit under a black caption. The page calls it when its theme changes (a choice in Settings, or the system's mode under "System").
func setTitlebarColors(bg, border string) {
	r, g, b, ok := parseCSSColor(bg)
	if !ok {
		return
	}
	pageColoredTitlebar.Store(true)
	hwnd := findMainWindow()
	if hwnd == 0 {
		return
	}
	dark := int32(0)
	if isDarkColor(r, g, b) {
		dark = 1
	}
	setDWMAttr(hwnd, dwmwaUseImmersiveDarkMode, dark)
	setDWMAttr(hwnd, dwmwaCaptionColor, colorref(r, g, b))
	if br, bgc, bb, ok := parseCSSColor(border); ok {
		setDWMAttr(hwnd, dwmwaBorderColor, colorref(br, bgc, bb))
	} else {
		setDWMAttr(hwnd, dwmwaBorderColor, colorref(r, g, b))
	}
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
