package startup

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/config"
)

// selfSignedPair writes a certificate for localhost/127.0.0.1 and its key, and returns
// their paths and a pool that trusts the certificate.
func selfSignedPair(t *testing.T) (certFile, keyFile string, pool *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "coral-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	kder, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	dir := t.TempDir()
	certFile, keyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	require.NoError(t, os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	require.NoError(t, os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}), 0o600))
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	pool = x509.NewCertPool()
	pool.AddCert(cert)
	return certFile, keyFile, pool
}

func TestLoadTLSConfig(t *testing.T) {
	cfg, err := loadTLSConfig("", " ")
	require.NoError(t, err)
	assert.Nil(t, cfg, "neither file: no TLS")

	certFile, keyFile, _ := selfSignedPair(t)
	for _, pair := range [][2]string{{certFile, ""}, {"", keyFile}} {
		_, err := loadTLSConfig(pair[0], pair[1])
		require.Error(t, err)
		assert.Contains(t, err.Error(), "needs both --tls-cert and --tls-key")
	}
	_, err = loadTLSConfig(certFile, filepath.Join(t.TempDir(), "missing.pem"))
	require.Error(t, err, "a pair that does not load stops startup")
	_, err = loadTLSConfig(keyFile, certFile)
	require.Error(t, err, "swapped files")

	cfg, err = loadTLSConfig(certFile, keyFile)
	require.NoError(t, err)
	assert.Equal(t, uint16(tls.VersionTLS12), cfg.MinVersion)
	assert.Equal(t, []string{"http/1.1"}, cfg.NextProtos)
}

// serveSniffed runs handler behind the sniffing listener on a loopback port.
func serveSniffed(t *testing.T, handler http.Handler) string {
	t.Helper()
	certFile, keyFile, _ := selfSignedPair(t)
	cfg, err := loadTLSConfig(certFile, keyFile)
	require.NoError(t, err)
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := &http.Server{Handler: requireTLSFromNetwork(handler), ReadHeaderTimeout: 5 * time.Second}
	go srv.Serve(newSniffListener(raw, cfg))
	t.Cleanup(func() { srv.Close() })
	return raw.Addr().String()
}

// One port: HTTPS (Request.TLS set) and, from this computer, plain HTTP.
func TestSniffListenerServesTLSAndLocalPlainHTTP(t *testing.T) {
	addr := serveSniffed(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, map[bool]string{true: "tls", false: "plain"}[r.TLS != nil])
	}))
	get := func(c *http.Client, url string) string {
		resp, err := c.Get(url)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // test certificate
	assert.Equal(t, "tls", get(&http.Client{Transport: tr}, "https://"+addr+"/"))
	assert.Equal(t, "plain", get(http.DefaultClient, "http://"+addr+"/"), "local tools keep using http://localhost")

	// A client that connects and sends nothing doesn't hold up the others.
	idle, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	defer idle.Close()
	assert.Equal(t, "plain", get(http.DefaultClient, "http://"+addr+"/again"))
}

// From the network, plain HTTP is never served: GET/HEAD go to https, anything else is
// refused.
func TestRequireTLSFromNetwork(t *testing.T) {
	served := 0
	h := requireTLSFromNetwork(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { served++ }))
	do := func(method, remote string, tlsOn bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://192.168.1.10:8420/api/sessions/live?x=1", strings.NewReader(""))
		r.RemoteAddr = remote
		if tlsOn {
			r.TLS = &tls.ConnectionState{}
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	rec := do("GET", "192.168.1.20:5000", false)
	assert.Equal(t, http.StatusPermanentRedirect, rec.Code)
	assert.Equal(t, "https://192.168.1.10:8420/api/sessions/live?x=1", rec.Header().Get("Location"))
	rec = do("POST", "192.168.1.20:5000", false)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "only HTTPS")
	assert.Zero(t, served, "nothing plain from the network reaches Coral's routes")

	assert.Equal(t, http.StatusOK, do("POST", "192.168.1.20:5000", true).Code, "HTTPS from the network")
	assert.Equal(t, http.StatusOK, do("POST", "127.0.0.1:5000", false).Code, "plain from this computer")
	assert.Equal(t, http.StatusOK, do("GET", "[::1]:5000", false).Code)
	assert.Equal(t, 3, served)
}

func TestRemoteAccessWarning(t *testing.T) {
	assert.Contains(t, remoteAccessWarning("0.0.0.0", false), "remote access is on over plain HTTP")
	assert.Contains(t, remoteAccessWarning("0.0.0.0", false), "--tls-cert and --tls-key")
	assert.Empty(t, remoteAccessWarning("0.0.0.0", true), "TLS on")
	assert.Empty(t, remoteAccessWarning("127.0.0.1", false), "local only")
}

// #3355: Coral started with a certificate pair answers /api/health over HTTPS, and
// still over plain HTTP on loopback; with only one of the two files it doesn't start.
func TestStartWithTLSAnswersHealthOverHTTPS(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CORAL_NO_DEMO_SEED", "1")
	certFile, keyFile, pool := selfSignedPair(t)

	home := t.TempDir()
	cfg := config.Load(home)
	cfg.Host = "127.0.0.1"
	cfg.Port = freePort(t)
	_, err := Start(context.Background(), cfg, Options{BackendType: "pty", TLSCert: certFile})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "needs both --tls-cert and --tls-key")

	cfg = config.Load(home)
	cfg.Host = "127.0.0.1"
	cfg.Port = freePort(t)
	rs, err := Start(context.Background(), cfg, Options{BackendType: "pty", TLSCert: certFile, TLSKey: keyFile})
	require.NoError(t, err)
	t.Cleanup(func() { rs.Shutdown(5 * time.Second) })
	assert.True(t, cfg.TLS)

	c := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	base := "https://127.0.0.1:" + strconv.Itoa(cfg.Port)
	var resp *http.Response
	require.Eventually(t, func() bool {
		resp, err = c.Get(base + "/api/health")
		return err == nil
	}, 10*time.Second, 50*time.Millisecond)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.NotNil(t, resp.TLS, "verified against the test certificate")

	plain, err := http.Get("http://127.0.0.1:" + strconv.Itoa(cfg.Port) + "/api/health")
	require.NoError(t, err)
	plain.Body.Close()
	assert.Equal(t, http.StatusOK, plain.StatusCode, "plain HTTP on loopback still works")

	// #3387: an app running Coral in-process gets a fresh sign-in link with no API key.
	link, err := rs.NewLoginLink(cfg.Port)
	require.NoError(t, err)
	assert.Contains(t, link, "/?login_token=")
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp2, err := noFollow.Get(strings.Replace(link, "localhost", "127.0.0.1", 1))
	require.NoError(t, err)
	resp2.Body.Close()
	assert.Equal(t, http.StatusFound, resp2.StatusCode)
	assert.NotEmpty(t, resp2.Cookies(), "signed in")
	assert.NotEmpty(t, rs.LoginToken, "the startup token too")
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}
