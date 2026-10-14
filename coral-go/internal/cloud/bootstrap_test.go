//go:build linux || darwin

package cloud

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/store/storetest"
	terms "github.com/cdknorow/coral/pkg/license"
	"github.com/cdknorow/coral/pkg/secretbox"
)

// fakeEnrollHub answers the redeem endpoints with a scripted list of replies
// and records every request.
type fakeEnrollHub struct {
	srv     *httptest.Server
	mu      sync.Mutex
	replies []func(w http.ResponseWriter)
	got     []recordedRedeem
}

type recordedRedeem struct {
	path, query, auth string
	body              map[string]json.RawMessage
}

func newFakeEnrollHub(t *testing.T) *fakeEnrollHub {
	h := &fakeEnrollHub{}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		h.mu.Lock()
		h.got = append(h.got, recordedRedeem{path: r.URL.Path, query: r.URL.RawQuery, auth: r.Header.Get("Authorization"), body: body})
		var reply func(http.ResponseWriter)
		if len(h.replies) > 0 {
			reply, h.replies = h.replies[0], h.replies[1:]
		}
		h.mu.Unlock()
		if reply == nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		reply(w)
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *fakeEnrollHub) then(replies ...func(http.ResponseWriter)) {
	h.replies = append(h.replies, replies...)
}

func (h *fakeEnrollHub) requests() []recordedRedeem {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]recordedRedeem(nil), h.got...)
}

func status(code int, body string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = io.WriteString(w, body)
	}
}

func (h *fakeEnrollHub) ok() func(http.ResponseWriter) {
	return status(200, `{"identity_id":"i1","server_id":"srv_1","server_token":"coralhub_srv_secret","hub_url":"`+h.srv.URL+`","mode":"view","terms_accepted":"`+terms.EULAVersion+`","state":"active"}`)
}

type bootEnv struct {
	hub     *fakeEnrollHub
	cs      *store.CloudStore
	ss      *store.SessionStore
	sleeps  []time.Duration
	written []string // terms versions recorded
	opts    BootstrapOptions
}

