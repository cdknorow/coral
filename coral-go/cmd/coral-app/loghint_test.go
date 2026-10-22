//go:build webview

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// #4151: the disconnect overlay names the log this server writes, in its own data folder.
func TestServerLogPathIsTheNewestServerLog(t *testing.T) {
	// The home folder is shown as "~" (below). On Windows the temp folder is under the real home (its AppData\Local\Temp), which turned every path here into a "~" one.
	// This test is about which log is newest, so the home is a folder of its own that holds none of them.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dir := t.TempDir()
	if got := serverLogPath(dir); got != filepath.Join(dir, "coral.log") {
		t.Fatalf("no log yet: %q", got)
	}
	tray, coral := filepath.Join(dir, "tray.log"), filepath.Join(dir, "coral.log")
	for _, p := range []string{tray, coral} {
		if err := os.WriteFile(p, []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(coral, old, old); err != nil {
		t.Fatal(err)
	}
	if got := serverLogPath(dir); got != tray {
		t.Fatalf("a tray server: got %q, want %q", got, tray)
	}
	if err := os.Chtimes(tray, old.Add(-time.Hour), old.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := serverLogPath(dir); got != coral {
		t.Fatalf("a coral server: got %q, want %q", got, coral)
	}
}

func TestServerLogPathShowsHomeAsTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dir := filepath.Join(home, ".coral")
	if got, want := serverLogPath(dir), filepath.Join("~", ".coral", "coral.log"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// The path reaches the page as a JSON string, so a folder name can't end the script.
func TestLogHintScriptIsAJSONString(t *testing.T) {
	s := logHintScript(filepath.Join(t.TempDir(), `a"</script><b>`))
	if !strings.HasPrefix(s, "window.__CORAL_LOG_PATH__ = \"") || strings.Contains(s, "</script>") {
		t.Fatalf("not escaped: %s", s)
	}
}
