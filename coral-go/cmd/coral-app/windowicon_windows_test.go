//go:build windows

package main

import (
	"syscall"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The test exe links the same rsrc_windows_amd64.syso as coral-app, so the icon is there to load.
func TestExeCarriesTheAppIcon(t *testing.T) {
	for _, size := range []int{16, 32, 48} {
		h, err := loadAppIcon(size, size)
		require.NoError(t, err, "%dx%d", size, size)
		assert.NotZero(t, h)
	}
}

func TestSetWindowIconGivesTheWindowBothSizes(t *testing.T) {
	user32 := syscall.NewLazyDLL("user32.dll")
	class, _ := syscall.UTF16PtrFromString("STATIC")
	hwnd, _, err := user32.NewProc("CreateWindowExW").Call(0, uintptr(unsafe.Pointer(class)), 0, 0, 0, 0, 10, 10, 0, 0, 0, 0)
	require.NotZero(t, hwnd, "CreateWindowEx: %v", err)
	t.Cleanup(func() { user32.NewProc("DestroyWindow").Call(hwnd) })

	const wmGetIcon = 0x007F
	get := func(kind uintptr) uintptr {
		v, _, _ := procSendMsg.Call(hwnd, wmGetIcon, kind, 0)
		return v
	}
	require.Zero(t, get(iconSmall), "a new window has no icon of its own")

	require.NoError(t, setWindowIcon(hwnd))
	assert.NotZero(t, get(iconSmall), "the title bar's icon")
	assert.NotZero(t, get(iconBig), "the taskbar's and Alt-Tab's icon")
}
