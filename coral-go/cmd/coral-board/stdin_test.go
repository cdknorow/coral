package main

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// postedContent is the "content" field of the one request a recording server got.
func postedContent(t *testing.T, rs *recordingServer) string {
	t.Helper()
	reqs := rs.requests()
	if len(reqs) != 1 {
		t.Fatalf("want 1 request, got %d: %v", len(reqs), reqs)
	}
	if !strings.HasPrefix(reqs[0], "POST /api/board/team/messages {") {
		t.Fatalf("not a post: %q", reqs[0])
	}
	body := reqs[0][strings.Index(reqs[0], "{"):]
	var got struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("request %q: %v", reqs[0], err)
	}
	return got.Content
}

// #3691: "post -" posts the text piped in, as written: backticks, $( ), quotes and newlines
// included, with --to's mentions in front and only the trailing newline dropped.
func TestPostDashReadsStdinVerbatim(t *testing.T) {
	text := "Ready: `abc123` and $(make test) stay as text.\n'single' \"double\" \\ back\n\n- a bullet\n"
	for _, args := range [][]string{{"post", "-"}, {"post", "--to", "Lead", "-"}, {"post", "-", "--to", "Lead"}} {
		srv := newRecordingServer(t)
		res := runBoardInput(t, srv.URL, subscribedDataDir(t, srv.URL), strings.NewReader(text), args...)
		if res.code != 0 {
			t.Fatalf("%v: exit %d, stderr %q", args, res.code, res.stderr)
		}
		want := strings.TrimRight(text, "\n")
		if len(args) > 2 {
			want = "@Lead " + want
		}
		if got := postedContent(t, srv); got != want {
			t.Errorf("%v posted %q, want %q", args, got, want)
		}
		if res.stderr != "" {
			t.Errorf("%v: stdin text is sent as written with no shell note, stderr %q", args, res.stderr)
		}
	}
}

// Empty or whitespace-only stdin is refused (exit 2) and nothing is sent; "-- -" still posts a
// literal dash.
func TestPostDashRefusesEmptyStdin(t *testing.T) {
	for _, in := range []string{"", "\n", "  \n\t\n"} {
		srv := newRecordingServer(t)
		res := runBoardInput(t, srv.URL, subscribedDataDir(t, srv.URL), strings.NewReader(in), "post", "-")
		if res.code != 2 || !strings.Contains(res.stderr, "empty") {
			t.Errorf("stdin %q: exit %d, stderr %q; want exit 2 saying it's empty", in, res.code, res.stderr)
		}
		if reqs := srv.requests(); len(reqs) != 0 {
			t.Errorf("stdin %q: refused input made a request: %v", in, reqs)
		}
	}

	srv := newRecordingServer(t)
	res := runBoardInput(t, srv.URL, subscribedDataDir(t, srv.URL), strings.NewReader("ignored"), "post", "--", "-")
	if res.code != 0 {
		t.Fatalf("post -- -: exit %d, stderr %q", res.code, res.stderr)
	}
	if got := postedContent(t, srv); got != "-" {
		t.Errorf("post -- - posted %q, want a literal dash", got)
	}
}

// A task's --message and --body read stdin on "-" too.
func TestTaskTextFlagsReadStdin(t *testing.T) {
	for _, args := range [][]string{
		{"task", "complete", "5", "--message", "-"},
		{"task", "cancel", "5", "--message", "-"},
		{"task", "add", "A title", "--body", "-"},
	} {
		srv := newRecordingServer(t)
		res := runBoardInput(t, srv.URL, subscribedDataDir(t, srv.URL), strings.NewReader("line one\n`two` $(three)\n"), args...)
		reqs := srv.requests()
		if len(reqs) != 1 || !strings.Contains(reqs[0], `line one\n`+"`two`"+` $(three)"`) {
			t.Errorf("%v: exit %d, stderr %q, requests %v; want the stdin text as sent", args, res.code, res.stderr, reqs)
		}
	}
}

// readStdinText: a terminal on stdin is refused before anything is read, text over the cap and
// text that isn't UTF-8 are refused, and stdin is read once per run.
func TestReadStdinTextRefusals(t *testing.T) {
	defer func(r func() bool) { stdinIsTerminal = r }(stdinIsTerminal)
	defer func(r io.Reader) { stdinReader, stdinRead = r, false }(stdinReader)
	try := func(term bool, in []byte) error {
		stdinIsTerminal = func() bool { return term }
		stdinReader, stdinRead = bytes.NewReader(in), false
		_, err := readStdinText("message")
		return err
	}
	if err := try(true, []byte("text")); err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Errorf("a terminal on stdin: %v", err)
	}
	if err := try(false, bytes.Repeat([]byte("a"), maxStdinMessage+1)); err == nil || !strings.Contains(err.Error(), "over") {
		t.Errorf("over the cap: %v", err)
	}
	if err := try(false, bytes.Repeat([]byte("a"), maxStdinMessage)); err != nil {
		t.Errorf("exactly the cap: %v", err)
	}
	if err := try(false, []byte{0xff, 0xfe, 'a'}); err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Errorf("not UTF-8: %v", err)
	}
	if err := try(false, []byte("one")); err != nil {
		t.Fatal(err)
	}
	if _, err := readStdinText("body"); err == nil || !strings.Contains(err.Error(), "only one") {
		t.Errorf("a second read in one run: %v", err)
	}
}

// Text given as arguments that still holds $( or a backtick goes out as written, with a note on
// stderr; ordinary text gets none.
func TestArgumentTextWithShellSyntaxIsNoted(t *testing.T) {
	srv := newRecordingServer(t)
	res := runBoard(t, srv.URL, subscribedDataDir(t, srv.URL), "post", "$(cat /tmp/x.txt)")
	if res.code != 0 || !strings.Contains(res.stderr, "sent as written") {
		t.Errorf("exit %d, stderr %q; want a note", res.code, res.stderr)
	}
	if got := postedContent(t, srv); got != "$(cat /tmp/x.txt)" {
		t.Errorf("posted %q", got)
	}
	var b bytes.Buffer
	warnShellSyntax(&b, "plain text, a - dash and 100$")
	if b.Len() != 0 {
		t.Errorf("ordinary text noted: %q", b.String())
	}
}
