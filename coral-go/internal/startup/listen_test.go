package startup

import (
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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
	// Port 0 picks a free port on the named address; the loopback listener then takes the same
	// number on 127.0.0.1, which another process can hold on a busy host: try another port.
	lns, port, err := listenAllRetrying(t, cfg)
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

	// --no-auto-loopback: only the named address. Proved by binding 127.0.0.1 on that port
	// ourselves (a connect could reach another process's socket); a port another process holds
	// on 127.0.0.1 is tried again with a new one, a loopback listener of ours fails every try.
	var lastErr error
	for try := 0; try < 5; try++ {
		opt := &config.Config{Host: ip, Port: 0, NoAutoLoopback: true}
		lns2, port2, err := listenAll(opt)
		require.NoError(t, err)
		require.Len(t, lns2, 1, "--no-auto-loopback: only the named address")
		require.Equal(t, ip, lns2[0].Addr().(*net.TCPAddr).IP.String())
		serveAll(t, lns2)
		assert.Equal(t, "ok", get(t, ip, port2))
		ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port2))
		if err == nil {
			ln.Close()
			return
		}
		lastErr = err
		t.Logf("try %d: %v", try+1, err)
	}
	t.Fatalf("127.0.0.1 on the named address's port stayed taken with --no-auto-loopback: %v", lastErr)
}

// addrInUse reports a bind refused because the port is taken (wrapped or only in the text).
func addrInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE) || (err != nil && strings.Contains(err.Error(), "address already in use"))
}

// listenAllRetrying is listenAll with port 0 where a second address must take the first one's
// port: a collision with another process's socket ("address already in use") is retried, up to
// five times; any other error is returned at once.
func listenAllRetrying(t *testing.T, cfg *config.Config) ([]net.Listener, int, error) {
	t.Helper()
	var err error
	for try := 0; try < 5; try++ {
		var lns []net.Listener
		var port int
		if lns, port, err = listenAll(cfg); err == nil || !addrInUse(err) {
			return lns, port, err
		}
		t.Logf("try %d: %v", try+1, err)
	}
	return nil, 0, err
}

// A comma list listens on each address; a busy port closes the ones already open.
func TestListenAllList(t *testing.T) {
	cfg := &config.Config{Host: "127.0.0.1, ::1", Port: 0}
	lns, port, err := listenAllRetrying(t, cfg)
	if err != nil {
		require.False(t, addrInUse(err), "a free port pair after five tries: %v", err)
		t.Skipf("no IPv6 loopback here: %v", err)
	}
	require.Len(t, lns, 2, "both loopback addresses, nothing added")
	serveAll(t, lns)
	assert.Equal(t, "ok", get(t, "127.0.0.1", port))
	assert.Equal(t, "ok", get(t, "::1", port))

	// The second address's port is taken: the first listener is closed again. Another process can
	// hold 127.0.0.1 on the port picked for ::1 (or take it meanwhile) on a busy host, so a port
	// whose 127.0.0.1 side is in use is skipped, and the check is tried up to five times: a
	// listener left open fails every try, another process's socket only by chance.
	var lastErr error
	for try := 0; try < 5; try++ {
		if lastErr = busyPortClosesTheFirstListener(t); lastErr == nil {
			return
		}
		t.Logf("try %d: %v", try+1, lastErr)
	}
	t.Fatalf("the 127.0.0.1 listener opened before the failure was closed: %v", lastErr)
}

// busyPortClosesTheFirstListener holds [::1]:P, has listenAll bind 127.0.0.1 then ::1 on P (so
// the second fails), and reports whether 127.0.0.1:P is free again.
func busyPortClosesTheFirstListener(t *testing.T) error {
	t.Helper()
	busy, err := net.Listen("tcp", "[::1]:0")
	require.NoError(t, err)
	defer busy.Close()
	bport := busy.Addr().(*net.TCPAddr).Port
	addr := "127.0.0.1:" + strconv.Itoa(bport)
	probe, err := net.Listen("tcp", addr)
	if err != nil {
		return err // someone else's port: try another
	}
	probe.Close()
	_, _, err = listenAll(&config.Config{Host: "127.0.0.1,::1", Port: bport})
	require.Error(t, err, "::1 is taken")
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return ln.Close()
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
	lns, port, err := listenAllRetrying(t, cfg)
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
