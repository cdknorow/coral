package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/cloud"
)

// fakeCoral is the running server as `coral cloud` sees it: it wrote its
// command-line token in a data folder (CORAL_DATA_DIR), answers the hello with
// the real cloud.CLIAuth, refuses a request that does not prove the token, and
// answers the routes from its fields. Every request is recorded.
type fakeCoral struct {
	srv  *httptest.Server
	auth *cloud.CLIAuth

	mu          sync.Mutex
	checkReply  string
	redeemReply string
	checks      int
	refused     int
	redeems     []fakeRedeem
	calls       []string // "METHOD path body"
	// the enrollment, for status/connect/cancel: each status read pops statuses[0]
	// (the last one stays).
	statuses []string
	// connectFail, if set, is the HTTP status /api/cloud/connect answers (with an error body).
	connectFail       int
	connectRetryAfter string
	connects          int
	onStatus          func() // test hook, called on every status read
}

type fakeRedeem struct {
	auth string
	body map[string]json.RawMessage
}

func newFakeCoral(t *testing.T, checkReply, redeemReply string) *fakeCoral {
	dataDir := t.TempDir()
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		require.NoError(t, os.Chmod(dataDir, 0o700))
	}
	t.Setenv("CORAL_DATA_DIR", dataDir)
	tok, err := cloud.WriteCLIToken(dataDir)
	require.NoError(t, err)
	f := &fakeCoral{auth: cloud.NewCLIAuth(tok), checkReply: checkReply, redeemReply: redeemReply}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeCoral) serve(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var body map[string]json.RawMessage
	_ = json.Unmarshal(b, &body)
	if r.URL.Path == cloud.CLIHelloPath {
		var h struct {
			Nonce string `json:"nonce"`
		}
		_ = json.Unmarshal(b, &h)
		f.mu.Lock()
		auth := f.auth
		f.mu.Unlock()
		sn, proof, err := auth.Hello(h.Nonce)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"nonce": sn, "proof": proof})
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.auth.Verify(r, b) {
		f.refused++
		w.WriteHeader(http.StatusForbidden)
		return
	}
	f.calls = append(f.calls, r.Method+" "+r.URL.Path+" "+strings.TrimSpace(string(b)))
	switch r.URL.Path {
	case "/api/cloud/redeem":
		if string(body["check"]) == "true" {
			f.checks++
			_, _ = io.WriteString(w, f.checkReply)
			return
		}
		f.redeems = append(f.redeems, fakeRedeem{auth: r.Header.Get("Authorization"), body: body})
		_, _ = io.WriteString(w, f.redeemReply)
	case "/api/cloud/status":
		if f.onStatus != nil {
			f.mu.Unlock()
			f.onStatus()
			f.mu.Lock()
		}
		_, _ = io.WriteString(w, f.statuses[0])
		if len(f.statuses) > 1 {
			f.statuses = f.statuses[1:]
		}
	case "/api/cloud/connect":
		f.connects++
		if f.connectFail != 0 {
			if f.connectRetryAfter != "" {
				w.Header().Set("Retry-After", f.connectRetryAfter)
			}
			w.WriteHeader(f.connectFail)
			_, _ = io.WriteString(w, `{"error":"Could not start enrollment: Coral Hub could not be reached or had an error; try again in a minute"}`)
			return
		}
		_, _ = io.WriteString(w, enrolling)
	case "/api/cloud/connect/cancel":
		_, _ = io.WriteString(w, idle)
	default:
		_, _ = io.WriteString(w, `{}`)
	}
}

func (f *fakeCoral) callList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

const (
	testCred = "chb_0123456789abcdefABCDEF_-xyz"
	ready    = `{"status":"ready","exit_code":0}`
	enrolled = `{"status":"enrolled","detail":"enrolled as server srv_1","exit_code":0}`

	notConfigured = `{"configured":false,"state":"off","max_mode":"view"}`
	idle          = `{"configured":true,"hub_url":"https://app.coralhub.dev","state":"idle","max_mode":"view"}`
	enrolling     = `{"configured":true,"hub_url":"https://app.coralhub.dev","state":"enrolling","user_code":"ABCD-EFGH","verification_url":"https://app.coralhub.dev/enroll?code=ABCD-EFGH","max_mode":"view"}`
	expired       = `{"configured":true,"hub_url":"https://app.coralhub.dev","state":"idle","last_error":"The code expired before it was approved. If you couldn't sign in at app.coralhub.dev, Coral Hub is in closed beta: request access at https://coralhub.dev/hub.html","last_error_code":"code_expired","max_mode":"view"}`
	connected     = `{"configured":true,"hub_url":"https://app.coralhub.dev","state":"connected","label":"build-box","max_mode":"view"}`
)

