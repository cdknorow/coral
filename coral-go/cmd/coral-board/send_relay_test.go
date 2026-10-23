package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/subgentic/coral/internal/clierr"
)

// #3489: send --address goes to this board's relay-messages route with the sender's own
// identity, and targets lists the hub's answer.
func TestSendAddressAndTargets(t *testing.T) {
	isolateBoardEnv(t)
	var gotPath, gotQuery string
	var gotBody map[string]any
	status := http.StatusOK
	reply := `{"ok":true,"message_id":"m-1","target_message_id":77}`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		gotBody = nil
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(reply))
	}))
	defer ts.Close()
	oldURL := serverURL
	serverURL = ts.URL
	defer func() { serverURL = oldURL }()

	id := sendIdentity{Board: "alpha", SubscriberID: "Orchestrator", SessionName: "tmux-1"}
	var out bytes.Buffer
	if err := runSend([]string{"--address", "homelab/qa", "--reply-to", "12", "please", "check"}, id, &out); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/board/alpha/relay-messages" {
		t.Errorf("path %q", gotPath)
	}
	if gotBody["to_address"] != "homelab/qa" || gotBody["content"] != "please check" || gotBody["subscriber_id"] != "Orchestrator" || gotBody["session_name"] != "tmux-1" || gotBody["reply_to"] != float64(12) {
		t.Errorf("body %v", gotBody)
	}
	if !strings.Contains(out.String(), "homelab/qa") || !strings.Contains(out.String(), "m-1") {
		t.Errorf("output %q", out.String())
	}

	for _, args := range [][]string{
		{"--address", "homelab/qa", "--board", "beta", "hi"},
		{"--board", "beta", "--reply-to", "3", "hi"},
		{"--address", "homelab/qa", "--reply-to", "x", "hi"},
	} {
		if err := runSend(args, id, &out); err == nil {
			t.Errorf("%v: want an error", args)
		}
	}

	status, reply = http.StatusServiceUnavailable, `{"ok":false,"error":"target_offline","message":"later"}`
	err := runSend([]string{"--address", "homelab/qa", "hi"}, id, &out)
	if err == nil || !strings.Contains(err.Error(), "target_offline") || !strings.Contains(err.Error(), "--message-id") {
		t.Errorf("want a retry hint, got %v", err)
	}

	status, reply = http.StatusOK, `{"ok":true,"targets":[{"address":"homelab/qa","reachable":true},{"address":"lab2/ops","reachable":false}]}`
	out.Reset()
	if err := runTargets("alpha", "Orchestrator", "tmux-1", &out); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/board/alpha/relay-targets" || !strings.Contains(gotQuery, "subscriber_id=Orchestrator") {
		t.Errorf("targets request %q %q", gotPath, gotQuery)
	}
	if out.String() != "homelab/qa (online)\nlab2/ops (offline)\n" {
		t.Errorf("targets output %q", out.String())
	}

	// No reachable field (what Coral Hub sends): the address alone, never "(offline)" (#5598).
	status, reply = http.StatusOK, `{"ok":true,"targets":[{"address":"homelab/qa"},{"address":"lab2/ops","reachable":false}]}`
	out.Reset()
	if err := runTargets("alpha", "Orchestrator", "tmux-1", &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "homelab/qa\nlab2/ops (offline)\n" {
		t.Errorf("targets output without reachable %q", out.String())
	}
}

// Through the real binary's dispatch (main, its help and free-text pre-checks), not
// runSend: send --address with --reply-to and --message-id reaches relay-messages, and
// targets is a command (QA's #3489 finding: the pre-check refused these options).
func TestSendAddressThroughMainDispatch(t *testing.T) {
	srv := newRecordingServer(t)
	dataDir := subscribedDataDir(t, srv.URL)

	res := runBoard(t, srv.URL, dataDir, "send", "--address", "homelab/qa", "--reply-to", "5", "--message-id", "m-retry-1", "please", "check")
	if strings.Contains(res.stderr, "unknown option") {
		t.Fatalf("the pre-check refused an option: %s", res.stderr)
	}
	res = runBoard(t, srv.URL, dataDir, "targets")
	if strings.Contains(res.stderr, "Unknown command") {
		t.Fatalf("targets isn't a command: %s", res.stderr)
	}
	reqs := srv.requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %v", reqs)
	}
	if !strings.Contains(reqs[0], "/api/board/team/relay-messages") || !strings.Contains(reqs[0], `"to_address":"homelab/qa"`) ||
		!strings.Contains(reqs[0], `"reply_to":5`) || !strings.Contains(reqs[0], `"message_id":"m-retry-1"`) || !strings.Contains(reqs[0], `"content":"please check"`) {
		t.Errorf("send request = %s", reqs[0])
	}
	if !strings.Contains(reqs[1], "/api/board/team/relay-targets") {
		t.Errorf("targets request = %s", reqs[1])
	}
}

