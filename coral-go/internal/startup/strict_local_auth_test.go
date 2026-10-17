package startup

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cdknorow/coral/internal/config"
)

// #3936: strict local auth is on unless CORAL_STRICT_LOCAL_AUTH says off or the command line
// turns it off; a typo is on (never weaker); the first run on a data folder says so once.
func TestStrictLocalAuthIsOnUnlessTurnedOff(t *testing.T) {
	var out bytes.Buffer
	log.SetOutput(&out)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	on, off := true, false
	envHonoured := config.StrictLocalAuthEnvHonoured // a dev build only (#4290)
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
		{env: "0", set: true, want: !envHonoured},
		{env: " Off ", set: true, want: !envHonoured},
		{env: "no", set: true, want: !envHonoured},
		{env: "false", set: true, want: !envHonoured},
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
		if got, err := strictLocalAuth(t.TempDir(), tc.override); err != nil || got != tc.want {
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
	_, _ = strictLocalAuth(dir, nil)
	if n := strings.Count(out.String(), "Coral now requires a credential"); n != 1 {
		t.Fatalf("first start: notice shown %d times:\n%s", n, out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, strictNoticeFile)); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	_, _ = strictLocalAuth(dir, nil)
	if strings.Contains(out.String(), "Coral now requires a credential") {
		t.Errorf("the notice is shown again:\n%s", out.String())
	}
}

// #4290: strict local auth off is refused on the default data folder and fine on one of its own.
func TestStrictOffIsRefusedOnTheDefaultDataFolder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	def := filepath.Join(home, ".coral")
	if err := os.MkdirAll(def, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(def, link); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
	for _, dir := range []string{def, def + string(filepath.Separator), filepath.Join(def, "..", ".coral"), link} {
		err := refuseStrictOffOnDefaultDir(false, dir)
		if err == nil || !strings.Contains(err.Error(), "CORAL_DATA_DIR") {
			t.Errorf("strict off on %q: got %v, want a refusal naming CORAL_DATA_DIR", dir, err)
		}
		if err := refuseStrictOffOnDefaultDir(true, dir); err != nil {
			t.Errorf("strict on, %q: %v", dir, err)
		}
	}
	if err := refuseStrictOffOnDefaultDir(false, t.TempDir()); err != nil {
		t.Errorf("a folder of its own: %v", err)
	}
	if err := refuseStrictOffOnDefaultDir(false, filepath.Join(home, ".coral-test")); err != nil {
		t.Errorf("a sibling folder: %v", err)
	}
}

// Through strictLocalAuth, which Start calls: off on the default folder is an error, off on a
// folder of its own is not.
func TestStrictLocalAuthRefusesOffOnTheDefaultFolder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	off := false
	on, err := strictLocalAuth(filepath.Join(home, ".coral"), &off)
	if err == nil || !strings.Contains(err.Error(), "CORAL_DATA_DIR") {
		t.Fatalf("want a refusal naming CORAL_DATA_DIR, got on=%v err=%v", on, err)
	}
	if on, err := strictLocalAuth(t.TempDir(), &off); err != nil || on {
		t.Errorf("off on a folder of its own: on=%v err=%v", on, err)
	}
	t.Setenv("CORAL_STRICT_LOCAL_AUTH", "0")
	_, err = strictLocalAuth(filepath.Join(home, ".coral"), nil)
	if config.StrictLocalAuthEnvHonoured && err == nil {
		t.Error("CORAL_STRICT_LOCAL_AUTH=0 on the default folder started")
	}
	if !config.StrictLocalAuthEnvHonoured && err != nil {
		t.Errorf("a release build ignores the env, so strict stays on and starts: %v", err)
	}
}

// #4290: only a dev build honours CORAL_STRICT_LOCAL_AUTH=0; any other tier stays strict and says
// which flag turns it off, and the flag works in both.
func TestStrictLocalAuthEnvIsHonouredOnlyInDevBuilds(t *testing.T) {
	var out bytes.Buffer
	log.SetOutput(&out)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	t.Setenv("CORAL_STRICT_LOCAL_AUTH", "0")
	got, err := strictLocalAuth(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := !config.StrictLocalAuthEnvHonoured; got != want {
		t.Errorf("CORAL_STRICT_LOCAL_AUTH=0: strict = %v, want %v for this build", got, want)
	}
	if !config.StrictLocalAuthEnvHonoured && !strings.Contains(out.String(), "--strict-local-auth=false") {
		t.Errorf("an ignored env is not reported with the flag:\n%s", out.String())
	}
	off := false
	if got, err := strictLocalAuth(t.TempDir(), &off); err != nil || got {
		t.Errorf("--strict-local-auth=false: strict = %v, err %v, want off in every tier", got, err)
	}
}
