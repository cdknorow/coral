package startup

import (
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/agentenv"
	"github.com/cdknorow/coral/internal/auth"
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

// #3594 (d, e): both listeners get the same treatment, keyed to the peer address (#3585's
// direct-local test), never to which listener a request arrived on: on the loopback listener a
// direct request passes without a key, and its Host must name this server (localhost, loopback
// or a listened address; an unlisted IP or a domain is refused as DNS rebinding); on the network
// listener the peer isn't loopback, so the key is needed whatever the Host says. A forged tunnel
// stamp changes nothing on either.
func TestBothListenersAreKeyedToThePeer(t *testing.T) {
	ip := localNetworkIPv4(t)
	if ip == "" {
		t.Skip("this machine has no non-loopback IPv4 address")
	}
	cfg := &config.Config{Host: ip, Port: 0}
	lns, port, err := listenAll(cfg)
	require.NoError(t, err)
	auth.SetListenHosts(cfg.ListenHosts())
	t.Cleanup(func() { auth.SetListenHosts([]string{""}) })
	ks, err := auth.NewKeyStore(t.TempDir())
	require.NoError(t, err)
	srv := &http.Server{Handler: auth.Middleware(ks)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))}
	for _, ln := range lns {
		go srv.Serve(ln)
	}
	t.Cleanup(func() { srv.Close() })

	status := func(addr, host string, hdr map[string]string) int {
		req, err := http.NewRequest(http.MethodGet, "http://"+net.JoinHostPort(addr, strconv.Itoa(port))+"/api/x", nil)
		require.NoError(t, err)
		if host != "" {
			req.Host = host
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		return resp.StatusCode
	}
	p := ":" + strconv.Itoa(port)
	key := map[string]string{"Authorization": "Bearer " + ks.Key()}
	forged := map[string]string{"X-Coral-Via-Tunnel": "1"}

	// The loopback listener: a direct local request passes, with a Host naming this server.
	assert.Equal(t, 200, status("127.0.0.1", "", nil))
	assert.Equal(t, 200, status("127.0.0.1", "localhost"+p, nil))
	assert.Equal(t, 200, status("127.0.0.1", ip+p, nil), "the listened network address is this server too")
	assert.Equal(t, 403, status("127.0.0.1", "10.255.255.254"+p, nil), "an IP Coral doesn't listen on is DNS rebinding")
	assert.Equal(t, 403, status("127.0.0.1", "evil.example"+p, nil))
	assert.Equal(t, 200, status("127.0.0.1", "", forged), "a forged tunnel stamp changes nothing")
	assert.Equal(t, 401, status("127.0.0.1", "", map[string]string{"X-Forwarded-For": "1.2.3.4"}), "a proxied request isn't direct")

	// The network listener: the peer isn't loopback, so only the key passes, whatever the Host.
	assert.Equal(t, 401, status(ip, "", nil))
	assert.Equal(t, 401, status(ip, "localhost"+p, nil), "a loopback Host doesn't make a network peer local")
	assert.Equal(t, 401, status(ip, "", forged))
	assert.Equal(t, 200, status(ip, "", key))
}

// #3601 (weak site W17): the pipe log folder is this user's only, and an existing 0755 one (an
// older install) is tightened.
func TestPrepareLogDirIsPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	prepareLogDir(dir)
	fi, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), fi.Mode().Perm())
	require.NoError(t, os.Chmod(dir, 0o755))
	prepareLogDir(dir)
	fi, _ = os.Stat(dir)
	assert.Equal(t, os.FileMode(0o700), fi.Mode().Perm(), "an older 0755 folder is tightened")
}
