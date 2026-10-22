package hooks

import (
	"io"
	"log"
	"os"
	"strings"
	"testing"
)

func TestLogCrashRedactsThePanicAndItsStack(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", "")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prevErr, prevOut, prevFlags := os.Stderr, log.Writer(), log.Flags()
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = prevErr; log.SetOutput(prevOut); log.SetFlags(prevFlags) })

	LogCrash("failed for Authorization: Bearer abcdefghijklmnopqrstuvwxyz0123456789ABCDEF in " + home + "/work and /home/otheruser/x")
	w.Close()
	b, _ := io.ReadAll(r)
	got := string(b)
	if !strings.Contains(got, "[CRASH] hook panicked") {
		t.Fatalf("no crash line: %q", got)
	}
	for _, leak := range []string{"abcdefghijklmnopqrstuvwxyz0123456789ABCDEF", home, "otheruser"} {
		if strings.Contains(got, leak) {
			t.Errorf("the crash trace carries %q: %s", leak, got)
		}
	}
}
