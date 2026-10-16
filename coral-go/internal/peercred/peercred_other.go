//go:build !linux && !darwin && !windows

package peercred

import (
	"errors"
	"net/netip"
)

// Supported reports whether this platform can tell the peer's user.
const Supported = false

func ownerIsMe(client, server netip.AddrPort) (bool, error) {
	return false, errors.New("peercred: unsupported on this system")
}
