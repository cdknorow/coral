package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// #5124 review: the tray hands the server a --host only when one was asked for. Started without --host (the default, and what the installers and
// launchers start), it passes none, so the server's own default (this computer only, or every interface once remote access is on) applies.
// Mutation: hostArgs hands 0.0.0.0 on when there was no host (the old tray default): every tray start listens on the network.
func TestTrayPassesNoHostUnlessOneWasAskedFor(t *testing.T) {
	assert.Empty(t, hostArgs(""), "no --host asked for: none handed to the server")
	assert.Equal(t, []string{"--host", "0.0.0.0"}, hostArgs("0.0.0.0"))
	assert.Equal(t, []string{"--host", "127.0.0.1,192.168.1.5"}, hostArgs("127.0.0.1,192.168.1.5"), "an asked-for host goes through as it is")
}
