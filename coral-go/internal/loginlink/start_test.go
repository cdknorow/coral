package loginlink_test

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/cloud"
	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/loginlink"
	"github.com/cdknorow/coral/internal/startup"
)

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// #3394: a real Coral. The desktop window (another process) gets a sign-in link through
// the command-line handshake, the tray and launch-coral get one from the server they run;
// each link signs in once (a session cookie, then the same page without the token), with
// no API key anywhere.
func TestAppsSignInWithoutTheAPIKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CORAL_NO_DEMO_SEED", "1")
	cfg := config.Load(t.TempDir())
	cfg.Host = "127.0.0.1"
	cfg.Port = freePort(t)
	rs, err := startup.Start(context.Background(), cfg, startup.Options{BackendType: "pty"})
	require.NoError(t, err)
	// Shutdown stops serving; Close releases the instance lock and the databases, as coral does,
	// or Windows can't remove the test's folder with coral-server.lock open (#3920 T2).
	t.Cleanup(func() { rs.Shutdown(5 * time.Second); rs.Close() })
	base := "http://127.0.0.1:" + strconv.Itoa(cfg.Port)

	noFollow := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	signIn := func(link string) *http.Response {
		t.Helper()
		resp, err := noFollow.Get(strings.Replace(link, "localhost", "127.0.0.1", 1))
		require.NoError(t, err)
		resp.Body.Close()
		return resp
	}

	// coral-app: out of process, through the handshake.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	link, err := loginlink.FromCLI(ctx, base+"/", cfg.CoralDir())
	require.NoError(t, err)
	require.True(t, loginlink.HasToken(link))
	assert.True(t, strings.HasPrefix(link, base+"/?login_token="), loginlink.Redact(link))
	resp := signIn(link)
	assert.Equal(t, http.StatusFound, resp.StatusCode)
	var session *http.Cookie
	for _, c := range resp.Cookies() {
		if strings.HasPrefix(c.Name, "coral_session_") { // per server since #3682
			session = c
		}
	}
	require.NotNil(t, session, "signed in: the session cookie is set")
	assert.True(t, session.HttpOnly)
	assert.NotContains(t, resp.Header.Get("Location"), "login_token", "the token is dropped from the address")
	assert.Equal(t, http.StatusForbidden, signIn(link).StatusCode, "single use")

	// Each open asks again: a second link is a different, working one.
	link2, err := loginlink.FromCLI(ctx, base, cfg.CoralDir())
	require.NoError(t, err)
	assert.NotEqual(t, link, link2)
	assert.Equal(t, http.StatusFound, signIn(link2).StatusCode)

	// The tray / launch-coral: in process.
	mint := func() (string, error) { return rs.NewLoginLink(cfg.Port) }
	in := loginlink.Or(base, mint)
	require.True(t, loginlink.HasToken(in))
	assert.Equal(t, http.StatusFound, signIn(in).StatusCode)

	// A data folder whose command-line token isn't this Coral's: refused at once, nothing
	// is asked (the server can't prove it knows that token).
	other := t.TempDir()
	_, err = cloud.WriteCLIToken(other)
	require.NoError(t, err)
	start := time.Now()
	_, err = loginlink.FromCLI(ctx, base, other)
	assert.ErrorIs(t, err, cloud.ErrNotCoral)
	assert.Less(t, time.Since(start), 5*time.Second, "no retrying against another Coral")

	// No token there yet (Coral still starting): it keeps trying until the deadline.
	short, cancel2 := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel2()
	_, err = loginlink.FromCLI(short, base, t.TempDir())
	assert.Error(t, err)
}
