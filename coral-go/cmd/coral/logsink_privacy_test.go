package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// coral.log is what a bug report carries: the home folder, another user's folder name and the computer's name never reach it (#6053).
func TestCoralLogCarriesNoHomeFolderOrUserName(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", "")
	dir := filepath.Join(home, ".coral")
	prevOut, prevFlags := log.Writer(), log.Flags()
	t.Cleanup(func() { log.SetOutput(prevOut); log.SetFlags(prevFlags) })

	setupCrashLogging(dir)
	log.Printf("opened %s and /home/someoneelse/project and C:\\Users\\Other\\x", filepath.Join(home, ".coral", "sessions.db"))
	b, err := os.ReadFile(filepath.Join(dir, "coral.log"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, leak := range []string{home, "someoneelse", "Other"} {
		if strings.Contains(got, leak) {
			t.Errorf("coral.log carries %q: %s", leak, got)
		}
	}
	sep := string(filepath.Separator) // the redactor keeps the system's own separator after "~"
	if !strings.Contains(got, "~"+sep+".coral"+sep+"sessions.db") {
		t.Errorf("the home folder is not written as ~: %s", got)
	}
}
