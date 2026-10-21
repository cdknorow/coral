package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/pkg/tunnel"
)

// #3966: the Coral half of Hub sign-in's wire, against the fake hub.

const (
	ownerAcct = "11111111-2222-4333-8444-555555555555"
	otherAcct = "99999999-2222-4333-8444-555555555555"
	grantID   = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	loopback  = "http://127.0.0.1:8420"
)

// signinHub answers each operation with replies[op] (a JSON body, status 200) and records
// what it was sent.
type signinHub struct {
	mu      sync.Mutex
	replies map[string]string
	status  map[string]int
	got     map[string][]map[string]any
}

func (s *signinHub) set(op, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replies[op] = body
}

func (s *signinHub) sent(op string) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.got[op]
}

func (s *signinHub) answer(req tunnel.SigninRequest) tunnel.SigninResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	var body map[string]any
	_ = json.Unmarshal(req.Body, &body)
	s.got[req.Op] = append(s.got[req.Op], body)
	st := 200
	if c, ok := s.status[req.Op]; ok {
		st = c
	}
	return tunnel.SigninResponse{Status: st, Body: []byte(s.replies[req.Op])}
}

// signinEnv enrolls a manager with a hub that offers sign-in; account is what the enrollment
// reply names ("" names none).
func signinEnv(t *testing.T, account string) (*env, *Manager, *signinHub, *[]SigninEvent) {
	t.Helper()
	e := newEnv(t)
	sh := &signinHub{replies: map[string]string{
		tunnel.SigninOrigins: `{"ok":true,"account":"` + ownerAcct + `"}`,
	}, status: map[string]int{}, got: map[string][]map[string]any{}}
	e.hub.signin.Store(signinFunc(sh.answer))
	if account != "" {
		e.hub.account.Store(account)
	}
	var mu sync.Mutex
	events := &[]SigninEvent{}
	m := New(Options{
		HubURL: e.hub.srv.URL, HTTPClient: e.hub.srv.Client(), Store: e.cs, LocalBaseURL: e.local.srv.URL,
		CoralVersion: "test", PollInterval: 20 * time.Millisecond,
		Backoff:       tunnel.Backoff{Min: 20 * time.Millisecond, Max: 100 * time.Millisecond},
		SigninOrigins: func() []string { return []string{loopback, "http://localhost:8420"} },
		OnSigninEvent: func(_ context.Context, ev SigninEvent) { mu.Lock(); *events = append(*events, ev); mu.Unlock() },
	})
	start(t, m)
	require.NoError(t, m.Connect(context.Background()))
	waitState(t, m, StateConnected)
	e.hub.session(t)
	require.Eventually(t, func() bool { return m.originAccepted(loopback) }, 10*time.Second, 10*time.Millisecond, "the hub accepted the origins")
	return e, m, sh, events
}

func TestSigninEnrollmentPinsTheOwnerAndOriginsAreSent(t *testing.T) {
	e, m, sh, events := signinEnv(t, ownerAcct)
	owner, err := e.cs.Owner(context.Background())
	require.NoError(t, err)
	assert.Equal(t, ownerAcct, owner)
	require.NotEmpty(t, *events)
	assert.Equal(t, SigninEvent{Kind: SigninOwnerPinned, How: "enrollment"}, (*events)[0])
	sent := sh.sent(tunnel.SigninOrigins)
	require.NotEmpty(t, sent)
	assert.Equal(t, []any{loopback, "http://localhost:8420"}, sent[0]["origins"])
	assert.EqualValues(t, 1, sent[0]["v"])
	assert.True(t, m.SigninReady(context.Background(), loopback))
	assert.False(t, m.SigninReady(context.Background(), "http://192.168.1.5:8420"), "not on the accepted list")
}

// An older hub names no owner anywhere: nothing is pinned and sign-in never starts.
func TestSigninWithoutAnOwnerStaysOff(t *testing.T) {
	e, m, sh, _ := signinEnv(t, "")
	sh.set(tunnel.SigninOrigins, `{"ok":true}`)
	// Re-send with the hub naming no account, on a fresh enrollment that pinned none.
	_, err := e.db.Exec("UPDATE cloud_connection SET hub_owner = NULL")
	require.NoError(t, err)
	m.SigninOriginsChanged()
	require.Eventually(t, func() bool { return m.originAccepted(loopback) }, 10*time.Second, 10*time.Millisecond)
	assert.False(t, m.SigninReady(context.Background(), loopback))
	assert.ErrorIs(t, m.CheckSignin(context.Background(), grantID), ErrSigninNoOwner)
	_, err = m.RedeemSignin(context.Background(), strings.Repeat("c", 43), strings.Repeat("v", 43))
	assert.ErrorIs(t, err, ErrSigninNoOwner)
}

