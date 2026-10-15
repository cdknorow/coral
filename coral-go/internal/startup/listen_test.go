package startup

import (
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/agentenv"
	"github.com/cdknorow/coral/internal/config"
)

// localNetworkIPv4 is a non-loopback IPv4 address of this machine (a LAN or tailnet address),
// or "" when it has none.
func localNetworkIPv4(t *testing.T) string {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && !ipn.IP.IsLoopback() && !ipn.IP.IsLinkLocalUnicast() {
			return ipn.IP.String()
		}
	}
	return ""
}

// serveAll answers "ok" on every listener, the way Start serves them with one http.Server.
func serveAll(t *testing.T, lns []net.Listener) {
	t.Helper()
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") })}
	for _, ln := range lns {
		go srv.Serve(ln)
	}
	t.Cleanup(func() { srv.Close() })
}

func get(t *testing.T, host string, port int) string {
	t.Helper()
	c := &http.Client{Timeout: 5 * time.Second}
	resp, err := c.Get("http://" + net.JoinHostPort(host, strconv.Itoa(port)) + "/")
	require.NoError(t, err, host)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// #3594: --host naming a network address listens there and on 127.0.0.1, one port; agents get
// the loopback URL. With --no-auto-loopback only the named address listens.
func TestListenAllAddsTheLoopbackListener(t *testing.T) {
	ip := localNetworkIPv4(t)
	if ip == "" {
		t.Skip("this machine has no non-loopback IPv4 address")
	}
	cfg := &config.Config{Host: ip, Port: 0}
	lns, port, err := listenAll(cfg)
	require.NoError(t, err)
	require.Len(t, lns, 2)
	serveAll(t, lns)
	assert.Equal(t, "ok", get(t, "127.0.0.1", port), "the automatic loopback listener answers")
	assert.Equal(t, "ok", get(t, ip, port), "the named address answers, on the same port")

	// The agents' environment names the loopback listener.
	// UseServerEnv rewrites this process's agent variables: put every one back afterwards.
	for _, k := range append(append([]string{}, agentenv.IdentityVars...), agentenv.LocationVars...) {
		if v, ok := os.LookupEnv(k); ok {
			t.Setenv(k, v)
		} else {
			t.Setenv(k, "")
			os.Unsetenv(k)
		}
	}
	agentenv.UseServerEnv(cfg.AgentHost(), port, "")
	assert.Equal(t, "http://127.0.0.1:"+strconv.Itoa(port), agentenvURL(t))

	opt := &config.Config{Host: ip, Port: 0, NoAutoLoopback: true}
	lns2, port2, err := listenAll(opt)
	require.NoError(t, err)
	require.Len(t, lns2, 1, "--no-auto-loopback: only the named address")
	serveAll(t, lns2)
	assert.Equal(t, "ok", get(t, ip, port2))
	c := &http.Client{Timeout: 2 * time.Second}
	if _, err := c.Get("http://127.0.0.1:" + strconv.Itoa(port2) + "/"); err == nil {
		t.Errorf("nothing should listen on 127.0.0.1:%d with --no-auto-loopback", port2)
	}
}

// A comma list listens on each address; a busy port closes the ones already open.
func TestListenAllList(t *testing.T) {
	cfg := &config.Config{Host: "127.0.0.1, ::1", Port: 0}
	lns, port, err := listenAll(cfg)
	if err != nil {
		t.Skipf("no IPv6 loopback here: %v", err)
	}
	require.Len(t, lns, 2, "both loopback addresses, nothing added")
	serveAll(t, lns)
	assert.Equal(t, "ok", get(t, "127.0.0.1", port))
	assert.Equal(t, "ok", get(t, "::1", port))

	// The second address's port is taken: the first listener is closed again.
	busy, err := net.Listen("tcp", "[::1]:0")
	require.NoError(t, err)
	defer busy.Close()
	bport := busy.Addr().(*net.TCPAddr).Port
	_, _, err = listenAll(&config.Config{Host: "127.0.0.1,::1", Port: bport})
	require.Error(t, err)
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(bport))
	require.NoError(t, err, "the 127.0.0.1 listener opened before the failure was closed")
	ln.Close()
}

func agentenvURL(t *testing.T) string {
	t.Helper()
	return os.Getenv("CORAL_URL")
}

// A wildcard with another address is refused before anything binds (Security's NIT 2).
func TestListenAllRefusesAWildcardInAList(t *testing.T) {
	lns, _, err := listenAll(&config.Config{Host: "0.0.0.0,127.0.0.1", Port: 0})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already includes every other address")
	assert.Empty(t, lns)
}
