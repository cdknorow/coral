package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// #3594: --host takes a comma list; Coral adds a 127.0.0.1 listener when the list names only
// network addresses (unless --no-auto-loopback), and this computer's agents always use the
// loopback listener when there is one.

func TestHosts(t *testing.T) {
	assert.Equal(t, []string{""}, Hosts(""))
	assert.Equal(t, []string{""}, Hosts(" , "))
	assert.Equal(t, []string{"127.0.0.1"}, Hosts("127.0.0.1"))
	assert.Equal(t, []string{"127.0.0.1", "100.101.102.103"}, Hosts(" 127.0.0.1 , 100.101.102.103,"))
	assert.Equal(t, []string{"100.64.0.1"}, Hosts("100.64.0.1,100.64.0.1"), "duplicates go")
	assert.Equal(t, []string{"::1", "fd7a::1"}, Hosts("::1,fd7a::1"))
}

func TestIsLoopbackHostWithAList(t *testing.T) {
	for _, h := range []string{"127.0.0.1", "localhost", "::1", "[::1]", "127.0.0.1,::1", "127.0.0.2"} {
		assert.True(t, IsLoopbackHost(h), h)
	}
	for _, h := range []string{"0.0.0.0", "", "::", "100.64.0.1", "127.0.0.1,100.64.0.1", "192.168.1.5,localhost"} {
		assert.False(t, IsLoopbackHost(h), h)
	}
}

func TestListenHostsAndAgentHost(t *testing.T) {
	for _, c := range []struct {
		host   string
		noAuto bool
		listen []string
		agent  string
		auto   bool
	}{
		{"0.0.0.0", false, []string{"0.0.0.0"}, "127.0.0.1", false},
		{"", false, []string{""}, "127.0.0.1", false},
		{"::", false, []string{"::"}, "127.0.0.1", false},
		{"127.0.0.1", false, []string{"127.0.0.1"}, "127.0.0.1", false},
		{"localhost", false, []string{"localhost"}, "127.0.0.1", false},
		{"::1", false, []string{"::1"}, "::1", false},
		{"127.0.0.2", false, []string{"127.0.0.2"}, "127.0.0.2", false},
		// Only network addresses: Coral adds 127.0.0.1, and agents use it.
		{"100.101.102.103", false, []string{"127.0.0.1", "100.101.102.103"}, "127.0.0.1", true},
		{"100.101.102.103,192.168.1.5", false, []string{"127.0.0.1", "100.101.102.103", "192.168.1.5"}, "127.0.0.1", true},
		// A loopback address in the list: nothing added.
		{"127.0.0.1,100.101.102.103", false, []string{"127.0.0.1", "100.101.102.103"}, "127.0.0.1", false},
		{"100.101.102.103,127.0.0.1", false, []string{"100.101.102.103", "127.0.0.1"}, "127.0.0.1", false},
		// Opted out: only the network address, and agents dial it.
		{"100.101.102.103", true, []string{"100.101.102.103"}, "100.101.102.103", false},
		{"fd7a:115c::1", true, []string{"fd7a:115c::1"}, "fd7a:115c::1", false},
	} {
		cfg := &Config{Host: c.host, NoAutoLoopback: c.noAuto}
		assert.Equal(t, c.listen, cfg.ListenHosts(), "%q noAuto=%v", c.host, c.noAuto)
		assert.Equal(t, c.agent, cfg.AgentHost(), "%q noAuto=%v", c.host, c.noAuto)
		assert.Equal(t, c.auto, cfg.AutoLoopback(), "%q noAuto=%v", c.host, c.noAuto)
	}
}
