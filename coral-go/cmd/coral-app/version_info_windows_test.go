//go:build windows

package main

import (
	"os"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// The version resource as Windows itself reads it (what the taskbar and Task Manager use), from the test
// exe, which links the same rsrc_windows_amd64.syso as coral-app.
func TestWindowsReadsTheVersionInfo(t *testing.T) {
	exe, err := os.Executable()
	require.NoError(t, err)
	size, err := windows.GetFileVersionInfoSize(exe, nil)
	require.NoError(t, err)
	buf := make([]byte, size)
	require.NoError(t, windows.GetFileVersionInfo(exe, 0, size, unsafe.Pointer(&buf[0])))
	query := func(name string) string {
		var p *uint16
		var n uint32
		// 0409 = US English, 04b0 = Unicode: the block go-winres writes.
		require.NoError(t, windows.VerQueryValue(unsafe.Pointer(&buf[0]), `\StringFileInfo\040904b0\`+name, unsafe.Pointer(&p), &n), name)
		return windows.UTF16PtrToString(p)
	}
	assert.Equal(t, "Coral", query("FileDescription"))
	assert.Equal(t, "Coral", query("ProductName"))
	assert.Equal(t, "Coral", query("CompanyName"))
	assert.Equal(t, "coral-app.exe", query("OriginalFilename"))
}