func newBootEnv(t *testing.T) *bootEnv {
	t.Helper()
	t.Setenv(secretbox.EnvKey, "")
	dir := t.TempDir()
	db, err := storetest.Open(t, filepath.Join(dir, "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	e := &bootEnv{hub: newFakeEnrollHub(t), cs: store.NewCloudStore(db, store.NewRemoteServerStore(db, dir)), ss: store.NewSessionStore(db)}
	e.opts = BootstrapOptions{
		HubURL: e.hub.srv.URL, Store: e.cs, Settings: e.ss, HTTPClient: e.hub.srv.Client(), Hostname: "box-1",
		Sleep:       func(_ context.Context, d time.Duration) bool { e.sleeps = append(e.sleeps, d); return true },
		RecordTerms: func(v string) error { e.written = append(e.written, v); return nil },
	}
	return e
}

func (e *bootEnv) run(src CredentialSource) BootstrapResult {
	return Bootstrap(context.Background(), e.opts, src)
}

// A bootstrap credential is redeemed at /api/bootstrap/enroll with the
// credential only in the Authorization header, the binary's terms version, a
// redeem id, the hostname and the proof unchanged; the identity and the
// Hub-confirmed terms are stored; the local cap is not touched.
func TestBootstrapEnrolls(t *testing.T) {
	e := newBootEnv(t)
	e.hub.then(e.hub.ok())
	ticket := credFile(t, goodCred+"\n")
	proofPath := credFile(t, `{"kind":"aws","document":"{}","signature":"c2ln"}`)
	res := e.run(CredentialSource{File: ticket, ProofFile: proofPath})
	require.Equal(t, BootstrapEnrolled, res.Status, res.Detail)
	assert.Equal(t, 0, BootstrapExitCode(res.Status))
	assert.NoFileExists(t, ticket)

	reqs := e.hub.requests()
	require.Len(t, reqs, 1)
	r := reqs[0]
	assert.Equal(t, bootstrapEnrollPath, r.path)
	assert.Empty(t, r.query, "nothing in the URL")
	assert.Equal(t, "Bearer "+goodCred, r.auth)
	assert.JSONEq(t, `"`+terms.EULAVersion+`"`, string(r.body["eula_version"]))
	assert.JSONEq(t, `"box-1"`, string(r.body["hostname"]))
	assert.JSONEq(t, `{"kind":"aws","document":"{}","signature":"c2ln"}`, string(r.body["proof"]))
	var id string
	require.NoError(t, json.Unmarshal(r.body["redeem_id"], &id))
	assert.Len(t, id, 22, "128 random bits, base64url")
	for _, b := range r.body {
		assert.NotContains(t, string(b), goodCred, "the credential is never in the body")
	}

	conn, err := e.cs.Get(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "srv_1", conn.ServerID)
	assert.Equal(t, e.hub.srv.URL, conn.HubURL)
	tok, err := e.cs.Token(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "coralhub_srv_secret", tok)
	assert.Equal(t, []string{terms.EULAVersion}, e.written, "the Hub-confirmed terms are recorded")
	settings, _ := e.ss.GetSettings(context.Background())
	assert.Contains(t, settings[BootstrapStatusSetting], `"status":"enrolled"`)
	_, hasCap := settings["cloud_max_mode"]
	assert.False(t, hasCap, "enrollment never sets the local access cap")
}

// A Coder ticket (cht_) uses the contract's exchange endpoint, with no redeem
// id and no proof (that endpoint refuses unknown fields).
func TestCoderTicketUsesTheExchangeEndpoint(t *testing.T) {
	e := newBootEnv(t)
	e.hub.then(e.hub.ok())
	res := e.run(CredentialSource{Value: []byte("cht_0123456789abcdefXYZ")})
	require.Equal(t, BootstrapEnrolled, res.Status, res.Detail)
	r := e.hub.requests()[0]
	assert.Equal(t, coderExchangePath, r.path)
	assert.NotContains(t, r.body, "redeem_id")
	assert.NotContains(t, r.body, "proof")

	e2 := newBootEnv(t)
	res = e2.run(CredentialSource{Value: []byte("cht_0123456789abcdefXYZ"), Proof: []byte(`{"kind":"gcp","token":"t"}`)})
	assert.Equal(t, BootstrapRefused, res.Status, "a proof goes only with a bootstrap credential")
	assert.Empty(t, e2.hub.requests())
}

// The Hub's answers: 401 is the generic refusal, not retried (exit 2); 409
// terms_reconsent_required is permanent (exit 3); 400 is a Coral bug (exit 1);
// none stores anything.
func TestBootstrapPermanentAnswers(t *testing.T) {
	for _, tc := range []struct {
		reply    func(http.ResponseWriter)
		status   string
		exit     int
		contains string
	}{
		{status(401, `{"error":"unauthorized"}`), BootstrapInvalid, 2, "credential invalid or already used: ask for a new one"},
		{status(409, `{"error":"terms_reconsent_required","version":"x"}`), BootstrapTermsReconsent, 3, "terms"},
		{status(400, `{"error":"bad_request"}`), BootstrapRefused, 1, "Coral bug"},
	} {
		e := newBootEnv(t)
		e.hub.then(tc.reply, e.hub.ok())
		res := e.run(CredentialSource{Value: []byte(goodCred)})
		assert.Equal(t, tc.status, res.Status)
		assert.Equal(t, tc.exit, BootstrapExitCode(res.Status))
		assert.Contains(t, res.Detail, tc.contains)
		assert.Len(t, e.hub.requests(), 1, "%s: not retried", tc.status)
		_, err := e.cs.Get(context.Background())
		assert.ErrorIs(t, err, store.ErrNoCloudConnection, "nothing stored")
		assert.Empty(t, e.written)
	}
}

// An unusable 200 stores nothing: another hub's identity, terms for another
// version, a mode other than view, a malformed token.
func TestBootstrapRefusesUnusableIdentity(t *testing.T) {
	for name, body := range map[string]string{
		"another hub":   `{"server_id":"srv_1","server_token":"coralhub_srv_x","hub_url":"https://evil.example","mode":"view","terms_accepted":"` + terms.EULAVersion + `"}`,
		"other terms":   `{"server_id":"srv_1","server_token":"coralhub_srv_x","hub_url":"HUB","mode":"view","terms_accepted":"2020-01-01"}`,
		"no mode":       `{"server_id":"srv_1","server_token":"coralhub_srv_x","hub_url":"HUB","terms_accepted":"` + terms.EULAVersion + `"}`,
		"not view":      `{"server_id":"srv_1","server_token":"coralhub_srv_x","hub_url":"HUB","mode":"launch","terms_accepted":"` + terms.EULAVersion + `"}`,
		"bad token":     `{"server_id":"srv_1","server_token":"nope","hub_url":"HUB","mode":"view","terms_accepted":"` + terms.EULAVersion + `"}`,
		"bad server id": `{"server_id":"a b","server_token":"coralhub_srv_x","hub_url":"HUB","mode":"view","terms_accepted":"` + terms.EULAVersion + `"}`,
	} {
		e := newBootEnv(t)
		e.hub.then(status(200, strings.ReplaceAll(body, "HUB", e.hub.srv.URL)))
		res := e.run(CredentialSource{Value: []byte(goodCred)})
		assert.Equal(t, BootstrapRefused, res.Status, name)
		_, err := e.cs.Get(context.Background())
		assert.ErrorIs(t, err, store.ErrNoCloudConnection, name)
		assert.Empty(t, e.written, name)
	}
}

// 429 honours Retry-After; 5xx and a lost response are retried with backoff;
// every retry of the series reuses the SAME redeem id (the Hub then returns the
// same identity), and the identity is stored only from the reply that succeeds.
func TestBootstrapRetriesWithOneRedeemID(t *testing.T) {
	e := newBootEnv(t)
	lost := func(w http.ResponseWriter) { // the connection drops before an answer
		hj, _ := w.(http.Hijacker)
		conn, _, _ := hj.Hijack()
		conn.Close()
	}
	e.hub.then(status(429, `{"error":"rate_limited"}`), status(503, ``), lost, e.hub.ok())
	// Retry-After is set on the 429 by the handler below.
	e.hub.replies[0] = func(w http.ResponseWriter) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
	}
	res := e.run(CredentialSource{Value: []byte(goodCred)})
	require.Equal(t, BootstrapEnrolled, res.Status, res.Detail)
	reqs := e.hub.requests()
	require.Len(t, reqs, 4)
	for _, r := range reqs[1:] {
		assert.Equal(t, string(reqs[0].body["redeem_id"]), string(r.body["redeem_id"]), "one redeem id for the series")
		assert.Equal(t, "Bearer "+goodCred, r.auth)
	}
	assert.Equal(t, []time.Duration{7 * time.Second, 2 * time.Second, 4 * time.Second}, e.sleeps)

	// Exhausted: exit 4, nothing stored.
	e2 := newBootEnv(t)
	e2.opts.Attempts = 3
	res = e2.run(CredentialSource{Value: []byte(goodCred)}) // the fake hub answers 500 when it runs out
	assert.Equal(t, BootstrapRetryLater, res.Status)
	assert.Equal(t, 4, BootstrapExitCode(res.Status))
	assert.Len(t, e2.hub.requests(), 3)
	_, err := e2.cs.Get(context.Background())
	assert.ErrorIs(t, err, store.ErrNoCloudConnection)
}

// A machine that already has an identity never calls the Hub (exit 5): a
// named file is removed only if it holds a credential, so a wrong path never
// deletes anything else; a bad file or a bad proof is refused before any
// network call (exit 2); with no hub configured nothing is read.
func TestBootstrapLocalRefusalsMakeNoHubCall(t *testing.T) {
	t.Run("already enrolled", func(t *testing.T) {
		e := newBootEnv(t)
		require.NoError(t, e.cs.Save(context.Background(), e.hub.srv.URL, "srv_0", "x", "coralhub_srv_old"))
		ticket := credFile(t, goodCred)
		res := e.run(CredentialSource{File: ticket})
		assert.Equal(t, BootstrapIgnoredIdentity, res.Status)
		assert.Equal(t, 5, BootstrapExitCode(res.Status))
		assert.NoFileExists(t, ticket, "a credential is removed")
		assert.Empty(t, e.hub.requests())
		settings, _ := e.ss.GetSettings(context.Background())
		assert.Contains(t, settings[BootstrapStatusSetting], BootstrapIgnoredIdentity)

		key := credFile(t, "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA\n")
		res = e.run(CredentialSource{File: key})
		assert.Equal(t, BootstrapIgnoredIdentity, res.Status)
		assert.Contains(t, res.Detail, "left in place")
		assert.FileExists(t, key, "a file that is not a credential is never removed")

		res = e.run(CredentialSource{Value: []byte(goodCred)})
		assert.Equal(t, BootstrapIgnoredIdentity, res.Status, "a handed-over credential is not sent either")
		assert.Empty(t, e.hub.requests())
	})
	t.Run("unsafe file", func(t *testing.T) {
		e := newBootEnv(t)
		ticket := credFile(t, goodCred)
		require.NoError(t, os.Chmod(ticket, 0o644))
		res := e.run(CredentialSource{File: ticket})
		assert.Equal(t, BootstrapUnsafeFile, res.Status)
		assert.Equal(t, 2, BootstrapExitCode(res.Status))
		assert.Empty(t, e.hub.requests())
	})
	t.Run("bad proof", func(t *testing.T) {
		e := newBootEnv(t)
		res := e.run(CredentialSource{Value: []byte(goodCred), Proof: []byte(`{"kind":"aws","document":"x"}`)})
		assert.Equal(t, BootstrapUnsafeFile, res.Status)
		assert.Empty(t, e.hub.requests())
	})
	t.Run("not a credential", func(t *testing.T) {
		e := newBootEnv(t)
		res := e.run(CredentialSource{Value: []byte("hello")})
		assert.Equal(t, BootstrapUnsafeFile, res.Status)
		assert.Empty(t, e.hub.requests())
	})
	t.Run("no hub configured", func(t *testing.T) {
		e := newBootEnv(t)
		e.opts.HubURL = ""
		ticket := credFile(t, goodCred)
		res := e.run(CredentialSource{File: ticket})
		assert.Equal(t, BootstrapNoHub, res.Status)
		assert.FileExists(t, ticket, "nothing read")
	})
	t.Run("nothing offered", func(t *testing.T) {
		e := newBootEnv(t)
		assert.Equal(t, BootstrapNone, e.run(CredentialSource{}).Status)
		settings, _ := e.ss.GetSettings(context.Background())
		assert.Empty(t, settings[BootstrapStatusSetting])
	})
}

func TestValidateProof(t *testing.T) {
	for _, ok := range []string{
		`{"kind":"aws","document":"{\"a\":1}","signature":"c2ln"}`,
		`{"kind":"gcp","token":"jwt"}`,
		`{"kind":"kubernetes","token":"jwt"}`,
	} {
		assert.NoError(t, ValidateProof([]byte(ok)), ok)
	}
	for _, bad := range []string{
		``, `[]`, `"x"`, `{}`, `{"kind":"coder","token":"x"}`, `{"kind":"k8s","token":"x"}`,
		`{"kind":"gcp"}`, `{"kind":"gcp","token":""}`, `{"kind":"gcp","token":1}`,
		`{"kind":"gcp","token":"x","extra":"y"}`, `{"kind":"aws","document":"x"}`,
		`{"kind":"gcp","token":"x"}{"kind":"gcp","token":"x"}`,
		`{"kind":"azure","signature":"cms","nonce":"chb_pref"}`, // blocked for now
		`{"kind":"gcp","token":"` + strings.Repeat("a", 16<<10) + `"}`,
		"{\"kind\":\"gcp\",\"token\":\"\xff\"}",
	} {
		assert.Error(t, ValidateProof([]byte(bad)), bad)
	}
}

func TestParseCredential(t *testing.T) {
	for _, ok := range []string{goodCred, "cht_0123456789abcdefXYZ", "  " + goodCred + "\n"} {
		_, err := ParseCredential([]byte(ok))
		assert.NoError(t, err, ok)
	}
	for _, bad := range []string{"", "chb_short", "chx_0123456789abcdefXYZ", "chb_0123456789abcdef XYZ", "Bearer " + goodCred, "chb_" + strings.Repeat("a", 4100)} {
		_, err := ParseCredential([]byte(bad))
		assert.Error(t, err, bad)
	}
}

// A credential handed to the running server (coral cloud connect) enrolls it
// and starts the tunnel at once, with no restart.
func TestManagerRedeemConnects(t *testing.T) {
	e := newBootEnv(t)
	e.hub.then(e.hub.ok())
	m := New(Options{HubURL: e.hub.srv.URL, Store: e.cs, HTTPClient: e.hub.srv.Client(), LocalBaseURL: "http://127.0.0.1:1",
		RecordTerms: func(string) error { return nil }})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	require.Eventually(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.root != nil }, 2*time.Second, 5*time.Millisecond)

	res := m.Redeem(context.Background(), e.ss, "box-1", CredentialSource{Value: []byte(goodCred)})
	require.Equal(t, BootstrapEnrolled, res.Status, res.Detail)
	st := m.Status()
	assert.Equal(t, "srv_1", st.ServerID)
	assert.NotEqual(t, StateIdle, st.State, "the tunnel was started")
}

