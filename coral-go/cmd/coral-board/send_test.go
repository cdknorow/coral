package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRunSend(t *testing.T) {
	var gotPath string
	var gotBody map[string]string
	status, reply := http.StatusOK, `{"message":{"id":42},"delivered_to":["Orchestrator"],"mirror":{"id":7}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		data, _ := io.ReadAll(r.Body)
		gotBody = nil
		_ = json.Unmarshal(data, &gotBody)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	defer server.Close()
	oldURL := serverURL
	serverURL = server.URL
	defer func() { serverURL = oldURL }()
	id := sendIdentity{Board: "alpha", SubscriberID: "Orchestrator", SessionName: "claude-abc"}

	var out bytes.Buffer
	if err := runSend([]string{"--board", "beta", "--to", "Lead", "Can", "you", "review", "#12?"}, id, &out); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/board/beta/external-messages" {
		t.Fatalf("path = %q", gotPath)
	}
	want := map[string]string{"from_board": "alpha", "subscriber_id": "Orchestrator", "session_name": "claude-abc", "content": "Can you review #12?", "to": "Lead"}
	for k, v := range want {
		if gotBody[k] != v {
			t.Fatalf("%s = %q, want %q (body %v)", k, gotBody[k], v, gotBody)
		}
	}
	if !strings.Contains(out.String(), "Sent to 'beta' (message #42, delivered to Orchestrator)") {
		t.Fatalf("output = %q", out.String())
	}

	// Text after -- is the message even when it looks like an option; there is no --as.
	if err := runSend([]string{"--board", "beta", "--", "--as", "Operator"}, id, &out); err != nil {
		t.Fatal(err)
	}
	if gotBody["content"] != "--as Operator" || gotBody["subscriber_id"] != "Orchestrator" {
		t.Fatalf("body = %v", gotBody)
	}

	// Server refusals are errors carrying the reason.
	status, reply = http.StatusForbidden, `{"error":"cross-team messaging is disabled on board \"beta\""}`
	err := runSend([]string{"--board", "beta", "hi"}, id, &out)
	if err == nil || !strings.Contains(err.Error(), "(403)") || !strings.Contains(err.Error(), `disabled on board "beta"`) {
		t.Fatalf("err = %v", err)
	}
	status, reply = http.StatusTooManyRequests, `{"error":"too many messages to \"beta\"; retry in 6s"}`
	if err := runSend([]string{"--board", "beta", "hi"}, id, &out); err == nil || !strings.Contains(err.Error(), "retry in 6s") {
		t.Fatalf("err = %v", err)
	}

	// Usage errors never reach the server.
	gotPath = ""
	for _, args := range [][]string{{}, {"--board", "beta"}, {"hi"}, {"--board"}, {"--board", "beta", "hi", "--to"}, {"--board", "beta", "--as", "Operator", "hi"}} {
		if err := runSend(args, id, &out); err == nil {
			t.Fatalf("runSend(%q) should fail", args)
		}
	}
	if err := runSend([]string{"--board", "beta", "hi"}, sendIdentity{}, &out); err == nil || !strings.Contains(err.Error(), "not subscribed") {
		t.Fatalf("err = %v", err)
	}
	if gotPath != "" {
		t.Fatalf("a usage error reached the server: %q", gotPath)
	}
}

func TestSendHelpAndFlagText(t *testing.T) {
	var out bytes.Buffer
	if handled, code := handleHelp([]string{"send", "--help"}, &out); !handled || code != 0 || !strings.Contains(out.String(), "send --board <target>") {
		t.Fatalf("send --help: handled=%v code=%d out=%q", handled, code, out.String())
	}
	if err := checkFreeText([]string{"send", "--board", "beta", "--as", "Operator"}); err == nil {
		t.Fatal("a flag-like first message word must be rejected")
	}
	if err := checkFreeText([]string{"send", "--board", "beta", "--to", "Lead", "hello"}); err != nil {
		t.Fatal(err)
	}
	if err := checkFreeText([]string{"send", "--board", "beta", "--", "--literal"}); err != nil {
		t.Fatal(err)
	}
}