func secretFile(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o700))
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	return p
}

func unixOnly(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("credential files are read on Linux and macOS")
	}
}

func fastPoll(t *testing.T) {
	old := connectPoll
	connectPoll = 5 * time.Millisecond
	t.Cleanup(func() { connectPoll = old })
}

// The CLI reads the file itself (as the user who runs it) and sends the
// credential only in the Authorization header and the proof's contents inline:
// no path ever reaches the server. The file is removed once the Hub answered.
func TestCloudConnectTicketFile(t *testing.T) {
	unixOnly(t)
	f := newFakeCoral(t, ready, enrolled)
	ticket := secretFile(t, "ticket", testCred+"\n")
	proof := secretFile(t, "proof.json", `{"kind":"gcp","token":"x"}`)
	var out, errb bytes.Buffer
	code := runCloud([]string{"connect", "--ticket-file", ticket, "--proof-file", proof, "--url", f.srv.URL}, nil, &out, &errb)
	assert.Equal(t, 0, code, errb.String())
	require.Len(t, f.redeems, 1)
	assert.Equal(t, "Bearer "+testCred, f.redeems[0].auth)
	assert.JSONEq(t, `{"kind":"gcp","token":"x"}`, string(f.redeems[0].body["proof"]))
	assert.NotContains(t, f.redeems[0].body, "ticket_file")
	assert.NotContains(t, f.redeems[0].body, "proof_file")
	assert.Contains(t, out.String(), "enrolled")
	assert.Equal(t, 1, f.checks)
	assert.Zero(t, f.refused, "the check and the redeem both prove the token")
	assert.NoFileExists(t, ticket, "removed once the Hub answered")
	assert.FileExists(t, proof, "a proof is not removed")

	f2 := newFakeCoral(t, ready, `{"status":"credential_invalid","detail":"credential not valid","exit_code":2}`)
	ticket = secretFile(t, "ticket", testCred)
	errb.Reset()
	code = runCloud([]string{"connect", "--ticket-file", ticket, "--url", f2.srv.URL}, nil, &out, &errb)
	assert.Equal(t, 2, code)
	assert.Contains(t, errb.String(), "credential_invalid")
	assert.NoFileExists(t, ticket, "a credential the Hub refused is removed")
}

// #3149 review, note 1: exit 4 means "try again", so the file stays until the
// Hub has answered: kept when it could not be reached, removed after success.
func TestCloudConnectKeepsTheFileUntilTheHubAnswers(t *testing.T) {
	unixOnly(t)
	f := newFakeCoral(t, ready, `{"status":"retry_later","detail":"the Coral Hub could not be reached","exit_code":4}`)
	ticket := secretFile(t, "ticket", testCred)
	var out, errb bytes.Buffer
	assert.Equal(t, 4, runCloud([]string{"connect", "--ticket-file", ticket, "--url", f.srv.URL}, nil, &out, &errb))
	assert.FileExists(t, ticket, "kept for the retry")
	assert.Contains(t, errb.String(), "left in place")

	f.mu.Lock()
	f.redeemReply = enrolled
	f.mu.Unlock()
	assert.Equal(t, 0, runCloud([]string{"connect", "--ticket-file", ticket, "--url", f.srv.URL}, nil, &out, &errb), errb.String())
	assert.NoFileExists(t, ticket)
	assert.Len(t, f.redeems, 2, "the same credential, retried")
}