// A 401 after an attempt that may have reached the Hub (a lost answer) says
// the credential is no longer usable here, not that it was bad; still exit 2.
// A 401 after a 429 (which never uses it) is the plain refusal.
func TestBootstrap401AfterALostAnswer(t *testing.T) {
	e := newBootEnv(t)
	e.hub.then(status(503, ``), status(401, `{"error":"unauthorized"}`))
	res := e.run(CredentialSource{Value: []byte(goodCred)})
	assert.Equal(t, BootstrapInvalid, res.Status)
	assert.Equal(t, 2, BootstrapExitCode(res.Status))
	assert.Equal(t, "the credential is no longer usable here: ask the automation for a new one", res.Detail)

	e2 := newBootEnv(t)
	e2.hub.then(status(429, ``), status(401, `{"error":"unauthorized"}`))
	res = e2.run(CredentialSource{Value: []byte(goodCred)})
	assert.Contains(t, res.Detail, "credential not valid")
}

func TestAzureProofIsBlocked(t *testing.T) {
	err := ValidateProof([]byte(`{"kind":"azure","signature":"cms","nonce":"chb_pref"}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "azure adapter is not available yet")
}

// PreCheck (coral cloud connect asks it before reading anything) answers from
// local state only: ready, already enrolled, or no Hub configured.
func TestPreCheck(t *testing.T) {
	e := newBootEnv(t)
	ctx := context.Background()
	assert.Equal(t, BootstrapNone, PreCheck(ctx, e.hub.srv.URL, e.cs).Status)
	assert.Equal(t, BootstrapNoHub, PreCheck(ctx, "", e.cs).Status)
	require.NoError(t, e.cs.Save(ctx, e.hub.srv.URL, "srv_0", "x", "coralhub_srv_old"))
	assert.Equal(t, BootstrapIgnoredIdentity, PreCheck(ctx, e.hub.srv.URL, e.cs).Status)
	assert.Empty(t, e.hub.requests())
	assert.Equal(t, 0, BootstrapExitCode(BootstrapReady))
}
