//go:build webview && windows

package main

import (
	"golang.org/x/sys/windows"
	"syscall"
	"unsafe"

	"github.com/subgentic/coral/internal/eulatext"
)

var (
	user32     = windows.NewLazySystemDLL("user32.dll")
	messageBox = user32.NewProc("MessageBoxW")
)

const (
	mbYesNo        = 0x00000004
	mbIconQuestion = 0x00000020
	mbDefButton2   = 0x00000100 // the second button, No, is the default: Enter must not accept terms nobody has read
	idYes          = 6
)

func showEULADialog(tosText string) bool {
	title, _ := syscall.UTF16PtrFromString(eulatext.DialogTitle)
	text, _ := syscall.UTF16PtrFromString(tosText + "\n\n" + eulatext.DialogIntro + "\nYes = " + eulatext.AcceptLabel + ", No = " + eulatext.DeclineLabel + " (the default).")

	ret, _, _ := messageBox.Call(
		0,
		uintptr(unsafe.Pointer(text)),
		uintptr(unsafe.Pointer(title)),
		uintptr(mbYesNo|mbIconQuestion|mbDefButton2),
	)
	return ret == idYes
}