// #3149 review, blocker B: a program squatting on the Coral port (it answers
// every request {"status":"ready"}) must get neither the token nor the
// credential, and the credential file must not even be read.
func TestCloudConnectSquatterGetsNothing(t *testing.T) {
	unixOnly(t)
	dataDir := t.TempDir()
	require.NoError(t, os.Chmod(dataDir, 0o700))
	t.Setenv("CORAL_DATA_DIR", dataDir)
	tok, err := cloud.WriteCLIToken(dataDir)
	require.NoError(t, err)
	var mu sync.Mutex
	var seen bytes.Buffer
	squatter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		_ = r.Header.Write(&seen)
		_, _ = io.Copy(&seen, r.Body)
		mu.Unlock()
		_, _ = io.WriteString(w, `{"status":"ready","exit_code":0,"detail":"","nonce":"AAAA","proof":"AAAA"}`)
	}))
	defer squatter.Close()

	ticket := secretFile(t, "ticket", testCred)
	before, err := os.Stat(ticket)
	require.NoError(t, err)
	var out, errb bytes.Buffer
	for _, args := range [][]string{
		{"connect", "--ticket-file", ticket, "--url", squatter.URL},
		{"connect", "--url", squatter.URL},
		{"status", "--url", squatter.URL},
		{"disconnect", "--url", squatter.URL},
	} {
		errb.Reset()
		assert.Equal(t, 4, runCloud(args, strings.NewReader(""), &out, &errb), args)
		assert.Contains(t, errb.String(), "Nothing was sent", args)
	}
	mu.Lock()
	got := seen.String()
	mu.Unlock()
	assert.NotContains(t, got, tok, "never the token")
	assert.NotContains(t, got, testCred, "never the credential")
	assert.NotContains(t, got, "Authorization")
	assert.NotContains(t, got, cloud.CLIAuthHeader, "nothing past the hello")
	after, err := os.Stat(ticket)
	require.NoError(t, err)
	assert.Equal(t, before.ModTime(), after.ModTime())
	assert.FileExists(t, ticket, "not read, not removed")
}

// Nothing is read when the server says a bootstrap cannot proceed (already
// enrolled, no Hub configured): the file stays for its owner, exit as told.
func TestCloudConnectReadsNothingUnlessReady(t *testing.T) {
	unixOnly(t)
	for _, tc := range []struct {
		check string
		code  int
	}{
		{`{"status":"ignored_existing_identity","detail":"this machine is already enrolled","exit_code":5}`, 5},
		{`{"status":"no_hub_configured","detail":"no Coral Hub is configured","exit_code":2}`, 2},
	} {
		f := newFakeCoral(t, tc.check, enrolled)
		ticket := secretFile(t, "ticket", testCred)
		stdin := strings.NewReader(testCred)
		var out, errb bytes.Buffer
		assert.Equal(t, tc.code, runCloud([]string{"connect", "--ticket-file", ticket, "--url", f.srv.URL}, nil, &out, &errb))
		assert.Contains(t, errb.String(), "nothing was read")
		assert.FileExists(t, ticket)
		assert.Equal(t, tc.code, runCloud([]string{"connect", "--ticket-file", "-", "--url", f.srv.URL}, stdin, &out, &errb))
		assert.Equal(t, len(testCred), stdin.Len(), "stdin not read")
		assert.Empty(t, f.redeems)
	}
}

// A file that is not a credential, or a bad proof, is refused locally: no
// redeem request, and the files are left as they were.
func TestCloudConnectLocalRefusals(t *testing.T) {
	unixOnly(t)
	f := newFakeCoral(t, ready, enrolled)
	var out, errb bytes.Buffer
	key := secretFile(t, "id_ed25519", "-----BEGIN OPENSSH PRIVATE KEY-----\n")
	assert.Equal(t, 2, runCloud([]string{"connect", "--ticket-file", key, "--url", f.srv.URL}, nil, &out, &errb))
	assert.FileExists(t, key, "not a credential: never removed")

	ticket := secretFile(t, "ticket", testCred)
	badProof := secretFile(t, "proof.json", `{"kind":"aws","document":"x"}`)
	assert.Equal(t, 2, runCloud([]string{"connect", "--ticket-file", ticket, "--proof-file", badProof, "--url", f.srv.URL}, nil, &out, &errb))
	assert.FileExists(t, ticket, "a bad proof leaves the credential on disk")

	open := secretFile(t, "ticket", testCred)
	require.NoError(t, os.Chmod(open, 0o644))
	assert.Equal(t, 2, runCloud([]string{"connect", "--ticket-file", open, "--url", f.srv.URL}, nil, &out, &errb))
	assert.FileExists(t, open)
	assert.Empty(t, f.redeems)
}

