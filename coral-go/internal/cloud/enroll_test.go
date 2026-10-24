package cloud

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
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
	require.ErrorIs(t, err, ErrHubUnavailable, "a hub server error is transient")
	_, err = run(good, 503)
	require.ErrorIs(t, err, ErrHubUnavailable)
	_, err = StartEnrollment(context.Background(), NewHubHTTPClient(), "https://127.0.0.1:1", time.Now(), EnrollInfo{})
	require.ErrorIs(t, err, ErrHubUnavailable, "unreachable")
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
	_, err = run(400, `{"error":"slow_down"}`)
	require.ErrorIs(t, err, ErrSlowDown, "RFC 8628 sends slow_down as a 400")
	_, err = run(400, `{"error":"access_denied"}`)
	require.ErrorIs(t, err, ErrAccessDenied)
	_, err = run(403, `{"error":"access_denied"}`)
	require.ErrorIs(t, err, ErrAccessDenied)
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

// 429 rate_limited at start: the wait comes from Retry-After (seconds or a date),
// capped, whatever the body is.
func TestStartEnrollmentRateLimited(t *testing.T) {
	now := time.Date(2026, 10, 14, 8, 0, 0, 0, time.UTC)
	run := func(retryAfter, body string) error {
		url, hc := hubWith(t, func(w http.ResponseWriter, r *http.Request) {
			if retryAfter != "" {
				w.Header().Set("Retry-After", retryAfter)
			}
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(body))
		})
		_, err := StartEnrollment(context.Background(), hc, url, now, EnrollInfo{})
		return err
	}
	for ra, want := range map[string]time.Duration{
		"120": 2 * time.Minute, "": 0, "-5": 0, "junk": 0, "86400": maxRetryAfter,
		now.Add(5 * time.Minute).Format(http.TimeFormat): 5 * time.Minute,
	} {
		var rl *RateLimitedError
		require.ErrorAs(t, run(ra, `{"error":"rate_limited"}`), &rl, ra)
		require.Equal(t, want, rl.RetryAfter, ra)
	}
	var rl *RateLimitedError
	require.ErrorAs(t, run("30", "Too Many Requests"), &rl, "a non-JSON body is still rate limiting")
	require.Equal(t, "Coral Hub is busy: try again in 30s", rl.Error())
	require.ErrorAs(t, run("", ""), &rl)
	require.Equal(t, "Coral Hub is busy: try again in a minute", rl.Error())
}

// Every hub call names Coral, its version and platform.
func TestHubCallsSendACoralUserAgent(t *testing.T) {
	var ua string
	url, hc := hubWith(t, func(w http.ResponseWriter, r *http.Request) {
		ua = r.Header.Get("User-Agent")
		w.WriteHeader(400)
		w.Write([]byte(`{"error":"authorization_pending"}`))
	})
	_, err := PollEnrollment(context.Background(), hc, url, "dev")
	require.ErrorIs(t, err, ErrAuthorizationPending)
	require.Regexp(t, `^Coral/\S+ \(`+runtime.GOOS+`/`+runtime.GOARCH+`\)$`, ua)
}

// #7061: what the enrolment reply says the machine is enrolled as. Personal needs no org; team needs the canonical org and keeps no person; anything this code does not understand is refused, never
// read as personal.
func TestPollEnrollmentOwnerKind(t *testing.T) {
	const tok = `"server_token":"coralhub_srv_abcd1234_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx","server_id":"srv_1"`
	const org = "0b9f1c2e-3d4a-4b5c-8d6e-7f8091a2b3c4"
	run := func(extra string) (*Enrolled, error) {
		url, hc := hubWith(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{` + tok + extra + `}`)) })
		return PollEnrollment(context.Background(), hc, url, "dev")
	}
	for name, extra := range map[string]string{
		"no kind":        ``,
		"personal":       `,"owner_kind":"personal"`,
		"personal, user": `,"owner_kind":"personal","owner_user":"hub-user-1234abcd","account":"acct_1"`,
	} {
		en, err := run(extra)
		require.NoError(t, err, name)
		require.Empty(t, en.TeamOrg, name)
	}
	en, err := run(`,"owner_user":"hub-user-1234abcd","owner_kind":"personal"`)
	require.NoError(t, err)
	require.Equal(t, "hub-user-1234abcd", en.OwnerUser, "a personal enrolment still records its owner")

	en, err = run(`,"owner_kind":"team","owner_org":"` + org + `","owner_user":"hub-user-1234abcd","account":"acct_1"`)
	require.NoError(t, err)
	require.Equal(t, org, en.TeamOrg)
	require.Empty(t, en.OwnerUser, "a team's machine has no person owner, whatever the reply carried")
	require.Empty(t, en.Account, "the account that pressed Confirm is not a standing way in")

	for name, extra := range map[string]string{
		"team, no org":         `,"owner_kind":"team"`,
		"team, empty org":      `,"owner_kind":"team","owner_org":""`,
		"team, upper-case org": `,"owner_kind":"team","owner_org":"` + strings.ToUpper(org) + `"`,
		"team, no hyphens":     `,"owner_kind":"team","owner_org":"` + strings.ReplaceAll(org, "-", "") + `"`,
		"team, hub-org id":     `,"owner_kind":"team","owner_org":"hub-org-1234abcd"`,
		"unknown kind":         `,"owner_kind":"organisation","owner_org":"` + org + `"`,
		"another case":         `,"owner_kind":"Team","owner_org":"` + org + `"`,
		"personal, with org":   `,"owner_kind":"personal","owner_org":"` + org + `"`,
		"no kind, with org":    `,"owner_org":"` + org + `"`,
	} {
		_, err := run(extra)
		require.ErrorIs(t, err, ErrHubResponse, name)
	}
}
