//go:build windows

package server

import "time"

// processCPU: Windows has no getrusage; the scale test reports no CPU time there.
func processCPU() time.Duration { return 0 }
