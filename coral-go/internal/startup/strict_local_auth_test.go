package startup

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #3936: strict local auth is on unless CORAL_STRICT_LOCAL_AUTH says off or the command line
// turns it off; a typo is on (never weaker); the first run on a data folder says so once.
func TestStrictLocalAuthIsOnUnlessTurnedOff(t *testing.T) {
	var out bytes.Buffer
	log.SetOutput(&out)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	on, off := true, false
	for _, tc := range []struct {
		env      string
		set      bool
		override *bool
		want     bool
	}{
		{want: true},
		{env: "1", set: true, want: true},
		{env: "on", set: true, want: true},
		{env: "", set: true, want: true},
		{env: "0", set: true, want: false},
		{env: " Off ", set: true, want: false},
		{env: "no", set: true, want: false},
		{env: "false", set: true, want: false},
		{env: "nope", set: true, want: true}, // a typo never weakens it
		{env: "0", set: true, override: &on, want: true},
		{override: &off, want: false},
	} {
		if tc.set {
			t.Setenv("CORAL_STRICT_LOCAL_AUTH", tc.env)
		} else {
			t.Setenv("CORAL_STRICT_LOCAL_AUTH", "")
			os.Unsetenv("CORAL_STRICT_LOCAL_AUTH")
		}
		if got := strictLocalAuth(t.TempDir(), tc.override); got != tc.want {
			t.Errorf("CORAL_STRICT_LOCAL_AUTH=%q (set %v), override %v: got %v, want %v", tc.env, tc.set, tc.override, got, tc.want)
		}
	}
	if !strings.Contains(out.String(), `CORAL_STRICT_LOCAL_AUTH="nope" is not on or off: taken as on`) {
		t.Errorf("a typo is not reported:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "strict local auth is OFF") {
		t.Errorf("off is not reported:\n%s", out.String())
	}

	// The notice: once per data folder.
	os.Unsetenv("CORAL_STRICT_LOCAL_AUTH")
	dir := t.TempDir()
	out.Reset()
	strictLocalAuth(dir, nil)
	if n := strings.Count(out.String(), "Coral now requires a credential"); n != 1 {
		t.Fatalf("first start: notice shown %d times:\n%s", n, out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, strictNoticeFile)); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	strictLocalAuth(dir, nil)
	if strings.Contains(out.String(), "Coral now requires a credential") {
		t.Errorf("the notice is shown again:\n%s", out.String())
	}
}
