package agent

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

// #340: Windows refuses to open a PATH folder that is, or goes through, an untrusted mount point (ERROR_UNTRUSTED_MOUNT_POINT, 448). The lookup finds that folder and nothing else:
// Coral never follows the junctions itself.
func TestUntrustedMountDirFindsTheFolderWindowsWillNotOpen(t *testing.T) {
	root := t.TempDir()
	good, bad, other := filepath.Join(root, "good"), filepath.Join(root, "Codex", "bin"), filepath.Join(root, "other")
	var asked []string
	stat := func(p string) (os.FileInfo, error) {
		asked = append(asked, p)
		switch {
		case strings.HasPrefix(p, bad):
			return nil, &fs.PathError{Op: "CreateFile", Path: p, Err: syscall.Errno(448)}
		case strings.HasPrefix(p, good), strings.HasPrefix(p, other):
			return nil, &fs.PathError{Op: "CreateFile", Path: p, Err: fs.ErrNotExist}
		}
		return nil, errors.New("unexpected")
	}
	path := strings.Join([]string{good, bad, other}, string(os.PathListSeparator))

	dir, ok := untrustedMountDir("codex", path, "", stat)
	if !ok || dir != bad {
		t.Fatalf("got %q, %v; want %q", dir, ok, bad)
	}
	for _, p := range asked {
		if strings.HasPrefix(p, other) {
			t.Errorf("the lookup went on past the first untrusted folder: %v", asked)
		}
	}

	// Nothing wrong on PATH: not found, no complaint. Relative and empty entries are not searched.
	if dir, ok := untrustedMountDir("codex", strings.Join([]string{good, other, "", "rel"}, string(os.PathListSeparator)), "", stat); ok {
		t.Errorf("an ordinary missing CLI is not an untrusted mount: %q", dir)
	}
	// A file-level answer counts as well as the folder's.
	fileOnly := func(p string) (os.FileInfo, error) {
		if strings.HasSuffix(p, "codex.exe") {
			return nil, syscall.Errno(448)
		}
		return nil, fs.ErrNotExist
	}
	if dir, ok := untrustedMountDir("codex", good, "", fileOnly); !ok || dir != good {
		t.Errorf("a file Windows will not open: %q, %v", dir, ok)
	}
}

func TestTheUntrustedMountErrorNamesTheFolderAndTheFix(t *testing.T) {
	e := &CLIUntrustedMountError{AgentType: "codex", Binary: "codex", Dir: `C:\Users\x\AppData\Local\Programs\OpenAI\Codex\bin`}
	for _, want := range []string{"codex CLI not found", `C:\Users\x\AppData\Local\Programs\OpenAI\Codex\bin`, "untrusted mount point", "Settings > CLI paths", CLIPathSettingKey("codex"), "does not follow the junctions"} {
		if !strings.Contains(e.Error(), want) {
			t.Errorf("%q is missing %q", e.Error(), want)
		}
	}
	if strings.Contains(e.HubText(), "Users") || !strings.Contains(e.HubText(), "Settings > CLI paths") {
		t.Errorf("through Coral Hub no folder of this computer is named, the fix is: %q", e.HubText())
	}
}

// A restart, resume or wake with no configured path: the bare name would fail in the agent's shell the same way, so the launch is refused with the reason.
func TestLaunchCLISaysWhenPathHoldsAnUntrustedMount(t *testing.T) {
	old := UntrustedMountDir
	t.Cleanup(func() { UntrustedMountDir = old })
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	UntrustedMountDir = func(string) (string, bool) { return "X:\\Codex\\bin", true }
	_, err := LaunchCLI(map[string]string{}, "codex")
	var um *CLIUntrustedMountError
	if !errors.As(err, &um) || um.Dir != "X:\\Codex\\bin" {
		t.Fatalf("got %v", err)
	}
	UntrustedMountDir = func(string) (string, bool) { return "", false }
	if p, err := LaunchCLI(map[string]string{}, "codex"); err != nil || p != "" {
		t.Fatalf("an ordinary missing CLI keeps the old answer: %q, %v", p, err)
	}
	// A configured path is honoured as it is (CheckRunnable and the rest), whatever PATH holds.
	UntrustedMountDir = func(string) (string, bool) { return "X:\\Codex\\bin", true }
	if _, err := LaunchCLI(map[string]string{CLIPathSettingKey("codex"): filepath.Join(t.TempDir(), "missing.exe")}, "codex"); err == nil || errors.As(err, &um) {
		t.Fatalf("a configured path that cannot be run is its own error: %v", err)
	}
}

// QA's note on #150: a saved path through the junctions says why it is refused, not "no file".
func TestValidateCLIPathExplainsAnUntrustedMount(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("untrusted mount points are Windows'")
	}
	err := &fs.PathError{Op: "CreateFile", Path: "x", Err: syscall.Errno(448)}
	if !isUntrustedMount(err) {
		t.Fatal("448 is an untrusted mount point")
	}
	if isUntrustedMount(fs.ErrNotExist) || isUntrustedMount(nil) {
		t.Fatal("an ordinary miss is not")
	}
}

// Security's note on #150: only a bare name is looked up along PATH; a path (with a separator) is skipped before the join.
func TestUntrustedMountDirSkipsAnythingButABareName(t *testing.T) {
	stat := func(string) (os.FileInfo, error) { return nil, syscall.Errno(448) }
	dir := t.TempDir()
	for _, name := range []string{"", `C:\x\codex.exe`, "a/b", `..\codex`, "sub/codex"} {
		if got, ok := untrustedMountDir(name, dir, "", stat); ok {
			t.Errorf("%q is not a bare name, got %q", name, got)
		}
	}
	if _, ok := untrustedMountDir("codex", dir, "", stat); !ok {
		t.Error("a bare name is looked up")
	}
}
