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

// #6523: coral-agent artifact reply <link> <comment-id> <text>: the agent's answer to a comment the Operator sent it, posted through the reply route.
const replyDigest = "5eed5eed5eed5eed5eed5eed5eed5eed5eed5eed5eed5eed5eed5eed5eed5eed"

func replyServer(t *testing.T, status int, body string, got *struct{ method, path, ctype, body string }) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if got != nil {
			got.method, got.path, got.ctype, got.body = r.Method, r.URL.Path, r.Header.Get("Content-Type"), string(b)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func TestReplyPostsIntoTheThreadThroughTheReplyRoute(t *testing.T) {
	var got struct{ method, path, ctype, body string }
	srv := replyServer(t, http.StatusCreated, `{"id":42,"parent_id":7,"body":"done"}`, &got)
	defer srv.Close()
	var out bytes.Buffer
	if err := replyToComment(srv.URL, "coral://artifacts/"+replyDigest, "7", "Done: 5,000 is fine.\nSecond line.", &out); err != nil {
		t.Fatal(err)
	}
	if got.method != http.MethodPost || got.path != "/api/artifacts/"+replyDigest+"/comments/7/reply" || got.ctype != "application/json" {
		t.Fatalf("request: %+v", got)
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(got.body), &body); err != nil || body["body"] != "Done: 5,000 is fine.\nSecond line." || len(body) != 1 {
		t.Fatalf("the body is the text and nothing else (no agent name or id the server could be asked to believe): %q", got.body)
	}
	if out.String() != "Reply posted (comment 42).\n" {
		t.Fatalf("stdout: %q", out.String())
	}
}

func TestReplyRefusedWhenNotAskedPrintsTheReasonAndFails(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
		want   string
	}{
		"not asked":  {http.StatusForbidden, `{"error":"this agent was not asked into that thread","code":"not_asked"}`, "this agent was not asked into that thread (not_asked)"},
		"ask ended":  {http.StatusConflict, `{"error":"the ask has ended","code":"ask_ended"}`, "the ask has ended (ask_ended)"},
		"reply cap":  {http.StatusConflict, `{"error":"this agent has left the most replies it may in that thread","code":"reply_limit"}`, "reply_limit"},
		"too big":    {http.StatusRequestEntityTooLarge, `{"error":"a reply is at most 8192 bytes"}`, "8192"},
		"plain text": {http.StatusBadGateway, "bad gateway", "bad gateway"},
		"empty body": {http.StatusForbidden, "", "403"},
	} {
		t.Run(name, func(t *testing.T) {
			srv := replyServer(t, tc.status, tc.body, nil)
			defer srv.Close()
			var out bytes.Buffer
			err := replyToComment(srv.URL, "coral://artifacts/"+replyDigest, "7", "text", &out)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "reply refused") {
				t.Fatalf("want a refusal naming %q, got %v", tc.want, err)
			}
			if out.Len() != 0 {
				t.Fatalf("nothing is printed as posted: %q", out.String())
			}
		})
	}
}

func TestReplyChecksItsArgumentsBeforeAnyRequest(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	defer srv.Close()
	for name, tc := range map[string]struct{ ref, id, text string }{
		"not a digest":         {"coral://artifacts/nope", "7", "text"},
		"a series link":        {"coral://artifacts/series/5e7d5e7d5e7d5e7d5e7d5e7d5e7d5e7d", "7", "text"},
		"a non-number id":      {"coral://artifacts/" + replyDigest, "seven", "text"},
		"a negative id":        {"coral://artifacts/" + replyDigest, "-1", "text"},
		"a path in the id":     {"coral://artifacts/" + replyDigest, "7/../8", "text"},
		"empty text":           {"coral://artifacts/" + replyDigest, "7", "   "},
		"another server's url": {"https://elsewhere.example/api/artifacts/" + replyDigest, "7", "text"},
	} {
		if err := replyToComment(srv.URL, tc.ref, tc.id, tc.text, io.Discard); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if hit {
		t.Fatal("a request went out for a bad argument")
	}
}

func TestA2xxThatIsNotAStoredCommentIsNotAReply(t *testing.T) {
	srv := replyServer(t, http.StatusOK, `{"ok":true}`, nil)
	defer srv.Close()
	var out bytes.Buffer
	if err := replyToComment(srv.URL, "coral://artifacts/"+replyDigest, "7", "text", &out); err == nil || out.Len() != 0 {
		t.Fatalf("a success-shaped answer with no comment is a failure and prints nothing: %v %q", err, out.String())
	}
}

func TestReplyCommandUsageAndExitCodes(t *testing.T) {
	srv := replyServer(t, http.StatusCreated, `{"id":9}`, nil)
	defer srv.Close()
	code, _, stderr := runAgent(t, srv.URL, "artifact", "reply", "coral://artifacts/"+replyDigest, "7")
	if code != 2 || !strings.Contains(stderr, "coral-agent artifact reply") {
		t.Fatalf("too few arguments: exit %d, %q", code, stderr)
	}
	code, out, _ := runAgent(t, srv.URL, "artifact", "reply", "coral://artifacts/"+replyDigest, "7", "an answer")
	if code != 0 || !strings.Contains(out, "Reply posted (comment 9)") {
		t.Fatalf("a reply: exit %d, %q", code, out)
	}
	denied := replyServer(t, http.StatusForbidden, `{"error":"this agent was not asked into that thread","code":"not_asked"}`, nil)
	defer denied.Close()
	code, _, stderr = runAgent(t, denied.URL, "artifact", "reply", "coral://artifacts/"+replyDigest, "7", "an answer")
	if code == 0 || !strings.Contains(stderr, "not_asked") {
		t.Fatalf("a refusal exits non-zero with Coral's reason: exit %d, %q", code, stderr)
	}
	if help, _, _ := runAgent(t, srv.URL, "artifact", "--help"); help != 0 {
		t.Fatalf("artifact --help: exit %d", help)
	}
}

// The path carries the parsed number, exactly its digits ("+5" and "0005" are the comment 5).
func TestReplyPathIsExactlyTheDigitsOfTheCommentId(t *testing.T) {
	for _, in := range []string{"5", "+5", "0005"} {
		var got struct{ method, path, ctype, body string }
		srv := replyServer(t, http.StatusCreated, `{"id":1}`, &got)
		if err := replyToComment(srv.URL, "coral://artifacts/"+replyDigest, in, "text", io.Discard); err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		srv.Close()
		if got.path != "/api/artifacts/"+replyDigest+"/comments/5/reply" {
			t.Fatalf("%q reached %q", in, got.path)
		}
	}
}
