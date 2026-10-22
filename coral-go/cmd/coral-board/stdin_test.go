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

// #5197: Messages that are a single $(...) or backtick expression, or start with one,
// are refused (exit non-zero, nothing sent), with an error showing the right form.
func TestPostRefusesUnexpandedCommandSubstitution(t *testing.T) {
	cases := [][]string{
		{"$(cat /tmp/x.txt)"},
		{"  $(cat /tmp/x.txt)  "},
		{"$(cat /tmp/x.txt) and extra notes"},
		{"`cat /tmp/x.txt`"},
		{"  `cat /tmp/x.txt`  "},
		{"`cat /tmp/x.txt` and extra notes"},
		{"--to", "Lead", "$(cat /tmp/x.txt)"},
		{"@Lead $(cat /tmp/x.txt)"},
		{"@Lead @QA `cat /tmp/x.txt`"},
	}
	for _, args := range cases {
		srv := newRecordingServer(t)
		runArgs := append([]string{"post"}, args...)
		res := runBoard(t, srv.URL, subscribedDataDir(t, srv.URL), runArgs...)
		if res.code == 0 {
			t.Errorf("args %v: exit code 0, want non-zero refusal", args)
		}
		if !strings.Contains(res.stderr, "refusing message with literal unexpanded command substitution") {
			t.Errorf("args %v: stderr %q; want refusal message", args, res.stderr)
		}
		if strings.Contains(res.stderr, `coral-board post "$(cat file)"`) {
			t.Errorf("args %v: stderr %q; must not recommend shell substitution", args, res.stderr)
		}
		if !strings.Contains(res.stderr, "coral-agent artifact upload <file>") || !strings.Contains(res.stderr, "coral://artifacts/<digest>") {
			t.Errorf("args %v: stderr %q; want long-content artifact guidance", args, res.stderr)
		}
		if !strings.Contains(res.stderr, "coral-board post - <<'EOF'") {
			t.Errorf("args %v: stderr %q; want stdin heredoc suggestion", args, res.stderr)
		}
		if len(srv.requests()) != 0 {
			t.Errorf("args %v: refused command still made server requests: %v", args, srv.requests())
		}
	}
}

// Messages that merely contain $(...) or backticks (do not start with one) go out as written,
// with a warning note on stderr. Ordinary text and markdown fences get no warning.
func TestPostAllowsMessagesMerelyContainingShellSyntax(t *testing.T) {
	for _, content := range []string{
		"The result is $(cat /tmp/x.txt)",
		"Here is `cat /tmp/x.txt` output",
		"@Lead the result is $(cat /tmp/x.txt)",
	} {
		srv := newRecordingServer(t)
		res := runBoard(t, srv.URL, subscribedDataDir(t, srv.URL), "post", content)
		if res.code != 0 {
			t.Errorf("content %q: exit %d, stderr %q; want success", content, res.code, res.stderr)
		}
		if !strings.Contains(res.stderr, "sent as written") {
			t.Errorf("content %q: stderr %q; want warning note", content, res.stderr)
		}
		if got := postedContent(t, srv); got != content {
			t.Errorf("posted %q, want %q", got, content)
		}
	}

	// Ordinary text: no note
	var b bytes.Buffer
	warnShellSyntax(&b, "plain text, a - dash and 100$")
	if b.Len() != 0 {
		t.Errorf("ordinary text noted: %q", b.String())
	}

	// Markdown fence: not refused
	srv := newRecordingServer(t)
	fence := "```go\npackage main\n```"
	res := runBoard(t, srv.URL, subscribedDataDir(t, srv.URL), "post", fence)
	if res.code != 0 {
		t.Errorf("markdown fence: exit %d, stderr %q; want success", res.code, res.stderr)
	}
	if got := postedContent(t, srv); got != fence {
		t.Errorf("posted %q, want %q", got, fence)
	}
}