// With no account at enrollment, the first origins reply pins one.
func TestSigninOriginsPinTheOwnerOnce(t *testing.T) {
	e, _, _, events := signinEnv(t, "")
	owner, _ := e.cs.Owner(context.Background())
	assert.Equal(t, ownerAcct, owner)
	assert.Equal(t, []SigninEvent{{Kind: SigninOwnerPinned, How: "origins"}}, *events)
}

func TestSigninStart(t *testing.T) {
	_, m, sh, _ := signinEnv(t, ownerAcct)
	ctx := context.Background()
	hubPage := func(host string) string { return "https://" + host + "/signin/coral?flow=" + strings.Repeat("f", 26) }
	host := strings.TrimPrefix(m.o.HubURL, "https://")

	sh.set(tunnel.SigninBegin, `{"ok":true,"flow":"`+strings.Repeat("f", 26)+`","url":"`+hubPage(host)+`","ttl":300}`)
	b, err := m.StartSignin(ctx, "ch", "st", loopback)
	require.NoError(t, err)
	assert.Equal(t, 5*time.Minute, b.TTL)
	got := sh.sent(tunnel.SigninBegin)
	require.Len(t, got, 1)
	assert.Equal(t, map[string]any{"v": float64(1), "challenge": "ch", "state": "st", "return": loopback}, got[0])

	_, err = m.StartSignin(ctx, "ch", "st", "http://evil.example:8420")
	assert.ErrorIs(t, err, ErrSigninRefused, "an origin the hub wasn't told")
	assert.Len(t, sh.sent(tunnel.SigninBegin), 1, "and nothing was sent")

	for name, reply := range map[string]string{
		"refused":       `{"ok":false}`,
		"other host":    `{"ok":true,"flow":"` + strings.Repeat("f", 26) + `","url":"` + hubPage("evil.example") + `","ttl":300}`,
		"http":          `{"ok":true,"flow":"` + strings.Repeat("f", 26) + `","url":"http://` + host + `/x","ttl":300}`,
		"bad flow":      `{"ok":true,"flow":"short","url":"` + hubPage(host) + `","ttl":300}`,
		"ttl too long":  `{"ok":true,"flow":"` + strings.Repeat("f", 26) + `","url":"` + hubPage(host) + `","ttl":3600}`,
		"unknown key":   `{"ok":true,"flow":"` + strings.Repeat("f", 26) + `","url":"` + hubPage(host) + `","ttl":300,"x":1}`,
		"repeated key":  `{"ok":true,"ok":true,"flow":"` + strings.Repeat("f", 26) + `","url":"` + hubPage(host) + `","ttl":300}`,
		"not an object": `[1]`,
	} {
		sh.set(tunnel.SigninBegin, reply)
		_, err := m.StartSignin(ctx, "ch", "st", loopback)
		assert.ErrorIs(t, err, ErrSigninRefused, name)
	}
	sh.set(tunnel.SigninBegin, `{"ok":false,"status":"disabled"}`)
	_, err = m.StartSignin(ctx, "ch", "st", loopback)
	assert.ErrorIs(t, err, ErrSigninDisabled, "the hub's per-machine switch is off")
}

