package cloud

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNormalizeHubURLCommon(t *testing.T) {
	for in, want := range map[string]string{
		"https://hub.example.com":      "https://hub.example.com",
		"https://hub.example.com/":     "https://hub.example.com",
		" https://hub.example.com ":    "https://hub.example.com",
		"https://hub.example.com:8443": "https://hub.example.com:8443",
	} {
		got, err := NormalizeHubURL(in)
		require.NoError(t, err, in)
		require.Equal(t, want, got)
	}
	for _, bad := range []string{
		"", "hub.example.com", "ftp://hub.example.com", "ws://hub.example.com", "wss://hub.example.com",
		"https://hub.example.com/path", "https://hub.example.com/?x=1", "https://hub.example.com/#f",
		"https://user:pw@hub.example.com", "http://hub.example.com", "http://10.0.0.1:8450", "https://",
	} {
		_, err := NormalizeHubURL(bad)
		require.Error(t, err, "accepted %q", bad)
	}
}

func TestTunnelURL(t *testing.T) {
	require.Equal(t, "wss://hub.example.com:8443/tunnel", TunnelURL("https://hub.example.com:8443"))
	require.Equal(t, "ws://127.0.0.1:8450/tunnel", TunnelURL("http://127.0.0.1:8450"))
}

func TestVerificationURLMustStayOnTheHub(t *testing.T) {
	hub := "https://hub.example.com"
	require.Equal(t, hub+"/enroll?code=ABCD-EFGH", safeVerificationURL(hub, hub+"/enroll?code=ABCD-EFGH"))
	for _, evil := range []string{"https://evil.example/enroll", "http://hub.example.com/enroll", "javascript:alert(1)",
		"https://hub.example.com@evil.example/", "https://user@hub.example.com/enroll", "//evil.example", ""} {
		require.Equal(t, hub+"/enroll", safeVerificationURL(hub, evil), evil)
	}
}

func hubWith(t *testing.T, h http.HandlerFunc) (string, *http.Client) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL, NewHubHTTPClient()
}

func TestStartEnrollmentValidatesHubAnswers(t *testing.T) {
	good := map[string]any{"device_code": strings.Repeat("d", 43), "user_code": "ABCD-EFGH", "verification_url": "x", "interval": 5, "expires_in": 600}
	run := func(body map[string]any, status int) (*DeviceCode, error) {
		url, hc := hubWith(t, func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "/api/enroll/start", r.URL.Path)
			w.WriteHeader(status)
			json.NewEncoder(w).Encode(body)
		})
		return StartEnrollment(context.Background(), hc, url, time.Unix(0, 0), EnrollInfo{})
	}
	dc, err := run(good, 200)
	require.NoError(t, err)
	require.Equal(t, 5*time.Second, dc.Interval)
	require.Equal(t, time.Unix(600, 0), dc.ExpiresAt)

	withInterval := func(i, e int) map[string]any {
		m := map[string]any{}
		for k, v := range good {
			m[k] = v
		}
		m["interval"], m["expires_in"] = i, e
		return m
	}
	dc, err = run(withInterval(0, 999999), 200)
	require.NoError(t, err)
	require.Equal(t, 2*time.Second, dc.Interval, "a hub cannot make the connector poll faster than every 2s")
	require.Equal(t, time.Unix(900, 0), dc.ExpiresAt, "nor wait longer than 15 minutes")

	for name, body := range map[string]map[string]any{
		"short device code": {"device_code": "short", "user_code": "ABCD-EFGH"},
		"bad user code":     {"device_code": strings.Repeat("d", 43), "user_code": "<script>"},
		"empty":             {},
	} {
		_, err := run(body, 200)
		require.ErrorIs(t, err, ErrHubResponse, name)
	}
	_, err = run(good, 500)
	require.ErrorIs(t, err, ErrHubResponse)
}

func TestPollEnrollmentAnswers(t *testing.T) {
	run := func(status int, body string) (*Enrolled, error) {
		url, hc := hubWith(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			w.Write([]byte(body))
		})
		return PollEnrollment(context.Background(), hc, url, "dev")
	}
	_, err := run(400, `{"error":"authorization_pending"}`)
	require.ErrorIs(t, err, ErrAuthorizationPending)
	_, err = run(400, `{"error":"expired_token"}`)
	require.ErrorIs(t, err, ErrEnrollmentExpired)
	_, err = run(429, `{"error":"slow_down"}`)
	require.ErrorIs(t, err, ErrSlowDown)
	_, err = run(400, `{"error":"something_else"}`)
	require.ErrorIs(t, err, ErrHubResponse)

	en, err := run(200, `{"server_token":"coralhub_srv_abcd1234_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx","server_id":"srv_1","label":"Lap\u0000top\n"}`)
	require.NoError(t, err)
	require.Equal(t, "srv_1", en.ServerID)
	require.Equal(t, "Laptop", en.Label, "control characters are stripped from hub-supplied labels")

	for name, body := range map[string]string{
		"wrong token prefix": `{"server_token":"evil_abc","server_id":"srv_1"}`,
		"bad server id":      `{"server_token":"coralhub_srv_abcd1234_x","server_id":"../etc"}`,
		"huge token":         `{"server_token":"coralhub_srv_` + strings.Repeat("a", 300) + `","server_id":"srv_1"}`,
	} {
		_, err := run(200, body)
		require.ErrorIs(t, err, ErrHubResponse, name)
	}
}

func TestEnrollmentDoesNotFollowRedirectsOrLeakErrors(t *testing.T) {
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("redirect followed") }))
	defer evil.Close()
	url, hc := hubWith(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, evil.URL, 307) })
	_, err := StartEnrollment(context.Background(), hc, url, time.Now(), EnrollInfo{})
	require.ErrorIs(t, err, ErrHubResponse)

	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	_, err = StartEnrollment(context.Background(), NewHubHTTPClient(), deadURL, time.Now(), EnrollInfo{})
	require.Error(t, err)
	require.NotContains(t, err.Error(), deadURL, "errors must not echo the hub address or transport details")
}

func TestHubResponsesAreSizeLimited(t *testing.T) {
	url, hc := hubWith(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"device_code":"` + strings.Repeat("a", 1<<20) + `"}`))
	})
	_, err := StartEnrollment(context.Background(), hc, url, time.Now(), EnrollInfo{})
	require.ErrorIs(t, err, ErrHubResponse)
}

func TestStartEnrollmentReportsACleanedHostnameAndVersion(t *testing.T) {
	var got EnrollInfo
	url, hc := hubWith(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		json.NewEncoder(w).Encode(map[string]any{"device_code": strings.Repeat("d", 43), "user_code": "ABCD-EFGH", "interval": 5, "expires_in": 600})
	})
	_, err := StartEnrollment(context.Background(), hc, url, time.Now(),
		EnrollInfo{Hostname: "Dev\u202eBox\n\x00" + strings.Repeat("x", 200), CoralVersion: "1.2.3\u200b\r"})
	require.NoError(t, err)
	require.NotContains(t, got.Hostname, "\u202e", "bidi override stripped")
	require.NotContains(t, got.Hostname, "\n")
	require.LessOrEqual(t, len([]rune(got.Hostname)), 64)
	require.Equal(t, "1.2.3", got.CoralVersion)
}
