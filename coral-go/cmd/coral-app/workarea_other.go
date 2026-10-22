//go:build webview && !windows

package main

import "unsafe"

// workArea is not asked off Windows: the system places and limits a window there. 0, 0 is "not known".
func workArea() (int, int) { return 0, 0 }

// placeInWorkArea: the system places windows off Windows.
func placeInWorkArea(unsafe.Pointer) {}