func TestSigninRedeem(t *testing.T) {
	e, m, sh, events := signinEnv(t, ownerAcct)
	ctx := context.Background()
	code, verifier := strings.Repeat("c", 43), strings.Repeat("v", 43)

	sh.set(tunnel.SigninRedeem, `{"ok":true,"grant":"`+grantID+`","account":"`+ownerAcct+`","display":"Ann\n\u202eevil\u0007 `+strings.Repeat("x", 80)+`","tier":"view","ttl":3600}`)
	g, err := m.RedeemSignin(ctx, code, verifier)
	require.NoError(t, err)
	assert.Equal(t, grantID, g.Grant)
	assert.Equal(t, time.Hour, g.TTL)
	assert.NotContains(t, g.Display, "\n")
	assert.NotContains(t, g.Display, "\u202e")
	assert.True(t, strings.HasPrefix(g.Display, "Annevil "), g.Display)
	assert.LessOrEqual(t, len([]rune(g.Display)), 64)

	_, err = m.RedeemSignin(ctx, "short", verifier)
	assert.ErrorIs(t, err, ErrSigninRefused, "a malformed code is never sent")
	assert.Len(t, sh.sent(tunnel.SigninRedeem), 1)

	for name, reply := range map[string]string{
		"refused":     `{"ok":false}`,
		"no grant":    `{"ok":true,"account":"` + ownerAcct + `","display":"a","ttl":60}`,
		"ttl over 8h": `{"ok":true,"grant":"` + grantID + `","account":"` + ownerAcct + `","display":"a","ttl":28801}`,
	} {
		sh.set(tunnel.SigninRedeem, reply)
		_, err := m.RedeemSignin(ctx, code, verifier)
		assert.ErrorIs(t, err, ErrSigninRefused, name)
	}

	// Another account: refused, and the pin is blocked until re-linking.
	sh.set(tunnel.SigninRedeem, `{"ok":true,"grant":"`+grantID+`","account":"`+otherAcct+`","display":"b","ttl":60}`)
	_, err = m.RedeemSignin(ctx, code, verifier)
	assert.ErrorIs(t, err, ErrSigninRefused)
	owner, _ := e.cs.Owner(ctx)
	assert.Equal(t, store.OwnerBlocked, owner)
	assert.Contains(t, *events, SigninEvent{Kind: SigninOwnerMismatch})
	assert.False(t, m.SigninReady(ctx, loopback))
	assert.ErrorIs(t, m.CheckSignin(ctx, grantID), ErrSigninRefused)

	// A new enrollment clears the block.
	require.NoError(t, e.cs.Save(ctx, e.hub.srv.URL, "srv_1", "My Laptop", fakeToken))
	owner, _ = e.cs.Owner(ctx)
	assert.Empty(t, owner)
}

func TestSigninCheck(t *testing.T) {
	_, m, sh, events := signinEnv(t, ownerAcct)
	ctx := context.Background()
	sh.set(tunnel.SigninCheck, `{"ok":true,"status":"ok","account":"`+ownerAcct+`"}`)
	require.NoError(t, m.CheckSignin(ctx, grantID))
	assert.Equal(t, map[string]any{"v": float64(1), "grant": grantID}, sh.sent(tunnel.SigninCheck)[0])

	// Every answer that isn't a well-formed ok for the owner ends the session at once.
	for name, tc := range map[string]struct {
		body   string
		status int
	}{
		"revoked":       {`{"ok":false,"status":"revoked"}`, 200},
		"unknown":       {`{"ok":false,"status":"unknown"}`, 200},
		"disabled":      {`{"ok":false,"status":"disabled"}`, 200},
		"plain refusal": {`{"ok":false}`, 400},
		"5xx with ok":   {`{"ok":true,"status":"ok","account":"` + ownerAcct + `"}`, 503},
		"odd status":    {`{"ok":true,"status":"fine","account":"` + ownerAcct + `"}`, 200},
		"no account":    {`{"ok":true,"status":"ok"}`, 200},
		"malformed":     {`{"ok":true,`, 200},
		"unknown field": {`{"ok":true,"status":"ok","account":"` + ownerAcct + `","admin":true}`, 200},
	} {
		sh.set(tunnel.SigninCheck, tc.body)
		sh.mu.Lock()
		sh.status[tunnel.SigninCheck] = tc.status
		sh.mu.Unlock()
		err := m.CheckSignin(ctx, grantID)
		assert.ErrorIs(t, err, ErrSigninRefused, name)
		assert.False(t, errors.Is(err, ErrSigninUnanswered), name)
	}
	assert.Empty(t, filterMismatch(*events), "no mismatch so far")
	sh.mu.Lock()
	delete(sh.status, tunnel.SigninCheck)
	sh.mu.Unlock()
	sh.set(tunnel.SigninCheck, `{"ok":true,"status":"ok","account":"`+otherAcct+`"}`)
	assert.ErrorIs(t, m.CheckSignin(ctx, grantID), ErrSigninRefused)
	assert.NotEmpty(t, filterMismatch(*events), "another account blocks the pin")
}

func filterMismatch(evs []SigninEvent) []SigninEvent {
	var out []SigninEvent
	for _, ev := range evs {
		if ev.Kind == SigninOwnerMismatch {
			out = append(out, ev)
		}
	}
	return out
}

