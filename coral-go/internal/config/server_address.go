package config

import (
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ServerLockFile is the instance lock a running Coral server holds in its data folder. Once it listens, the server records
// "<pid>\n<url>\n" in it, and empties it again when it stops (#235).
const ServerLockFile = "coral-server.lock"

// RecordedServer is the PID and address the Coral server for coralDir recorded in its lock file: 0 and "" when there is none or it
// can't be read. url is only ever an http(s) address on this computer (localhost or a loopback IP), so a command that finds it
// never sends its token anywhere else.
func RecordedServer(coralDir string) (pid int, addr string) {
	b, err := os.ReadFile(filepath.Join(coralDir, ServerLockFile))
	if err != nil || len(b) > 4096 {
		return 0, ""
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if n, err := strconv.Atoi(strings.TrimSpace(lines[0])); err == nil && n > 0 {
		pid = n
	}
	if len(lines) > 1 {
		if u := strings.TrimSpace(lines[1]); localServerURL(u) {
			addr = u
		}
	}
	return pid, addr
}

// localServerURL: an http(s) URL with no credentials, path or query, whose host is localhost or a loopback IP.
func localServerURL(u string) bool {
	p, err := url.Parse(u)
	if err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.User != nil || p.Port() == "" ||
		(p.Path != "" && p.Path != "/") || p.RawQuery != "" || p.Fragment != "" {
		return false
	}
	if strings.EqualFold(p.Hostname(), "localhost") {
		return true
	}
	ip := net.ParseIP(p.Hostname())
	return ip != nil && ip.IsLoopback()
}
