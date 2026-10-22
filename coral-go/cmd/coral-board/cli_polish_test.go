package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bom is a UTF-8 byte-order mark, which Notepad and Windows PowerShell put at the start of a file.
var bom = string(rune(0xFEFF))

// An option a command does not take is refused with exit 2 and the usage, and nothing is sent (#222). Each of these used to run and print as if
// the option were not there.
func TestUnknownOptionsAreRefusedWithoutAnyRequest(t *testing.T) {
	srv := newRecordingServer(t)
	dir := subscribedDataDir(t, srv.URL)
	for _, args := range [][]string{
		{"read", "--not-a-flag"},
		{"read", "--jsno"},
		{"read", "extra"},
		{"check", "--loud"},
		{"task", "list", "--not-a-flag"},
		{"task", "list", "--json=yes"},
		{"task", "current", "--nope"},
		{"task", "detail", "1", "--nope"},
		{"task", "detail", "1", "2"},
		{"status", "--nope"},
		{"queue", "--nope"},
		{"projects", "--x"},
		{"subscribers", "--x"},
		{"leave", "--x"},
		{"targets", "--x"},
	} {
		res := runBoard(t, srv.URL, dir, args...)
		assert.Equal(t, 2, res.code, "%v: stderr %q", args, res.stderr)
		assert.Contains(t, res.stderr, "Usage:", "%v", args)
	}
	assert.Empty(t, srv.requests(), "a refused command reaches no server")
}

// read's numbers: 0, a negative, a word and a number too big for an int were read as "not given".
func TestReadRefusesBadNumbers(t *testing.T) {
	srv := newRecordingServer(t)
	dir := subscribedDataDir(t, srv.URL)
	for _, args := range [][]string{
		{"read", "--last", "0"}, {"read", "--last", "-3"}, {"read", "--last", "abc"}, {"read", "--last", "99999999999999999999"},
		{"read", "--id", "0"}, {"read", "--id", "x"}, {"read", "--id=-1"}, {"read", "--last"},
	} {
		res := runBoard(t, srv.URL, dir, args...)
		assert.Equal(t, 2, res.code, "%v: stderr %q", args, res.stderr)
	}
	assert.Empty(t, srv.requests())
	res := runBoard(t, srv.URL, dir, "read", "--last=5", "--json")
	assert.NotEqual(t, 2, res.code, "valid numbers still run: %q", res.stderr)
}

func TestCheckKnownFlagsAcceptsWhatTheCommandsTake(t *testing.T) {
	for _, args := range [][]string{
		{"read"}, {"read", "--all"}, {"read", "-a", "--json"}, {"read", "--id", "7"}, {"read", "--last=3"},
		{"check", "--quiet"}, {"status"}, {"status", "--json"}, {"status", "--board", "team", "--activity"},
		{"queue", "--json"}, {"task", "list"}, {"task", "list", "--json"}, {"task", "detail", "4", "--json"}, {"task", "detail"},
		{"task", "add", "a title", "--body", "x"}, // not in the table: its own parser
		{"post", "--nope"},                        // post's own check handles its text
		{"wait", "--timeout", "1m"},
	} {
		assert.NoError(t, checkKnownFlags(args), "%v", args)
	}
}

// What stdin and --file change in a message, and nothing else (#222-C).
func TestInputTextNormalisation(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"final newline", "a\n", "a"},
		{"only one final newline goes", "a\n\n", "a\n"},
		{"crlf", "a\r\nb\r\n", "a\nb"},
		{"bom", "" + bom + "a", "a"},
		{"bom and crlf", "" + bom + "a\r\nb\r\n", "a\nb"},
		{"inner blank lines and indentation stay", "a\n\n  b\n", "a\n\n  b"},
		{"a lone cr stays", "a\rb", "a\rb"},
	}
	for _, c := range cases {
		got, err := normalizeInputText([]byte(c.in), "message", "on stdin")
		require.NoError(t, err, c.name)
		assert.Equal(t, c.want, got, c.name)
	}
	for name, in := range map[string][]byte{
		"empty": nil, "only whitespace": []byte(" \r\n\n"),
		"utf-16 le": {0xFF, 0xFE, 'a', 0}, "utf-16 be": {0xFE, 0xFF, 0, 'a'}, "latin-1": {'c', 'a', 'f', 0xE9},
	} {
		_, err := normalizeInputText(in, "message", "on stdin")
		assert.Error(t, err, name)
	}
	_, err := normalizeInputText([]byte{0xFF, 0xFE, 'a', 0}, "message", "on stdin")
	assert.Contains(t, err.Error(), "UTF-16", "says what it is")
	assert.Contains(t, err.Error(), "$OutputEncoding", "and the PowerShell 5.1 fix")
}

