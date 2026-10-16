package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

// #3481: a session records the version its CLI reports; an update of the binary is
// noticed, an unchanged one is asked once.
func TestCLIVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		// The fake CLI is a #!/bin/sh script, which Windows can't execute; version detection
		// over a real CLI exec is exercised on unix (#3629).
		t.Skip("fake CLI is a shell script; not executable on Windows (#3629)")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	counter := filepath.Join(dir, "calls")
	write := func(out string) {
		script := "#!/bin/sh\necho x >> " + counter + "\necho '" + out + "'\n"
		if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	calls := func() int { b, _ := os.ReadFile(counter); return len(b) / 2 }
	settings := map[string]string{CLIPathSettingKey("claude"): bin}

	write("2.1.300 (Claude Code)")
	if v := CLIVersion(settings, "claude"); v != "2.1.300" {
		t.Fatalf("got %q", v)
	}
	CLIVersion(settings, "claude")
	if calls() != 1 {
		t.Errorf("asked %d times, want once (cached)", calls())
	}
	write("codex-cli 2.1.301-beta.2 extra")
	future := time.Now().Add(time.Minute)
	os.Chtimes(bin, future, future)
	if v := CLIVersion(settings, "claude"); v != "2.1.301-beta.2" {
		t.Errorf("after an update got %q", v)
	}
	if v := CLIVersion(nil, "terminal"); v != "" {
		t.Errorf("a terminal has no CLI version, got %q", v)
	}
	write("no version here")
	os.Chtimes(bin, future.Add(time.Minute), future.Add(time.Minute))
	if v := CLIVersion(settings, "claude"); v != "" {
		t.Errorf("unparseable output: %q", v)
	}
}

// #4057: the cache keeps one entry per CLI path, so updating the CLI many times doesn't grow it.
func TestCLIVersionCacheIsOneEntryPerPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake CLI is a shell script; not executable on Windows (#3629)")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	settings := map[string]string{CLIPathSettingKey("claude"): bin}
	size := func() int {
		n := 0
		cliVersions.Range(func(_, _ any) bool { n++; return true })
		return n
	}
	start := size()
	when := time.Now()
	for i := 0; i < 20; i++ {
		script := "#!/bin/sh\necho '2.1." + strconv.Itoa(i) + "'\n"
		if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		when = when.Add(time.Minute)
		os.Chtimes(bin, when, when)
		if v := CLIVersion(settings, "claude"); v != "2.1."+strconv.Itoa(i) {
			t.Fatalf("update %d: got %q", i, v)
		}
	}
	if got := size(); got != start+1 {
		t.Fatalf("20 versions of one CLI: the cache has %d entries, want %d", got, start+1)
	}
}
