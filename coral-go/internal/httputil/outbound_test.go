package httputil

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeDNS makes the listed hostnames resolve to the given addresses for one test.
func fakeDNS(t *testing.T, m map[string]string) {
	t.Helper()
	orig := lookupIP
	lookupIP = func(ctx context.Context, host string) ([]net.IP, error) {
		if a, ok := m[host]; ok {
			return []net.IP{net.ParseIP(a)}, nil
		}
		return nil, errors.New("no such host")
	}
	t.Cleanup(func() { lookupIP = orig })
}

func allowPrivate(t *testing.T, v bool) {
	t.Helper()
	SetWebhookAllowPrivate(func() bool { return v })
	t.Cleanup(func() { SetWebhookAllowPrivate(nil) })
}

// #3363: a webhook URL is refused when saved if it points at this machine, the cloud
// metadata address, a private network, or a hostname that resolves to one of them.
func TestValidateWebhookURL(t *testing.T) {
	fakeDNS(t, map[string]string{"rebind.test": "127.0.0.1", "meta.test": "169.254.169.254", "hooks.example.com": "93.184.216.34"})
	ctx := context.Background()
	for _, u := range []string{
		"http://127.0.0.1:8420/api/sessions",
		"http://localhost.:80/",
		"http://[::1]/",
		"http://169.254.169.254/latest/meta-data/",
		"http://10.1.2.3/hook",
		"http://192.168.1.10/hook",
		"http://172.16.0.1/hook",
		"http://0.0.0.0:8420/",
		"http://[::ffff:127.0.0.1]/",
		"http://rebind.test/hook",
		"http://meta.test/hook",
	} {
		err := ValidateWebhookURL(ctx, u)
		if strings.Contains(u, "localhost.") {
			assert.Error(t, err, u) // unresolvable in the fake DNS: refused either way
			continue
		}
		assert.ErrorIs(t, err, ErrWebhookTargetBlocked, u)
	}
	for _, u := range []string{"ftp://hooks.example.com/", "file:///etc/passwd", "http://user:pw@hooks.example.com/", "not a url", "http:///nohost"} {
		assert.Error(t, ValidateWebhookURL(ctx, u), u)
	}
	assert.NoError(t, ValidateWebhookURL(ctx, "https://hooks.example.com/notify"))
	assert.NoError(t, ValidateWebhookURL(ctx, "http://93.184.216.34/notify"))

	allowPrivate(t, true)
	assert.NoError(t, ValidateWebhookURL(ctx, "http://192.168.1.10/hook"), "allowed by the setting")
	assert.Error(t, ValidateWebhookURL(ctx, "ftp://192.168.1.10/"), "the scheme is still checked")
}

func post(c *http.Client, url string) (*http.Response, error) {
	req, _ := http.NewRequest("POST", url, strings.NewReader("{}"))
	return c.Do(req)
}

// The client refuses a blocked address at connect time, including a hostname that
// resolves to one (DNS rebinding between the save-time check and the delivery).
func TestWebhookClientRefusesPrivateTargetsAtDialTime(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	fakeDNS(t, map[string]string{"rebind.test": "127.0.0.1"})
	c := WebhookClient(5 * time.Second)

	_, err := post(c, srv.URL)
	assert.ErrorIs(t, err, ErrWebhookTargetBlocked)
	_, err = post(c, "http://rebind.test:"+port+"/")
	assert.ErrorIs(t, err, ErrWebhookTargetBlocked)
	assert.Zero(t, hits, "nothing reached the local server")

	allowPrivate(t, true)
	resp, err := post(c, srv.URL)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, 1, hits, "allowed by the setting")
}

// A redirect is dialled through the same guard: a public webhook that redirects to the
// cloud metadata address is refused there.
func TestWebhookClientRefusesARedirectToABlockedAddress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	orig := blockedIP
	blockedIP = func(ip net.IP) bool { return !ip.Equal(net.ParseIP("127.0.0.1")) && IsIPBlocked(ip) } // the test server stands in for a public host
	t.Cleanup(func() { blockedIP = orig })

	_, err := post(WebhookClient(5*time.Second), srv.URL)
	assert.ErrorIs(t, err, ErrWebhookTargetBlocked)
}

// An HTTP(S)_PROXY in the environment is not used: it would connect for us, past the check.
func TestWebhookClientIgnoresTheEnvironmentProxy(t *testing.T) {
	tr := WebhookClient(time.Second).Transport.(*http.Transport)
	assert.Nil(t, tr.Proxy)
}
