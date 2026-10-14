package httputil

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
)

// ResolveAndValidateURL resolves a URL's hostname and validates it doesn't
// target internal/private networks (SSRF protection). Returns the first safe
// resolved IP, or an error if unsafe or unresolvable.
func ResolveAndValidateURL(rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid URL")
	}

	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("URL scheme must be http or https")
	}

	hostname := parsed.Hostname()
	if hostname == "" {
		return "", fmt.Errorf("URL missing hostname")
	}

	port := parsed.Port()
	if port == "" {
		port = "80"
	}
	portNum, err := strconv.Atoi(port)
	if err != nil || portNum < 1 || portNum > 65535 {
		return "", fmt.Errorf("invalid port")
	}

	addrs, err := net.LookupHost(hostname)
	if err != nil {
		return "", fmt.Errorf("cannot resolve hostname: %w", err)
	}

	// Check ALL resolved IPs — if any are blocked, reject
	for _, addr := range addrs {
		ip := net.ParseIP(addr)
		if ip == nil {
			return "", fmt.Errorf("invalid IP address resolved")
		}
		if isIPBlocked(ip) {
			return "", fmt.Errorf("remote server URL resolves to a private or reserved IP address")
		}
	}

	if len(addrs) == 0 {
		return "", fmt.Errorf("no addresses resolved for hostname")
	}

	return addrs[0], nil
}

// Pre-parsed CIDR blocks for SSRF checks (parsed once at init, not on every call).
var blockedCIDRs []*net.IPNet

func init() {
	for _, cidr := range []string{
		"0.0.0.0/8",       // "this network": 0.x addresses reach the local host on some systems
		"100.64.0.0/10",   // CGNAT
		"192.0.2.0/24",    // Documentation (TEST-NET-1)
		"198.51.100.0/24", // Documentation (TEST-NET-2)
		"203.0.113.0/24",  // Documentation (TEST-NET-3)
	} {
		_, network, _ := net.ParseCIDR(cidr)
		blockedCIDRs = append(blockedCIDRs, network)
	}
}

// IsIPBlocked checks if an IP address is private, reserved, or otherwise unsafe for SSRF.
func isIPBlocked(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return true
	}

	for _, network := range blockedCIDRs {
		if network.Contains(ip) {
			return true
		}
	}

	return false
}
