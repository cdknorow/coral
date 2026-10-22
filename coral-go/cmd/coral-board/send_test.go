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

// #4379: --attach (repeatable) goes in the request's attach list and the reply's attached links are
// printed; at most 4; not with --address yet.
func TestRunSendAttach(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		gotBody = nil
		_ = json.Unmarshal(data, &gotBody)
		_, _ = io.WriteString(w, `{"message":{"id":5},"delivered_to":["Orchestrator"],"attached":["coral://artifacts/aa","coral://artifacts/bb"]}`)
	}))
	defer server.Close()
	oldURL := serverURL
	serverURL = server.URL
	defer func() { serverURL = oldURL }()
	id := sendIdentity{Board: "alpha", SubscriberID: "Orchestrator", SessionName: "claude-abc"}

	var out bytes.Buffer
	if err := runSend([]string{"--board", "beta", "--attach", "coral://artifacts/aa", "--attach", "coral://artifacts/bb", "the", "report"}, id, &out); err != nil {
		t.Fatal(err)
	}
	got, _ := gotBody["attach"].([]any)
	if len(got) != 2 || got[0] != "coral://artifacts/aa" || got[1] != "coral://artifacts/bb" {
		t.Fatalf("attach = %v", gotBody["attach"])
	}
	if gotBody["content"] != "the report" {
		t.Fatalf("content = %v", gotBody["content"])
	}
	if !strings.Contains(out.String(), "Attached 2 file(s): coral://artifacts/aa, coral://artifacts/bb") {
		t.Fatalf("output = %q", out.String())
	}
	five := []string{"--board", "beta"}
	for i := 0; i < 5; i++ {
		five = append(five, "--attach", "coral://artifacts/x")
	}
	if err := runSend(append(five, "hi"), id, &out); err == nil || !strings.Contains(err.Error(), "at most 4") {
		t.Fatalf("five attachments: %v", err)
	}
	fiveAddr := []string{"--address", "srv/team"}
	for i := 0; i < 5; i++ {
		fiveAddr = append(fiveAddr, "--attach", "coral://artifacts/x")
	}
	if err := runSend(append(fiveAddr, "hi"), id, &out); err == nil || !strings.Contains(err.Error(), "at most 4") {
		t.Fatalf("five attachments to another Coral: %v", err)
	}
	if err := checkFreeText([]string{"send", "--board", "beta", "--attach", "coral://artifacts/aa", "hi"}); err != nil {
		t.Fatalf("the pre-check refuses --attach: %v", err)
	}
}

// C2 (#4403): --address with --attach posts the attach list to the relay route and prints the files the
// server sent; a refusal names the code and, when it is temporary, the message id to retry with.
func TestRunSendAddressAttach(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	status, reply := http.StatusOK, `{"ok":true,"message_id":"m-1","target_message_id":9,"attached":["coral://artifacts/aa"]}`
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
	if err := runSend([]string{"--address", "homelab/qa", "--attach", "coral://artifacts/aa", "the", "report"}, id, &out); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/board/alpha/relay-messages" {
		t.Fatalf("path = %q", gotPath)
	}
	if att, _ := gotBody["attach"].([]any); len(att) != 1 || att[0] != "coral://artifacts/aa" {
		t.Fatalf("attach = %v", gotBody["attach"])
	}
	if !strings.Contains(out.String(), "Attached 1 file(s): coral://artifacts/aa") {
		t.Fatalf("output = %q", out.String())
	}

	// No --attach: the body has none (a plain send is unchanged).
	out.Reset()
	if err := runSend([]string{"--address", "homelab/qa", "hello"}, id, &out); err != nil {
		t.Fatal(err)
	}
	if _, has := gotBody["attach"]; has {
		t.Fatalf("a plain send carries an attach list: %v", gotBody)
	}

	// A hub 429 reaches the caller with its code and the retry hint.
	status, reply = http.StatusTooManyRequests, `{"ok":false,"error":"artifact_quota","message":"attachment 1 of 1: Coral Hub's limit on files is used up; retry in 7 s"}`
	err := runSend([]string{"--address", "homelab/qa", "--message-id", "m-2", "--attach", "coral://artifacts/aa", "hi"}, id, &out)
	if err == nil || !strings.Contains(err.Error(), "(429)") || !strings.Contains(err.Error(), "artifact_quota") || !strings.Contains(err.Error(), "--message-id m-2") {
		t.Fatalf("429: %v", err)
	}
	// A local refusal (the switch is off) is an error with its code.
	status, reply = http.StatusForbidden, `{"ok":false,"error":"artifacts_off","message":"sending files is off on board \"alpha\""}`
	if err := runSend([]string{"--address", "homelab/qa", "--attach", "coral://artifacts/aa", "hi"}, id, &out); err == nil || !strings.Contains(err.Error(), "artifacts_off") {
		t.Fatalf("switch off: %v", err)
	}
	// An attach-limit refusal from the server carries its words in "error".
	status, reply = http.StatusBadRequest, `{"code":"not_yours","error":"coral://artifacts/aa is not one of your uploads"}`
	if err := runSend([]string{"--address", "homelab/qa", "--attach", "coral://artifacts/aa", "hi"}, id, &out); err == nil || !strings.Contains(err.Error(), "not one of your uploads") {
		t.Fatalf("not yours: %v", err)
	}
}

// #5197: send refuses messages that are or start with an unexpanded $(...) or backtick command.
func TestSendRefusesUnexpandedCommandSubstitution(t *testing.T) {
	id := sendIdentity{Board: "alpha", SubscriberID: "Orchestrator", SessionName: "s"}
	for _, text := range []string{
		"$(cat /tmp/x.txt)",
		"$(cat /tmp/x.txt) extra notes",
		"`cat /tmp/x.txt`",
		"`cat /tmp/x.txt` extra notes",
	} {
		var out bytes.Buffer
		err := runSend([]string{"--board", "beta", text}, id, &out)
		if err == nil || !strings.Contains(err.Error(), "refusing message with literal unexpanded command substitution") {
			t.Fatalf("text %q: want refusal error, got: %v", text, err)
		}
		if !strings.Contains(err.Error(), "coral-board send --board <target> - <<'EOF'") {
			t.Fatalf("text %q: want right form hint, got: %v", text, err)
		}
	}
}
