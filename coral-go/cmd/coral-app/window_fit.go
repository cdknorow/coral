//go:build webview

package main

// The window opens at the size asked for (1200x800 by default) unless the screen's work area (the screen less the taskbar) cannot hold it: a 1024x768 screen put its right and bottom edges
// off the screen (#6004 W5). fitSize brings the size inside the work area, less the window's own frame and title bar. A work area that is not known (0) leaves the size as asked.
const (
	windowFrameW = 16 // the frame's left and right edges, with a margin
	windowFrameH = 48 // the title bar and the bottom edge, with a margin
	minWindowW   = 480
	minWindowH   = 320
)

func fitSize(w, h, workW, workH int) (int, int) {
	if workW <= 0 || workH <= 0 {
		return w, h
	}
	return fitAxis(w, workW-windowFrameW, minWindowW), fitAxis(h, workH-windowFrameH, minWindowH)
}

// fitAxis keeps size when it fits in room; else room, but not below floor unless the room itself is smaller (then the room: never off the screen).
func fitAxis(size, room, floor int) int {
	if size <= room {
		return size
	}
	if room < floor {
		return max(room, 1)
	}
	return room
}

// placeOrigin is where the window's top-left goes to sit centred in the work area (left, top, width, height) and never left of or above it: the system's cascade put a 966x710 window at 78,78 on a
// 1024x768 screen, its right edge off the screen (#6004 W5). A window larger than the work area starts at its corner.
func placeOrigin(workL, workT, workW, workH, w, h int) (int, int) {
	return workL + max(0, (workW-w)/2), workT + max(0, (workH-h)/2)
}