// post --file posts the file's text, with the same rules as stdin; --file and message text do not mix.
func TestPostFilePostsTheFile(t *testing.T) {
	srv := newRecordingServer(t)
	dir := subscribedDataDir(t, srv.URL)
	file := filepath.Join(t.TempDir(), "msg.txt")
	require.NoError(t, os.WriteFile(file, []byte(""+bom+"café \"quoted\" — ok\r\nsecond line\r\n"), 0o600))
	res := runBoard(t, srv.URL, dir, "post", "--file", file, "--to", "Lead")
	require.Equal(t, 0, res.code, res.stderr)
	assert.Equal(t, "@Lead café \"quoted\" — ok\nsecond line", postedContent(t, srv))

	srv2 := newRecordingServer(t)
	dir2 := subscribedDataDir(t, srv2.URL)
	for _, args := range [][]string{
		{"post", "--file", file, "also text"},
		{"post", "--file", filepath.Join(t.TempDir(), "missing.txt")},
		{"post", "--file"},
		{"post", "--to"},
	} {
		res := runBoard(t, srv2.URL, dir2, args...)
		assert.Equal(t, 2, res.code, "%v: %q", args, res.stderr)
	}
	assert.Empty(t, srv2.requests())
}

func TestPostHelpTellsAWindowsUserWhatToDo(t *testing.T) {
	srv := newRecordingServer(t)
	res := runBoard(t, srv.URL, subscribedDataDir(t, srv.URL), "post", "--help")
	require.Equal(t, 0, res.code)
	for _, want := range []string{"--file", "$OutputEncoding", "PowerShell 5.1", "byte-order mark", "one final newline"} {
		assert.Contains(t, res.stdout, want)
	}
	res = runBoard(t, srv.URL, subscribedDataDir(t, srv.URL), "send", "--help")
	assert.Contains(t, res.stdout, "--file")
	assert.True(t, strings.Contains(res.stdout, "$OutputEncoding"))
}

// task list --json prints the tasks as a JSON array (an empty board is [], never null), and the table is unchanged without it.
func TestTaskListJSON(t *testing.T) {
	isolateBoardEnv(t)
	tasks := []map[string]any{
		{"id": 2, "status": "in_progress", "priority": "low", "assigned_to": "a", "title": "second", "workflow": map[string]any{}},
		{"id": 1, "status": "pending", "priority": "high", "title": "first", "workflow": map[string]any{}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"tasks": tasks})
	}))
	defer srv.Close()
	serverURL = srv.URL

	run := func(asJSON bool) string {
		r, w, err := os.Pipe()
		require.NoError(t, err)
		old := os.Stdout
		os.Stdout = w
		done := make(chan string)
		go func() { b, _ := io.ReadAll(r); done <- string(b) }()
		cmdTaskList(&boardState{Project: "team"}, asJSON)
		w.Close()
		os.Stdout = old
		return <-done
	}
	var got []map[string]any
	require.NoError(t, json.Unmarshal([]byte(run(true)), &got), "valid JSON, not a table")
	require.Len(t, got, 2)
	assert.EqualValues(t, 1, got[0]["id"], "in the table's order: priority, then id")
	assert.Equal(t, "second", got[1]["title"])
	assert.Contains(t, run(false), "ID", "the table is still the default")

	tasks = nil
	assert.Equal(t, "[]", strings.TrimSpace(run(true)), "an empty board is an empty array")
}

// status always printed JSON; --json is accepted (a no-op) and a real error is printed once (#222-D).
func TestStatusAcceptsJSON(t *testing.T) {
	srv := newRecordingServer(t)
	dir := subscribedDataDir(t, srv.URL)
	plain := runBoard(t, srv.URL, dir, "status")
	withFlag := runBoard(t, srv.URL, dir, "status", "--json")
	require.Equal(t, 0, plain.code, plain.stderr)
	assert.Equal(t, 0, withFlag.code, withFlag.stderr)
	assert.Equal(t, plain.stdout, withFlag.stdout)
	bad := runBoard(t, srv.URL, dir, "status", "--nope")
	assert.Equal(t, 2, bad.code)
	assert.Equal(t, 1, strings.Count(bad.stderr, `unknown option "--nope"`), "printed once: %q", bad.stderr)
}

// getBoard gives up on a server that accepts the connection and never answers (http.Get had no timeout: read and check hung for ever).
func TestGetBoardTimesOut(t *testing.T) {
	isolateBoardEnv(t)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	serverURL = srv.URL
	old := boardClient
	boardClient = &http.Client{Timeout: 200 * time.Millisecond}
	defer func() { boardClient = old }()
	start := time.Now()
	_, err := getBoard("/team/messages")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot reach Coral server")
	assert.Less(t, time.Since(start), 5*time.Second)
}