// '-' reads one credential from stdin and sends it in the Authorization
// header, never in a body or URL.
func TestCloudConnectStdin(t *testing.T) {
	f := newFakeCoral(t, ready, enrolled)
	var out, errb bytes.Buffer
	code := runCloud([]string{"connect", "--ticket-file", "-", "--url", f.srv.URL}, strings.NewReader(testCred+"\n"), &out, &errb)
	assert.Equal(t, 0, code, errb.String())
	require.Len(t, f.redeems, 1)
	assert.Equal(t, "Bearer "+testCred, f.redeems[0].auth)

	code = runCloud([]string{"connect", "--ticket-file", "-", "--url", f.srv.URL}, strings.NewReader("not a credential"), &out, &errb)
	assert.Equal(t, 2, code)
	assert.Len(t, f.redeems, 1)
}

// Never from argv; only to a Coral on this computer; nothing is read when
// Coral is not running.
func TestCloudConnectRefusals(t *testing.T) {
	t.Setenv("CORAL_DATA_DIR", t.TempDir())
	var out, errb bytes.Buffer
	assert.Equal(t, 2, runCloud([]string{"connect", testCred}, nil, &out, &errb), "a credential on the command line")
	assert.Contains(t, errb.String(), "never taken from the command line")
	assert.Equal(t, 2, runCloud([]string{"login"}, nil, &out, &errb))
	assert.Equal(t, 2, runCloud([]string{"connect", "--proof-file", "p"}, nil, &out, &errb), "--proof-file needs --ticket-file")

	for _, u := range []string{"http://coral.example.com:8420", "http://192.168.1.20:8420", "ftp://localhost", "http://user@localhost:8420"} {
		stdin := strings.NewReader(testCred)
		errb.Reset()
		assert.Equal(t, 2, runCloud([]string{"connect", "--ticket-file", "-", "--url", u}, stdin, &out, &errb), u)
		assert.Contains(t, errb.String(), "not this computer", u)
		assert.Equal(t, len(testCred), stdin.Len(), u)
	}
	assert.True(t, loopbackURL("http://localhost:8420"))
	assert.True(t, loopbackURL("http://[::1]:8420"))
	assert.True(t, loopbackURL("http://127.0.0.1:8420"))

	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		require.NoError(t, os.Chmod(os.Getenv("CORAL_DATA_DIR"), 0o700))
	}
	_, err := cloud.WriteCLIToken(os.Getenv("CORAL_DATA_DIR"))
	require.NoError(t, err)
	down := httptest.NewServer(http.NotFoundHandler())
	url := down.URL
	down.Close()
	ticket := filepath.Join(t.TempDir(), "ticket")
	require.NoError(t, os.WriteFile(ticket, []byte(testCred), 0o600))
	stdin := strings.NewReader(testCred)
	assert.Equal(t, exitCannotReach, runCloud([]string{"connect", "--ticket-file", "-", "--url", url}, stdin, &out, &errb), "Coral down: cannot reach (3)")
	assert.Equal(t, len(testCred), stdin.Len(), "stdin not read while Coral is down")
	assert.Equal(t, exitCannotReach, runCloud([]string{"connect", "--ticket-file", ticket, "--url", url}, nil, &out, &errb))
	assert.FileExists(t, ticket)
	assert.Equal(t, exitCannotReach, runCloud([]string{"connect", "--url", url}, nil, &out, &errb))
}

