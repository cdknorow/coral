package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The sequences a hostile post, task or screen could carry (#3580), as the server would send
// them inside JSON strings.
const hostileJSON = `before \u001b[2K\u001b[1Afaked line \u001b]0;pwned title\u0007\u001b]52;c;ZWNobyBwd25lZA==\u0007` +
	`\u001b]8;;https://evil.test\u001b\\link text\u001b]8;;\u001b\\ \u009b31mred\u001b[0m \u0000\u0008\u007f\u0085after`

// hostileServer answers coral-board's read, peek, task list and status with hostileJSON in
// every text field it prints.
func hostileServer(t *testing.T) *httptest.Server {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/messages") || strings.HasSuffix(r.URL.Path, "/messages/all"):
			w.Write([]byte(`[{"id":7,"subscriber_id":"Evil\u001b[8m","job_title":"Evil","content":"` + hostileJSON + `","created_at":"2026-10-15T16:00:00Z"}]`))
		case strings.HasSuffix(r.URL.Path, "/peek"):
			w.Write([]byte(`{"output":"` + hostileJSON + `\n"}`))
		case strings.HasSuffix(r.URL.Path, "/tasks"):
			w.Write([]byte(`{"tasks":[{"id":3,"title":"` + hostileJSON + `","status":"pending","priority":"medium","assigned_to":"Tester"}],"next_cursor":""}`))
		default:
			w.Write([]byte(`{"error":"` + hostileJSON + `"}`))
		}
	}))
	t.Cleanup(s.Close)
	return s
}

// controls lists the terminal controls left in out (newline and tab are fine).
func controls(out string) []string {
	var found []string
	for _, r := range out {
		if (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			found = append(found, fmt.Sprintf("%U", r))
		}
	}
	return found
}

func TestPrintedServerTextHasNoTerminalControl(t *testing.T) {
	s := hostileServer(t)
	dir := subscribedDataDir(t, s.URL)
	for _, args := range [][]string{
		{"read"},
		{"read", "--all"},
		{"read", "--last", "5"},
		{"read", "--json"},
		{"peek", "Evil"},
		{"task", "list"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			run := runBoard(t, s.URL, dir, args...)
			out := run.stdout + run.stderr
			assert.Empty(t, controls(out), "control characters printed: %q", out)
			assert.NotContains(t, out, "pwned title")
			assert.NotContains(t, out, "ZWNobyBwd25lZA==", "the clipboard payload is gone with its sequence")
			assert.NotContains(t, out, "evil.test")
			if args[0] != "task" || run.code == 0 {
				assert.Contains(t, out, "faked line", "the text itself is still shown")
				assert.Contains(t, out, "link text")
			}
		})
	}
}

func TestRawPrintsTheServersBytes(t *testing.T) {
	s := hostileServer(t)
	dir := subscribedDataDir(t, s.URL)
	run := runBoard(t, s.URL, dir, "peek", "Evil", "--raw")
	assert.Contains(t, run.stdout, "\x1b]0;pwned title\x07", "--raw keeps the bytes")
	run = runBoard(t, s.URL, dir, "--raw", "read")
	assert.Contains(t, run.stdout, "\x1b[2K")
}
