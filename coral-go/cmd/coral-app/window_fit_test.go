//go:build webview

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// #6004 W5: the window opens inside the screen's work area.
func TestTheWindowIsFittedToTheWorkAreaAndNeverMadeLarger(t *testing.T) {
	for name, tc := range map[string]struct{ w, h, workW, workH, wantW, wantH int }{
		"1200x800 on a 1024x768 screen (work area 1024x728)":  {1200, 800, 1024, 728, 1008, 680},
		"it fits: left as asked":                              {1200, 800, 1920, 1040, 1200, 800},
		"only the height does not fit":                        {1200, 800, 1920, 700, 1200, 652},
		"only the width does not fit":                         {1200, 800, 1000, 1040, 984, 800},
		"a work area that is not known: as asked":             {1200, 800, 0, 0, 1200, 800},
		"one side not known: as asked":                        {1200, 800, 1024, 0, 1200, 800},
		"a small window on a small screen is not enlarged":    {600, 400, 1024, 728, 600, 400},
		"a tiny work area: the room, never off the screen":    {1200, 800, 300, 200, 284, 152},
		"exactly the room":                                    {1008, 680, 1024, 728, 1008, 680},
		"a window larger than a big screen on a wide display": {3000, 2000, 2560, 1400, 2544, 1352},
	} {
		w, h := fitSize(tc.w, tc.h, tc.workW, tc.workH)
		assert.Equal(t, [2]int{tc.wantW, tc.wantH}, [2]int{w, h}, name)
		if tc.workW > 0 && tc.workH > 0 {
			assert.LessOrEqual(t, w, max(tc.w, 1), name)
			assert.LessOrEqual(t, w+windowFrameW, max(tc.workW, w+windowFrameW), name)
		}
	}
}

func TestTheWindowIsCentredInTheWorkAreaAndNeverLeftOfOrAboveIt(t *testing.T) {
	x, y := placeOrigin(0, 0, 1024, 728, 1008, 680)
	assert.Equal(t, [2]int{8, 24}, [2]int{x, y}, "a fitted window on a 1024x768 screen")
	x, y = placeOrigin(0, 40, 1920, 1040, 1200, 800)
	assert.Equal(t, [2]int{360, 160}, [2]int{x, y}, "a work area that starts below a taskbar at the top")
	x, y = placeOrigin(-1920, 0, 1920, 1040, 1200, 800)
	assert.Equal(t, [2]int{-1560, 120}, [2]int{x, y}, "a screen to the left of the primary one")
	x, y = placeOrigin(0, 0, 800, 600, 1200, 900)
	assert.Equal(t, [2]int{0, 0}, [2]int{x, y}, "larger than the work area: its corner, not off to the left or above")
}