// Without Coral's command-line token (another user, or Coral not started with
// this data folder) nothing is read or sent.
func TestCloudConnectNeedsTheToken(t *testing.T) {
	unixOnly(t)
	f := newFakeCoral(t, ready, enrolled)
	ticket := secretFile(t, "ticket", testCred)
	var out, errb bytes.Buffer
	assert.Equal(t, 2, runCloud([]string{"connect", "--ticket-file", ticket, "--url", f.srv.URL, "--home", t.TempDir()}, nil, &out, &errb))
	assert.Contains(t, errb.String(), "command-line token")
	assert.FileExists(t, ticket)

	require.NoError(t, os.Chmod(filepath.Join(os.Getenv("CORAL_DATA_DIR"), cloud.CLITokenFile), 0o644))
	assert.Equal(t, 2, runCloud([]string{"connect", "--ticket-file", ticket, "--url", f.srv.URL}, nil, &out, &errb), "a token others can read is refused")
	assert.FileExists(t, ticket)
	assert.Zero(t, f.checks+f.refused+len(f.redeems), "nothing reached the server")

	// A token from another start (or another data folder) proves nothing: the
	// command stops at the hello and sends nothing else.
	home := t.TempDir()
	require.NoError(t, os.Chmod(home, 0o700))
	_, err := cloud.WriteCLIToken(home)
	require.NoError(t, err)
	errb.Reset()
	assert.Equal(t, 4, runCloud([]string{"connect", "--ticket-file", ticket, "--url", f.srv.URL, "--home", home}, nil, &out, &errb))
	assert.Contains(t, errb.String(), "not this Coral")
	assert.FileExists(t, ticket)
	assert.Zero(t, f.checks+len(f.redeems))

	// --home names the server's data folder.
	tok, err := cloud.WriteCLIToken(home)
	require.NoError(t, err)
	f.mu.Lock()
	f.auth = cloud.NewCLIAuth(tok)
	f.mu.Unlock()
	assert.Equal(t, 0, runCloud([]string{"connect", "--ticket-file", ticket, "--url", f.srv.URL, "--home", home}, nil, &out, &errb), errb.String())
	assert.Len(t, f.redeems, 1)
}

// #3285: 'coral cloud connect' with no file is the device-code flow: it starts
// the enrollment, shows the link and the code, waits, and says when connected.
// Without a terminal it asks nothing (the access stays View).
func TestCloudConnectInteractive(t *testing.T) {
	fastPoll(t)
	f := newFakeCoral(t, ready, enrolled)
	f.statuses = []string{idle, enrolling, enrolling, connected}
	var out, errb bytes.Buffer
	code := runCloud([]string{"connect", "--url", f.srv.URL}, strings.NewReader(""), &out, &errb)
	assert.Equal(t, 0, code, errb.String())
	o := out.String()
	// No terminal: one line a script or a log can use.
	assert.Contains(t, o, "To connect this server to Coral Hub, open https://app.coralhub.dev/enroll?code=ABCD-EFGH and enter code ABCD-EFGH\n")
	assert.Contains(t, o, "Connected to Coral Hub as build-box.")
	assert.Contains(t, o, "Coral Hub can view this server")
	assert.Equal(t, 1, f.connects)
	for _, c := range f.callList() {
		assert.NotContains(t, c, "max-mode", "no terminal: nothing asked, nothing changed")
	}
}

// At a terminal, once connected, it asks the most Coral Hub may do (and, for
// Launch, auto-accept); an expired code is offered again.
func TestCloudConnectFlowAsksAndRetries(t *testing.T) {
	fastPoll(t)
	f := newFakeCoral(t, ready, enrolled)
	f.statuses = []string{idle, enrolling, expired, enrolling, connected}
	c, _ := newCloudClient(f.srv.URL, "", "connect", io.Discard)
	require.NotNil(t, c)
	var out bytes.Buffer
	ui := connectUI{out: &out, in: bufio.NewReader(strings.NewReader("\n3\ny\n")), ask: true, how: "coral cloud connect"}
	assert.Equal(t, 0, c.connectFlow(context.Background(), ui), out.String())
	assert.Equal(t, 2, f.connects, "a new code after the expiry")
	o := out.String()
	// coral-hub's text, printed as it is: the link is not followed by a period.
	assert.Contains(t, o, "Not connected. The code expired before it was approved. If you couldn't sign in at app.coralhub.dev, Coral Hub is in closed beta: request access at https://coralhub.dev/hub.html\n")
	assert.Contains(t, o, "Waiting for you to approve")
	assert.Contains(t, o, "Coral Hub access: launch.")
	calls := strings.Join(f.callList(), "\n")
	assert.Contains(t, calls, `PUT /api/cloud/max-mode {"mode":"launch"}`)
	assert.Contains(t, calls, `PUT /api/cloud/allow-auto-accept {"allow":true}`)

	// Enter takes the default (View); auto-accept is not asked.
	f.statuses = []string{idle, enrolling, connected}
	out.Reset()
	ui.in = bufio.NewReader(strings.NewReader("\n"))
	assert.Equal(t, 0, c.connectFlow(context.Background(), ui))
	calls = strings.Join(f.callList(), "\n")
	assert.Contains(t, calls, `PUT /api/cloud/max-mode {"mode":"view"}`)
	assert.Equal(t, 1, strings.Count(calls, "allow-auto-accept"))

	// q after an expiry stops.
	f.statuses = []string{idle, enrolling, expired}
	out.Reset()
	ui.in = bufio.NewReader(strings.NewReader("q\n"))
	assert.Equal(t, 4, c.connectFlow(context.Background(), ui))
	assert.Contains(t, out.String(), "Run 'coral cloud connect' to try again")
}