// No tunnel is no answer: the caller's grace applies, not an immediate end.
func TestSigninUnansweredWithoutATunnel(t *testing.T) {
	e := newEnv(t)
	m := e.manager(e.hub.srv.URL)
	_, _, err := m.signin(context.Background(), tunnel.SigninCheck, map[string]any{"v": 1})
	assert.ErrorIs(t, err, ErrSigninUnanswered)
	assert.False(t, m.SigninReady(context.Background(), loopback))
}

func TestStrictJSON(t *testing.T) {
	var v struct {
		A int `json:"a"`
	}
	assert.NoError(t, strictJSON([]byte(`{"a":1}`), &v))
	for _, bad := range []string{`{"a":1,"a":2}`, `{"a":1,"b":2}`, `{"a":1} {}`, `{"a":1}x`, `[]`, ``, `null`} {
		assert.Error(t, strictJSON([]byte(bad), &v), bad)
	}
}

func TestCleanDisplay(t *testing.T) {
	assert.Equal(t, "Ann Lee", CleanDisplay(" Ann Lee "))
	assert.Equal(t, "AnnLee", CleanDisplay("Ann\r\n\u2028\u202e\u200bLee\x00"))
	assert.Equal(t, 64, len([]rune(CleanDisplay(strings.Repeat("é", 100)))))
	assert.Equal(t, "ab", CleanDisplay("a\xffb"))
}

// The sign-in page's "Open Coral Hub" link: the configured hub only when it is https.
func TestHubPage(t *testing.T) {
	assert.Equal(t, "https://app.coralhub.dev", New(Options{HubURL: "https://app.coralhub.dev"}).HubPage())
	assert.Empty(t, New(Options{HubURL: "http://hub.local:8080"}).HubPage())
	assert.Empty(t, New(Options{}).HubPage())
}

// #3966, coral-hub Security's finding: Coral Hub's busy answer (429, the same {"ok":false} body
// as a refusal) is transient on a check, like no answer, so a relay burst never signs the owner
// out; a 400 with the same body still ends it, and begin and redeem take a 429 as a plain failure.
func TestSigninBusyCheckIsTransient(t *testing.T) {
	_, m, sh, _ := signinEnv(t, ownerAcct)
	ctx := context.Background()
	setStatus := func(op string, code int) {
		sh.mu.Lock()
		sh.status[op] = code
		sh.mu.Unlock()
	}
	sh.set(tunnel.SigninCheck, `{"ok":false}`)
	setStatus(tunnel.SigninCheck, 429)
	err := m.CheckSignin(ctx, grantID)
	assert.ErrorIs(t, err, ErrSigninBusy)
	assert.ErrorIs(t, err, ErrSigninUnanswered, "the grace applies")
	assert.NotErrorIs(t, err, ErrSigninRefused)

	setStatus(tunnel.SigninCheck, 400)
	err = m.CheckSignin(ctx, grantID)
	assert.ErrorIs(t, err, ErrSigninRefused, "the same body with 400 ends the session")
	assert.NotErrorIs(t, err, ErrSigninUnanswered)

	sh.set(tunnel.SigninRedeem, `{"ok":false}`)
	setStatus(tunnel.SigninRedeem, 429)
	_, err = m.RedeemSignin(ctx, strings.Repeat("c", 43), strings.Repeat("v", 43))
	assert.ErrorIs(t, err, ErrSigninRefused, "a busy redeem is a failed sign-in, not a retry")
	sh.set(tunnel.SigninBegin, `{"ok":false}`)
	setStatus(tunnel.SigninBegin, 429)
	_, err = m.StartSignin(ctx, "ch", "st", loopback)
	assert.ErrorIs(t, err, ErrSigninRefused)
}

// The connector's own budget: relay requests filling the relay's slots never hold up a check.
func TestSigninDoesNotWaitForRelaySlots(t *testing.T) {
	_, m, sh, _ := signinEnv(t, ownerAcct)
	sh.set(tunnel.SigninCheck, `{"ok":true,"status":"ok","account":"`+ownerAcct+`"}`)
	for i := 0; i < cap(m.relaySlots); i++ {
		m.relaySlots <- struct{}{}
	}
	defer func() {
		for i := 0; i < cap(m.relaySlots); i++ {
			<-m.relaySlots
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	assert.NoError(t, m.CheckSignin(ctx, grantID))
}
