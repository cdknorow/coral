//go:build webview && windows

package main

import (
	"syscall"
	"unsafe"
)

var (
	user32     = syscall.NewLazyDLL("user32.dll")
	messageBox = user32.NewProc("MessageBoxW")
)

const (
	mbYesNo           = 0x00000004
	mbIconInformation = 0x00000040
	idYes             = 6
)

func showEULADialog(tosText string) bool {
	title, _ := syscall.UTF16PtrFromString("Coral — Terms of Service")
	text, _ := syscall.UTF16PtrFromString(tosText + "\n\nDo you accept the Terms of Service?")

	ret, _, _ := messageBox.Call(
		0,
		uintptr(unsafe.Pointer(text)),
		uintptr(unsafe.Pointer(title)),
		uintptr(mbYesNo|mbIconInformation),
	)
	return ret == idYes
}