// Ctrl-C while waiting cancels the enrollment in the server (never a
// connection) and exits 130.
func TestCloudConnectCtrlCCancels(t *testing.T) {
	fastPoll(t)
	f := newFakeCoral(t, ready, enrolled)
	f.statuses = []string{idle, enrolling}
	ctx, cancel := context.WithCancel(context.Background())
	polls := 0
	f.onStatus = func() {
		if polls++; polls == 3 {
			cancel()
		}
	}
	c, _ := newCloudClient(f.srv.URL, "", "connect", io.Discard)
	var out bytes.Buffer
	code := c.connectFlow(ctx, connectUI{out: &out, in: bufio.NewReader(strings.NewReader("")), ask: true, how: "coral cloud connect"})
	assert.Equal(t, 130, code)
	assert.Contains(t, out.String(), "Cancelled: this server was not connected")
	assert.Contains(t, strings.Join(f.callList(), "\n"), "POST /api/cloud/connect/cancel")
}

// Without a Coral Hub URL on the server: say how to set it, and change nothing.
func TestCloudConnectNotConfigured(t *testing.T) {
	f := newFakeCoral(t, ready, enrolled)
	f.statuses = []string{notConfigured}
	var out, errb bytes.Buffer
	assert.Equal(t, 2, runCloud([]string{"connect", "--url", f.srv.URL}, strings.NewReader(""), &out, &errb))
	assert.Contains(t, out.String(), "Coral Hub isn't configured on this server")
	assert.Contains(t, out.String(), "CORAL_CLOUD_URL=https://app.coralhub.dev")
	assert.Zero(t, f.connects)

	out.Reset()
	assert.Equal(t, 0, runCloud([]string{"status", "--url", f.srv.URL}, nil, &out, &errb))
	assert.Contains(t, out.String(), "isn't configured")
}

func TestCloudStatusAndDisconnect(t *testing.T) {
	f := newFakeCoral(t, ready, enrolled)
	f.statuses = []string{connected}
	var out, errb bytes.Buffer
	assert.Equal(t, 0, runCloud([]string{"status", "--url", f.srv.URL}, nil, &out, &errb), errb.String())
	assert.Contains(t, out.String(), "State:     connected")
	assert.Contains(t, out.String(), "Server:    build-box")

	out.Reset()
	assert.Equal(t, 0, runCloud([]string{"disconnect", "--url", f.srv.URL}, nil, &out, &errb))
	assert.Contains(t, out.String(), "Disconnected from Coral Hub")
	assert.Contains(t, strings.Join(f.callList(), "\n"), "POST /api/cloud/disconnect")

	// Already connected: connect refuses (exit 5), changes nothing, and names --force.
	out.Reset()
	assert.Equal(t, 5, runCloud([]string{"connect", "--url", f.srv.URL}, strings.NewReader(""), &out, &errb))
	assert.Contains(t, out.String(), "already connected to Coral Hub")
	assert.Contains(t, out.String(), "'coral cloud connect --force'")
	assert.Zero(t, f.connects)
}

