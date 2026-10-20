//go:build relaunchfixture && windows

package routes

import "os"

// The fixture seam checks Unix ownership and modes; on Windows it is not built to run (the check refuses).
var fixtureUID = func() int { return -1 }

func ownerUID(os.FileInfo) (int, bool) { return 0, false }
