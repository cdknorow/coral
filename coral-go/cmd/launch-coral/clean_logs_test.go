package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// launch-coral cleans old agent logs only in a folder of its own. In the shared
// system temp dir they belong to whatever Coral is running, production's included, and
// must survive (#3259).
func TestCleanOldLogsNeverTouchesTheSharedTempDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp) // os.TempDir() on Unix
	t.Setenv("TMP", tmp)    // and on Windows
	t.Setenv("TEMP", tmp)

	shared := filepath.Join(tmp, "claude_coral_aaaa.log")
	if err := os.WriteFile(shared, []byte("live"), 0o644); err != nil {
		t.Fatal(err)
	}
	cleanOldLogs(tmp)
	if _, err := os.Stat(shared); err != nil {
		t.Fatalf("a log in the shared temp dir was deleted: %v", err)
	}

	own := filepath.Join(t.TempDir(), "logs")
	if err := os.MkdirAll(own, 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(own, "claude_coral_bbbb.log")
	fresh := filepath.Join(own, "claude_coral_cccc.log") // a live agent of a server on this data dir
	other := filepath.Join(own, "notes.txt")
	for _, f := range []string{old, fresh, other} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	twoDaysAgo := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(old, twoDaysAgo, twoDaysAgo); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(other, twoDaysAgo, twoDaysAgo); err != nil {
		t.Fatal(err)
	}
	cleanOldLogs(own)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("an old log in launch-coral's own log dir was not cleaned: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("a recent log (a live agent's) was deleted: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("a file that is not an agent log was deleted: %v", err)
	}
}
