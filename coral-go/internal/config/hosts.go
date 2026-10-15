package config

import (
	"net"
	"strings"
)

// Coral's listeners (#3594): --host (or CORAL_HOST) may name several addresses as a comma list,
// for example "127.0.0.1,100.101.102.103" to serve this computer and a tailnet address.

// Hosts splits a --host value into its addresses: trimmed, without empty entries or duplicates.
// A value with no address at all (""), like a single empty --host, is one entry: every interface.
func Hosts(host string) []string {
	var out []string
	seen := map[string]bool{}
	for _, h := range strings.Split(host, ",") {
		h = strings.TrimSpace(h)
		if h == "" || seen[strings.ToLower(h)] {
			continue
		}
		seen[strings.ToLower(h)] = true
		out = append(out, h)
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

// IsWildcardHost reports whether a bind address means every interface.
func IsWildcardHost(host string) bool {
	switch strings.TrimSpace(host) {
	case "", "0.0.0.0", "::", "[::]":
		return true
	}
	return false
}

// IsLoopbackHost reports whether a bind host only accepts local connections: every address in
// the list is a loopback one (one address reachable from the network makes the server reachable).
func IsLoopbackHost(host string) bool {
	for _, h := range Hosts(host) {
		if !isLoopbackAddr(h) {
			return false
		}
	}
	return true
}

func isLoopbackAddr(host string) bool {
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// AutoLoopback reports whether Coral adds a 127.0.0.1 listener of its own: --host names only
// specific addresses that aren't loopback (a tailnet or LAN address), so this computer's agents,
// command line and dashboard would otherwise have to go through the network address. Off with
// --no-auto-loopback (NoAutoLoopback).
func (c *Config) AutoLoopback() bool {
	if c.NoAutoLoopback {
		return false
	}
	for _, h := range Hosts(c.Host) {
		if IsWildcardHost(h) || isLoopbackAddr(h) {
			return false
		}
	}
	return true
}

// ListenHosts are the addresses Coral listens on: the --host list, and 127.0.0.1 first when
// AutoLoopback adds it.
func (c *Config) ListenHosts() []string {
	hosts := Hosts(c.Host)
	if c.AutoLoopback() {
		hosts = append([]string{"127.0.0.1"}, hosts...)
	}
	return hosts
}

// AgentHost is the address this computer's agents, workflow steps and tools use to reach Coral
// (CORAL_HOST, CORAL_URL, an agent's server_url): the loopback listener whenever there is one (an
// explicit loopback address, every interface, or the automatic listener), so an agent never goes
// through a network address and needs no key; otherwise (--no-auto-loopback with only network
// addresses) the first listed address.
func (c *Config) AgentHost() string {
	hosts := c.ListenHosts()
	for _, h := range hosts {
		if IsWildcardHost(h) || strings.EqualFold(strings.TrimSpace(h), "localhost") {
			return "127.0.0.1"
		}
		if ip := net.ParseIP(strings.Trim(h, "[]")); ip != nil && ip.IsLoopback() && ip.To4() != nil {
			return ip.String() // 127.0.0.1, or the 127/8 address it names
		}
	}
	for _, h := range hosts {
		if isLoopbackAddr(h) {
			return strings.Trim(h, "[]") // ::1 only
		}
	}
	return strings.Trim(hosts[0], "[]")
}
