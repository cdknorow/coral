package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
		w.Write([]byte(`{"ok":false,"error":"not_linked"}`))
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

	status, reply = http.StatusServiceUnavailable, `{"ok":false,"error":"not_linked"}`
	if err := send(); err == nil || !strings.Contains(err.Error(), "retry later with --message-id") {
		t.Fatalf("a 503 keeps the retry hint: %v", err)
	}
}