// A send without --message-id gets one made by the CLI, and a failure prints it, so the
// retry hint names a real id.
func TestSendAddressPrintsItsMessageIDOnFailure(t *testing.T) {
	isolateBoardEnv(t)
	var sent string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		sent, _ = b["message_id"].(string)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"ok":false,"error":"hub busy"}`))
	}))
	defer ts.Close()
	oldURL := serverURL
	serverURL = ts.URL
	defer func() { serverURL = oldURL }()
	err := runSend([]string{"--address", "homelab/qa", "hi"}, sendIdentity{Board: "alpha", SubscriberID: "Orchestrator", SessionName: "s"}, &bytes.Buffer{})
	if sent == "" {
		t.Fatal("the CLI sends a message_id")
	}
	if err == nil || !strings.Contains(err.Error(), "--message-id "+sent) {
		t.Fatalf("the failure names the id %q: %v", sent, err)
	}
}

// #3503: a hub that doesn't relay (501 relay_unsupported: switched off, or too old) isn't
// a passing failure, so the error says so instead of suggesting a retry; a 503 still
// gets the retry hint.
func TestSendAddressDoesNotSuggestARetryWhenTheHubDoesNotRelay(t *testing.T) {
	isolateBoardEnv(t)
	status, reply := http.StatusNotImplemented, `{"ok":false,"error":"relay_unsupported","message":"Coral Hub doesn't relay messages (its relay is switched off, or the hub is too old)"}`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(reply))
	}))
	defer ts.Close()
	oldURL := serverURL
	serverURL = ts.URL
	defer func() { serverURL = oldURL }()
	send := func() error {
		return runSend([]string{"--address", "homelab/qa", "hi"}, sendIdentity{Board: "alpha", SubscriberID: "Orchestrator", SessionName: "s"}, &bytes.Buffer{})
	}
	err := send()
	if err == nil || strings.Contains(err.Error(), "retry later") || strings.Contains(err.Error(), "--message-id") {
		t.Fatalf("no retry hint for a hub that doesn't relay: %v", err)
	}
	if !strings.Contains(err.Error(), "relay_unsupported") || !strings.Contains(err.Error(), "retrying won't help") {
		t.Fatalf("the error says the hub doesn't relay: %v", err)
	}

	// A hub that is not connected at all (#334): nothing was delivered and a retry fails the same way, so the words send the person to the Operator, not to a retry.
	status, reply = http.StatusServiceUnavailable, `{"ok":false,"error":"not_linked"}`
	if err := send(); err == nil || strings.Contains(err.Error(), "retry later") || !strings.Contains(err.Error(), "ask the Operator to connect this Coral to Coral Hub") {
		t.Fatalf("not_linked does not advise a retry: %v", err)
	}
	status, reply = http.StatusServiceUnavailable, `{"ok":false,"error":"hub busy"}`
	if err := send(); err == nil || !strings.Contains(err.Error(), "retry later with --message-id") {
		t.Fatalf("any other 503 keeps the retry hint: %v", err)
	}
}

// #334 (the Codex tester's #333): the wrong form of a target, and a missing message, are usage errors that say what to do and send nothing.
func TestSendUsageErrorsSayWhatToDo(t *testing.T) {
	requests := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++ }))
	defer ts.Close()
	oldURL := serverURL
	serverURL = ts.URL
	defer func() { serverURL = oldURL }()
	id := sendIdentity{Board: "alpha", SubscriberID: "Orchestrator", SessionName: "s"}
	for name, tc := range map[string]struct {
		args []string
		want []string
	}{
		"--board with a server/team form":   {[]string{"--board", "pro/b-4tt7u67wul", "hi"}, []string{"looks like a team on another Coral", "--address pro/b-4tt7u67wul", "coral-board targets"}},
		"--address with a local board name": {[]string{"--address", "beta", "hi"}, []string{"--address needs <server>/<team>", "--board beta"}},
		"a missing message, local":          {[]string{"--board", "beta"}, []string{"usage: coral-board send --board", "A message is required", "--file", "stdin"}},
		"a missing message, address":        {[]string{"--address", "homelab/qa"}, []string{"usage: coral-board send --address", "--attach", "A message is required"}},
	} {
		err := runSend(tc.args, id, &bytes.Buffer{})
		if err == nil || clierr.KindOf(err) != clierr.Usage {
			t.Errorf("%s: want a usage error, got %v", name, err)
			continue
		}
		for _, w := range tc.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s: %q is missing %q", name, err.Error(), w)
			}
		}
	}
	if requests != 0 {
		t.Errorf("a usage error sends nothing: %d requests", requests)
	}
}

// #3520 (QA's bug bash F4): targets' failure reads "<code>: <message>", as send's does,
// and an answer with no message has no trailing separator.
func TestTargetsFailureFormat(t *testing.T) {
	isolateBoardEnv(t)
	reply := `{"ok":false,"error":"not_linked","message":"this Coral isn't connected to Coral Hub"}`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(reply))
	}))
	defer ts.Close()
	oldURL := serverURL
	serverURL = ts.URL
	defer func() { serverURL = oldURL }()

	err := runTargets("alpha", "Orchestrator", "s", &bytes.Buffer{})
	want := "listing targets failed. Coral says: not_linked: this Coral isn't connected to Coral Hub (HTTP 503)"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
	reply = `{"ok":false,"error":"not_orchestrator"}`
	err = runTargets("alpha", "Worker", "s", &bytes.Buffer{})
	if want = "listing targets failed. Coral says: not_orchestrator (HTTP 503)"; err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
}

// An attachment's refusal answers {"code", "error": words}: the command line prints the code before the words
// (#4453), on both the address and the local board forms.
func TestSendPrintsAnAttachmentRefusalsCode(t *testing.T) {
	isolateBoardEnv(t)
	reply := `{"code":"attach_secret","error":"coral://artifacts/ab looks like it contains a secret (a token or a key), so it was not sent"}`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(reply))
	}))
	defer ts.Close()
	oldURL := serverURL
	serverURL = ts.URL
	defer func() { serverURL = oldURL }()
	id := sendIdentity{Board: "alpha", SubscriberID: "Orchestrator", SessionName: "tmux-1"}
	var out bytes.Buffer
	for _, args := range [][]string{{"--address", "homelab/qa", "--attach", "coral://artifacts/ab", "hi"}, {"--board", "beta", "--attach", "coral://artifacts/ab", "hi"}} {
		err := runSend(args, id, &out)
		if err == nil || !strings.Contains(err.Error(), "Coral says: attach_secret: coral://artifacts/ab looks like it contains a secret") {
			t.Errorf("%v: %v", args, err)
		}
	}
	// A refusal whose error is already its code is not doubled.
	reply = `{"ok":false,"error":"target_offline","message":"later"}`
	err := runSend([]string{"--address", "homelab/qa", "hi"}, id, &out)
	if err == nil || strings.Count(err.Error(), "target_offline") != 1 {
		t.Errorf("%v", err)
	}
	for _, c := range []struct{ code, words, want string }{{"", "w", "w"}, {"c", "c", "c"}, {"c", "c: w", "c: w"}, {"c", "w", "c: w"}} {
		if got := refusalText(c.code, c.words); got != c.want {
			t.Errorf("refusalText(%q, %q) = %q", c.code, c.words, got)
		}
	}
}

// A send waits for each attachment's scan (up to 100 s), the wait for another send's scan (up to 30 s, #4474)
// and its transfer (up to 125 s).
func TestAttachWaitCoversTheScanAndTheTransfer(t *testing.T) {
	if got := attachWait(1); got != 290*time.Second {
		t.Errorf("one attachment: %v", got)
	}
	if got := attachWait(4); got != 1055*time.Second {
		t.Errorf("four attachments: %v", got)
	}
}
