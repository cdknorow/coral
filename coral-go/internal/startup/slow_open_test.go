package startup

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"
)

// #4151: a database that takes long to open (another process holds it locked) is named in
// the log while startup waits, and again when it opens; a quick open logs nothing.
func TestNoticeSlowOpen(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	old := slowOpenAfter
	t.Cleanup(func() { slowOpenAfter = old })
	slowOpenAfter = 20 * time.Millisecond

	noticeSlowOpen("/data/sessions.db")()
	time.Sleep(60 * time.Millisecond)
	if buf.Len() != 0 {
		t.Fatalf("a quick open logged: %q", buf.String())
	}

	done := noticeSlowOpen("/data/sessions.db")
	time.Sleep(60 * time.Millisecond)
	done()
	out := buf.String()
	if !strings.Contains(out, "still opening sessions.db") || !strings.Contains(out, "locked") || !strings.Contains(out, "sessions.db opened after") {
		t.Fatalf("got %q", out)
	}
}
