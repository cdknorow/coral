package httputil

import "net"

// IsIPBlocked reports whether ip is private, loopback, link-local, reserved or
// otherwise unsafe as an outbound target (SSRF protection). It is the exported
// form of the check used by ResolveAndValidateURL, for use at dial time.
func IsIPBlocked(ip net.IP) bool { return isIPBlocked(ip) }
