//go:build webview && windows

package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32wa                  = windows.NewLazySystemDLL("user32.dll")
	procSystemParametersInfoW = user32wa.NewProc("SystemParametersInfoW")
	procGetWindowRectWA       = user32wa.NewProc("GetWindowRect")
	procSetWindowPosWA        = user32wa.NewProc("SetWindowPos")
)

const (
	spiGetWorkArea = 0x0030
	swpNoSize      = 0x0001
	swpNoZOrder    = 0x0004
	swpNoActivate  = 0x0010
)

type winRect struct{ left, top, right, bottom int32 }

// workRect is the primary screen's work area as a rectangle.
func workRect() (winRect, bool) {
	var r winRect
	ok, _, _ := procSystemParametersInfoW.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&r)), 0)
	if ok == 0 || r.right <= r.left || r.bottom <= r.top {
		return winRect{}, false
	}
	return r, true
}

// placeInWorkArea moves the window, without resizing it, to sit centred in the work area (placeOrigin). Nothing happens when the work area or the window cannot be read.
func placeInWorkArea(hwnd unsafe.Pointer) {
	wa, ok := workRect()
	if !ok || hwnd == nil {
		return
	}
	var r winRect
	if ok, _, _ := procGetWindowRectWA.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&r))); ok == 0 {
		return
	}
	x, y := placeOrigin(int(wa.left), int(wa.top), int(wa.right-wa.left), int(wa.bottom-wa.top), int(r.right-r.left), int(r.bottom-r.top))
	procSetWindowPosWA.Call(uintptr(hwnd), 0, uintptr(x), uintptr(y), 0, 0, swpNoSize|swpNoZOrder|swpNoActivate)
}

// workArea is the primary screen's work area (the screen less the taskbar) in the units this process's windows are sized in; 0, 0 when it cannot be read.
func workArea() (int, int) {
	r, ok := workRect()
	if !ok {
		return 0, 0
	}
	return int(r.right - r.left), int(r.bottom - r.top)
}
