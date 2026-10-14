//go:build !linux && !darwin

package tunnel

import "syscall"

// LowLatencyControl: TCP_NOTSENT_LOWAT isn't available here; the uplink scheduler
// still orders what reaches the socket.
func LowLatencyControl(string, string, syscall.RawConn) error { return nil }
