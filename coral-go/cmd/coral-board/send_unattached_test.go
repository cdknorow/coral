package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #5499: the reply's unattached_links print as a warning that says what to do; a send with none prints none.
func TestSendAddressWarnsOfLinksThatAreNotAttached(t *testing.T) {
	isolateBoardEnv(t)
	reply := `{"ok":true,"message_id":"m-1","target_message_id":77,"warnings":["unattached_artifact_links"],"unattached_links":["coral://artifacts/` + strings.Repeat("a", 64) + `","coral://artifacts/` + strings.Repeat("b", 64) + `"]}`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(reply))
	}))
	defer ts.Close()
	oldURL := serverURL
	serverURL = ts.URL
	defer func() { serverURL = oldURL }()

	id := sendIdentity{Board: "alpha", SubscriberID: "Orchestrator", SessionName: "tmux-1"}
	var out bytes.Buffer
	if err := runSend([]string{"--address", "homelab/qa", "see", "the", "files"}, id, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"Sent to 'homelab/qa'", "Warning: the message names 2 links that are not attached", "other Coral cannot open it", "--attach coral://artifacts/<id>",
		"not attached: coral://artifacts/" + strings.Repeat("a", 64), "not attached: coral://artifacts/" + strings.Repeat("b", 64)} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}

	reply = `{"ok":true,"message_id":"m-2","target_message_id":78,"unattached_links":["coral://artifacts/` + strings.Repeat("c", 64) + `"]}`
	out.Reset()
	if err := runSend([]string{"--address", "homelab/qa", "one"}, id, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "names 1 link that is not attached") {
		t.Errorf("singular: %q", out.String())
	}

	reply = `{"ok":true,"message_id":"m-3","target_message_id":79}`
	out.Reset()
	if err := runSend([]string{"--address", "homelab/qa", "none"}, id, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "Warning") || strings.Contains(out.String(), "not attached") {
		t.Errorf("no warning without links: %q", out.String())
	}
}

// #5501: the files attached from the text's links are said to be automatic.
func TestSendAddressSaysWhichFilesWereAttachedFromTheText(t *testing.T) {
	isolateBoardEnv(t)
	a, b := "coral://artifacts/"+strings.Repeat("a", 64), "coral://artifacts/"+strings.Repeat("b", 64)
	reply := `{"ok":true,"message_id":"m-1","target_message_id":77,"attached":["` + a + `","` + b + `"],"auto_attached":["` + b + `"]}`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(reply))
	}))
	defer ts.Close()
	oldURL := serverURL
	serverURL = ts.URL
	defer func() { serverURL = oldURL }()
	id := sendIdentity{Board: "alpha", SubscriberID: "Orchestrator", SessionName: "tmux-1"}
	var out bytes.Buffer
	if err := runSend([]string{"--address", "homelab/qa", "files"}, id, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Attached 2 file(s)", "1 of them was attached automatically because the message's text links it."} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in %q", want, out.String())
		}
	}
	if strings.Contains(out.String(), "Warning") {
		t.Errorf("no warning when nothing is left unattached: %q", out.String())
	}
}