// coral-hub's contract: --force forgets the enrollment here, then enrolls again. The old
// server entry stays in Coral Hub until the user revokes it there, and the output says so.
func TestCloudConnectForce(t *testing.T) {
	fastPoll(t)
	f := newFakeCoral(t, ready, enrolled)
	f.statuses = []string{connected, enrolling, connected}
	var out, errb bytes.Buffer
	assert.Equal(t, 0, runCloud([]string{"connect", "--force", "--url", f.srv.URL}, strings.NewReader(""), &out, &errb), errb.String())
	o := out.String()
	assert.Contains(t, o, "Forgot this server's enrollment (build-box). Its old entry stays in Coral Hub until you revoke it there.")
	assert.Contains(t, o, "Connected to Coral Hub as build-box.")
	calls := strings.Join(f.callList(), "\n")
	assert.Less(t, strings.Index(calls, "POST /api/cloud/disconnect"), strings.Index(calls, "POST /api/cloud/connect"), "disconnect, then connect")
	assert.Equal(t, 1, f.connects)

	// A revoked server: refused without --force too.
	f.statuses = []string{`{"configured":true,"hub_url":"https://app.coralhub.dev","state":"revoked","max_mode":"view"}`}
	out.Reset()
	assert.Equal(t, 5, runCloud([]string{"connect", "--url", f.srv.URL}, strings.NewReader(""), &out, &errb))
	assert.Contains(t, out.String(), "'coral cloud connect --force'")
	assert.Equal(t, 1, f.connects)
}

// QA's #3149 note: a credential that could not be removed after use is not used at
// all (hub.md). A file in a folder the user cannot write to is refused before
// anything is sent, and left where it is.
func TestCloudConnectRefusesACredentialItCannotRemove(t *testing.T) {
	unixOnly(t)
	if os.Geteuid() == 0 {
		t.Skip("root can remove anything")
	}
	f := newFakeCoral(t, ready, enrolled)
	ticket := secretFile(t, "ticket", testCred)
	dir := filepath.Dir(ticket)
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	var out, errb bytes.Buffer
	assert.Equal(t, 2, runCloud([]string{"connect", "--ticket-file", ticket, "--url", f.srv.URL}, nil, &out, &errb))
	assert.Contains(t, errb.String(), "folder is not writable")
	assert.FileExists(t, ticket)
	assert.Empty(t, f.redeems, "nothing was sent")
}

// QA's v6 nit: a Coral Hub that can't be reached or answers a server error is transient
// (exit 4, like busy), not a permanent failure (1).
func TestCloudConnectHubUnavailableIsRetryable(t *testing.T) {
	f := newFakeCoral(t, ready, enrolled)
	f.statuses = []string{idle}
	f.connectFail = http.StatusServiceUnavailable
	var out, errb bytes.Buffer
	assert.Equal(t, 4, runCloud([]string{"connect", "--url", f.srv.URL}, strings.NewReader(""), &out, &errb))
	assert.Contains(t, errb.String(), "Coral Hub could not be reached or had an error; try again in a minute")
	f.connectFail = http.StatusInternalServerError
	errb.Reset()
	assert.Equal(t, 1, runCloud([]string{"connect", "--url", f.srv.URL}, strings.NewReader(""), &out, &errb), "anything else is not retryable")
}

// #3285 follow-up: the unattended startup step waits as long as Coral's Retry-After says
// (the hub's, or this machine's own limit on new codes), never less than 30 s.
func TestCloudClientRecordsRetryAfter(t *testing.T) {
	f := newFakeCoral(t, ready, enrolled)
	f.statuses = []string{idle}
	f.connectFail, f.connectRetryAfter = http.StatusTooManyRequests, "540"
	c, _ := newCloudClient(f.srv.URL, "", "connect", io.Discard)
	require.NotNil(t, c)
	var out bytes.Buffer
	assert.Equal(t, 4, c.connectFlow(context.Background(), connectUI{out: &out, in: bufio.NewReader(strings.NewReader("")), how: "coral cloud connect"}))
	assert.Equal(t, 540*time.Second, c.retryAfter)
	f.connectFail, f.connectRetryAfter = 0, ""
	f.statuses = []string{idle, enrolling, connected}
	c.connectFlow(context.Background(), connectUI{out: &out, in: bufio.NewReader(strings.NewReader("")), how: "coral cloud connect"})
	assert.Zero(t, c.retryAfter, "cleared by the next answer")
}