// task current --json prints the task as JSON, or null when there is none; without --json it is the text it always was (#241 R1).
func TestTaskCurrentJSON(t *testing.T) {
	have := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !have {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"no active task"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":7,"title":"fix it","priority":"high","status":"in_progress","revision":2,"workflow":{}}`))
	}))
	defer srv.Close()
	dir := subscribedDataDir(t, srv.URL)

	res := runBoard(t, srv.URL, dir, "task", "current", "--json")
	require.Equal(t, 0, res.code, res.stderr)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(res.stdout), &got), res.stdout)
	assert.EqualValues(t, 7, got["id"])
	assert.Equal(t, "fix it", got["title"])
	res = runBoard(t, srv.URL, dir, "task", "detail", "--json") // no id: the current task
	require.NoError(t, json.Unmarshal([]byte(res.stdout), &got), res.stdout)
	res = runBoard(t, srv.URL, dir, "task", "current")
	assert.Contains(t, res.stdout, "Task #7")

	have = false
	res = runBoard(t, srv.URL, dir, "task", "current", "--json")
	assert.Equal(t, 0, res.code)
	assert.Equal(t, "null", strings.TrimSpace(res.stdout))
	res = runBoard(t, srv.URL, dir, "task", "current")
	assert.Contains(t, res.stdout, "No active task")
}

// Options are read before the words of a message: an option-looking first word is refused, with a "did you mean" when it is a typo (#241).
func TestPostRefusesAnUnknownOptionBeforeTheText(t *testing.T) {
	srv := newRecordingServer(t)
	dir := subscribedDataDir(t, srv.URL)
	res := runBoard(t, srv.URL, dir, "post", "--too", "Lead", "hello")
	assert.Equal(t, 2, res.code, res.stderr)
	assert.Contains(t, res.stderr, "did you mean --to")
	res = runBoard(t, srv.URL, dir, "post", "--nope", "hello")
	assert.Equal(t, 2, res.code)
	assert.Contains(t, res.stderr, "unknown option")
	assert.NotContains(t, res.stderr, "did you mean")
	assert.Empty(t, srv.requests())
}

// Once the text has begun, a "--word" that is not an option is text, whatever it looks like (#241); --to and --file are read anywhere, in any case.
func TestPostTreatsFlagsInTextAsText(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"post", "use --force here"}, "use --force here"},
		{[]string{"post", "use", "--force", "here"}, "use --force here"},
		{[]string{"post", "hello", "--fix"}, "hello --fix"},
		{[]string{"post", "scroll", "to", "--top"}, "scroll to --top"},
		{[]string{"post", "hello", "--too", "Lead"}, "hello --too Lead"},
		{[]string{"post", "list", "the", "--files"}, "list the --files"},
		{[]string{"post", "hello", "--go", "-o", "--no"}, "hello --go -o --no"},
		{[]string{"post", "run go test --count=1"}, "run go test --count=1"},
		{[]string{"post", "--TO", "Lead", "hello"}, "@Lead hello"},
		{[]string{"post", "hello", "--TO", "Lead"}, "@Lead hello"},
	} {
		srv := newRecordingServer(t)
		res := runBoard(t, srv.URL, subscribedDataDir(t, srv.URL), c.args...)
		require.Equal(t, 0, res.code, "%v: %q", c.args, res.stderr)
		assert.Equal(t, c.want, postedContent(t, srv), "%v", c.args)
	}
}

// An unknown command is a usage error (2); --file at a folder says so; send refuses an option-looking first word, with a hint (QA's pass on #100).
func TestUsageExitAndFileFolderAndSendTypo(t *testing.T) {
	srv := newRecordingServer(t)
	dir := subscribedDataDir(t, srv.URL)
	res := runBoard(t, srv.URL, dir, "frobnicate")
	assert.Equal(t, 2, res.code, res.stderr)
	assert.Contains(t, res.stderr, "Unknown command")

	res = runBoard(t, srv.URL, dir, "post", "--file", t.TempDir())
	assert.Equal(t, 2, res.code)
	assert.Contains(t, res.stderr, "is a folder")
	assert.NotContains(t, res.stderr, "Incorrect function")

	res = runBoard(t, srv.URL, dir, "send", "--board", "other", "--reply-too", "4", "hello")
	assert.Equal(t, 2, res.code, res.stderr)
	assert.Contains(t, res.stderr, "did you mean --reply-to")
	res = runBoard(t, srv.URL, dir, "send", "--BOARD", "other", "use", "--force", "here")
	assert.NotContains(t, res.stderr, "unknown option", "options are read in any case, and --words after the text are text")
	assert.NotContains(t, res.stderr, "needs a value")
}
