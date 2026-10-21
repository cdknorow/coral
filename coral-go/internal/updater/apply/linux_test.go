//go:build !windows

// Not built on Windows: these test the Linux and macOS apply (POSIX file modes, renames over open files, symlinks, a staged record read back by path); the Windows apply is a separate piece (journal, MSI hand-off), #5395.

package apply

import (
	"archive/tar"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/updater/runrecord"
)

// elfHeader is the smallest ELF a debug/elf parse accepts: a 64-bit little-endian header with no program or section headers.
func elfHeader(machine uint16, class byte) []byte {
	h := make([]byte, 64)
	copy(h, []byte{0x7f, 'E', 'L', 'F', class, 1, 1})
	binary.LittleEndian.PutUint16(h[16:], 2)       // ET_EXEC
	binary.LittleEndian.PutUint16(h[18:], machine) // e_machine
	binary.LittleEndian.PutUint32(h[20:], 1)
	if class == 2 {
		binary.LittleEndian.PutUint16(h[52:], 64) // e_ehsize
	}
	return h
}

const (
	emX86_64  = 62
	emAarch64 = 183
)

func linuxInstall(t *testing.T, programs []string, machine uint16) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "coral-bin")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	for _, p := range programs {
		require.NoError(t, os.WriteFile(filepath.Join(dir, p), elfHeader(machine, 2), 0o755))
	}
	return dir
}

func newLinux(t *testing.T) *Linux {
	t.Helper()
	return &Linux{DataDir: t.TempDir(), Arch: "amd64", Manager: func() bool { return false }, ManagedPrefixes: []string{},
		SwapSupported: func(string) bool { return true }, Sleep: func(time.Duration) {}, ProcDir: t.TempDir(),
		Exec: func(ctx context.Context, env []string, name string, args ...string) (string, error) {
			return "coral 1.4.130\n", nil
		}}
}

func TestLinuxProgramsAreTheReleasePackageSet(t *testing.T) {
	b, err := os.ReadFile("../../../../tests/release/commands.txt")
	require.NoError(t, err)
	var cmds []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			cmds = append(cmds, l)
		}
	}
	require.NotEmpty(t, cmds)
	for _, c := range cmds {
		assert.True(t, isProgram(c), "%s is a command of every package, so the apply must allow and require it", c)
	}
	assert.True(t, isProgram("coral-update"))
	assert.False(t, isProgram("sh"))
	assert.False(t, isProgram("coral-tray"), "the GUI programs are macOS and Windows")
}

func TestLinuxInstallIsTheFolderOfCoralUpdate(t *testing.T) {
	dir := linuxInstall(t, linuxPrograms, emX86_64)
	l := newLinux(t)
	l.Exe = filepath.Join(dir, "coral-update")
	got, err := l.Install()
	require.NoError(t, err)
	real, _ := filepath.EvalSymlinks(dir)
	assert.Equal(t, real, got)
	l.Exe = filepath.Join(dir, "coral")
	_, err = l.Install()
	assert.Error(t, err, "only coral-update's own folder")
}

func TestLinuxRefusesWhatItDoesNotOwn(t *testing.T) {
	l := newLinux(t)
	own := linuxInstall(t, linuxPrograms, emX86_64)
	assert.Empty(t, l.Unsupported(own), "a folder of Coral's own programs")

	// A package manager's folder.
	l.ManagedPrefixes = []string{filepath.Dir(own)}
	assert.Equal(t, "package_manager", l.Unsupported(own))
	l.ManagedPrefixes = []string{}
	l2 := newLinux(t)
	l2.ManagedPrefixes = nil
	assert.Equal(t, "package_manager", l2.Unsupported("/usr/bin"), "the default prefixes")
	assert.Equal(t, "package_manager", l2.Unsupported("/usr/local/bin"))
	assert.Equal(t, "package_manager", l2.Unsupported("/home/linuxbrew/.linuxbrew/bin"))

	// A folder shared with other tools (a home bin): not a folder to replace.
	shared := linuxInstall(t, append([]string{"ripgrep"}, linuxPrograms...), emX86_64)
	assert.Equal(t, "shared_folder", l.Unsupported(shared))
	sub := linuxInstall(t, linuxPrograms, emX86_64)
	require.NoError(t, os.Mkdir(filepath.Join(sub, "plugins"), 0o755))
	assert.Equal(t, "shared_folder", l.Unsupported(sub), "a sub folder is not Coral's")
	link := linuxInstall(t, linuxPrograms, emX86_64)
	require.NoError(t, os.Symlink("/bin/sh", filepath.Join(link, "launch-coral-x")))
	assert.Equal(t, "shared_folder", l.Unsupported(link))
	nocoral := linuxInstall(t, []string{"coral-update"}, emX86_64)
	assert.Equal(t, "shared_folder", l.Unsupported(nocoral), "no coral in it")

	// Another user's folder (a root-owned extraction).
	l.Uid = os.Getuid() + 1
	assert.Equal(t, "not_owned", l.Unsupported(own))
	l.Uid = 0

	// A versioned install behind a "current" link swaps a link, not the folder: not built yet.
	linked := linuxInstall(t, linuxPrograms, emX86_64)
	require.NoError(t, os.Symlink(linked, filepath.Join(filepath.Dir(linked), "current")))
	assert.Equal(t, "symlinked_install", l.Unsupported(linked))

	l.SwapSupported = func(string) bool { return false }
	assert.Equal(t, "no_atomic_swap", l.Unsupported(own))
}

func TestLinuxWritable(t *testing.T) {
	l := newLinux(t)
	own := linuxInstall(t, linuxPrograms, emX86_64)
	assert.True(t, l.Writable(own))
	if os.Getuid() != 0 {
		require.NoError(t, os.Chmod(own, 0o555))
		t.Cleanup(func() { os.Chmod(own, 0o755) })
		assert.False(t, l.Writable(own))
	}
}

func TestLinuxBuildUnpacksBesideTheInstallAndVerifyChecksEveryProgram(t *testing.T) {
	l := newLinux(t)
	install := linuxInstall(t, linuxPrograms, emX86_64)
	var entries []tarEntry
	for _, p := range linuxPrograms {
		entries = append(entries, tarEntry{name: p, body: string(elfHeader(emX86_64, 2)), mode: 0o755})
	}
	entries = append(entries, tarEntry{name: "THIRD_PARTY_NOTICES.md", body: "notices", mode: 0o644})
	pkg := makeTarGz(t, entries...)
	newPath, cleanup, err := l.Build(context.Background(), pkg, install, t.TempDir())
	require.NoError(t, err)
	defer cleanup()
	assert.Equal(t, filepath.Join(filepath.Dir(install), ".coral-bin.new"), newPath, "a hidden sibling: the same volume")
	require.NoError(t, l.Verify(context.Background(), newPath, "1.4.130"))

	var gotEnv []string
	var gotName string
	l.Exec = func(ctx context.Context, env []string, name string, args ...string) (string, error) {
		gotEnv, gotName = env, name
		return "coral 1.4.130\n", nil
	}
	require.NoError(t, l.Verify(context.Background(), newPath, "1.4.130"))
	assert.Equal(t, filepath.Join(newPath, "coral"), gotName)
	for _, e := range gotEnv {
		assert.False(t, strings.HasPrefix(e, "SSH_") || strings.HasPrefix(e, "CORAL_CLOUD"), "no environment of the user's: %s", e)
	}
	assert.Contains(t, gotEnv, "CORAL_AUTO_UPDATE=off")

	// The folder sat beside the install; a file replaced by another valid program passes every other check but not the hash of what was unpacked.
	require.NoError(t, os.WriteFile(filepath.Join(newPath, "coral-agent"), append(elfHeader(emX86_64, 2), 'x'), 0o755))
	assert.Error(t, l.Verify(context.Background(), newPath, "1.4.130"), "a file changed after it was unpacked")
	require.NoError(t, os.WriteFile(filepath.Join(newPath, "coral-agent"), elfHeader(emX86_64, 2), 0o755))
	require.NoError(t, l.Verify(context.Background(), newPath, "1.4.130"))
	require.NoError(t, os.WriteFile(filepath.Join(newPath, "NOTICE"), []byte("added"), 0o644))
	assert.Error(t, l.Verify(context.Background(), newPath, "1.4.130"), "a file added after it was unpacked")
	require.NoError(t, os.Remove(filepath.Join(newPath, "NOTICE")))

	// Refusals: each makes Verify fail.
	for name, change := range map[string]func(dir string){
		"a program is missing":   func(d string) { os.Remove(filepath.Join(d, "coral-update")) },
		"a program is a script":  func(d string) { os.WriteFile(filepath.Join(d, "coral-board"), []byte("#!/bin/sh\n"), 0o755) },
		"for another machine":    func(d string) { os.WriteFile(filepath.Join(d, "coral"), elfHeader(emAarch64, 2), 0o755) },
		"a 32-bit program":       func(d string) { os.WriteFile(filepath.Join(d, "coral"), elfHeader(emX86_64, 1), 0o755) },
		"not executable":         func(d string) { os.Chmod(filepath.Join(d, "coral-agent"), 0o644) },
		"set-id":                 func(d string) { os.Chmod(filepath.Join(d, "coral-agent"), os.ModeSetuid|0o755) },
		"a link in the folder":   func(d string) { os.Symlink("/bin/sh", filepath.Join(d, "extra")) },
		"a folder in the folder": func(d string) { os.Mkdir(filepath.Join(d, "extra"), 0o755) },
	} {
		dir := t.TempDir()
		for _, p := range linuxPrograms {
			require.NoError(t, os.WriteFile(filepath.Join(dir, p), elfHeader(emX86_64, 2), 0o755))
		}
		change(dir)
		assert.Error(t, l.Verify(context.Background(), dir, "1.4.130"), name)
	}
	// The wrong version, an answer that is not one, a failing coral.
	dir := t.TempDir()
	for _, p := range linuxPrograms {
		require.NoError(t, os.WriteFile(filepath.Join(dir, p), elfHeader(emX86_64, 2), 0o755))
	}
	for name, out := range map[string]string{"another version": "coral 1.4.129\n", "development": "coral (development build)\n", "junk": "hello\n", "extra words": "coral 1.4.130 extra\n"} {
		l.Exec = func(context.Context, []string, string, ...string) (string, error) { return out, nil }
		assert.Error(t, l.Verify(context.Background(), dir, "1.4.130"), name)
	}
	l.Exec = func(context.Context, []string, string, ...string) (string, error) { return "", errors.New("exit 1") }
	assert.Error(t, l.Verify(context.Background(), dir, "1.4.130"))
	// 32-bit/arm machine mapping.
	l.Arch = "arm64"
	l.Exec = func(context.Context, []string, string, ...string) (string, error) { return "coral 1.4.130\n", nil }
	assert.Error(t, l.Verify(context.Background(), dir, "1.4.130"), "an amd64 package on an arm64 machine")
	arm := t.TempDir()
	for _, p := range linuxPrograms {
		require.NoError(t, os.WriteFile(filepath.Join(arm, p), elfHeader(emAarch64, 2), 0o755))
	}
	assert.NoError(t, l.Verify(context.Background(), arm, "1.4.130"))
}

func TestLinuxBuildRefusesABadPackageAndLeavesNothing(t *testing.T) {
	l := newLinux(t)
	install := linuxInstall(t, linuxPrograms, emX86_64)
	pkg := makeTarGz(t, tarEntry{name: "coral", typ: tar.TypeSymlink, linkname: "/etc/passwd"})
	_, _, err := l.Build(context.Background(), pkg, install, t.TempDir())
	require.Error(t, err)
	_, serr := os.Stat(filepath.Join(filepath.Dir(install), ".coral-bin.new"))
	assert.ErrorIs(t, serr, os.ErrNotExist)
}

func fakeProc(t *testing.T, root string, pids map[int]string) {
	t.Helper()
	require.NoError(t, os.RemoveAll(root))
	require.NoError(t, os.MkdirAll(root, 0o755))
	for pid, exe := range pids {
		d := filepath.Join(root, strconv.Itoa(pid))
		require.NoError(t, os.MkdirAll(d, 0o755))
		require.NoError(t, os.Symlink(exe, filepath.Join(d, "exe")))
	}
	require.NoError(t, os.MkdirAll(filepath.Join(root, "self"), 0o755))
}

func TestLinuxWaitsForCoralAndNeverStopsIt(t *testing.T) {
	l := newLinux(t)
	install := linuxInstall(t, linuxPrograms, emX86_64)
	l.Self = 100
	fakeProc(t, l.ProcDir, map[int]string{1: "/sbin/init", 4242: "/usr/bin/vim", 100: filepath.Join(install, "coral-update"), 31337: filepath.Join(install, "coral")})
	assert.Equal(t, 1, l.running(install), "only the other process of the install; the updater itself and strangers do not count")
	n := 0
	l.Sleep = func(time.Duration) {
		n++
		if n == 2 {
			fakeProc(t, l.ProcDir, map[int]string{1: "/sbin/init", 100: filepath.Join(install, "coral-update")})
		}
	}
	assert.True(t, l.WaitCoralGone(context.Background(), install))
	assert.GreaterOrEqual(t, n, 2)

	l = newLinux(t)
	l.WaitFor = 30 * time.Millisecond
	l.Sleep = func(time.Duration) { time.Sleep(10 * time.Millisecond) }
	fakeProc(t, l.ProcDir, map[int]string{31337: filepath.Join(install, "coral")})
	assert.False(t, l.WaitCoralGone(context.Background(), install), "it did not end in time")
	l.Parent = os.Getpid()
	fakeProc(t, l.ProcDir, nil)
	assert.False(t, l.WaitCoralGone(context.Background(), install), "a live parent is waited for")
}

func TestLinuxStartRestartsCoralTheWayTheServerRecordedItOrLeavesItToTheManager(t *testing.T) {
	l := newLinux(t)
	install := linuxInstall(t, linuxPrograms, emX86_64)
	var spawned, spawnedEnv []string
	var cwd string
	l.Spawn = func(argv, env []string, c string) error { spawned, spawnedEnv, cwd = argv, env, c; return nil }

	// A service manager restarts Coral itself: nothing is started.
	l.Manager = func() bool { return true }
	require.NoError(t, l.Start(context.Background(), install))
	assert.Empty(t, spawned)

	// Without a record there is no way to start it: an error (the apply rolls back).
	l.Manager = func() bool { return false }
	assert.Error(t, l.Start(context.Background(), install))

	dir := filepath.Join(l.DataDir, "updates")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	write := func(body string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, runrecord.File), []byte(body), 0o600))
	}
	write(`{"argv":["` + install + `/coral","--port","8420"],"cwd":"/srv/coral"}`)
	assert.Error(t, l.Start(context.Background(), install), "a record without an environment is not started with the updater's own (#5414)")
	assert.Empty(t, spawned)
	write(`{"argv":["` + install + `/coral","--port","8420"],"env":["PATH=/usr/bin"],"cwd":"/srv/coral"}`)
	require.NoError(t, l.Start(context.Background(), install))
	assert.Equal(t, []string{install + "/coral", "--port", "8420"}, spawned)
	assert.Equal(t, "/srv/coral", cwd)
	assert.Equal(t, []string{"PATH=/usr/bin"}, spawnedEnv)

	// A record edited to carry anything the allowlist does not name is filtered again when it is read: no LD_PRELOAD, token or URL with a password reaches Coral.
	write(`{"argv":["` + install + `/coral"],"env":["LD_PRELOAD=/tmp/x.so","ANTHROPIC_API_KEY=k","CORAL_CLOUD_URL=https://u:pw@hub.example","PATH=/usr/bin","HOME=/home/u"],"cwd":"/srv/coral"}`)
	require.NoError(t, l.Start(context.Background(), install))
	assert.Equal(t, []string{"PATH=/usr/bin", "HOME=/home/u"}, spawnedEnv)

	// A record with one: exactly that environment, nothing of the updater's own (#5410).
	write(`{"argv":["` + install + `/coral"],"env":["CORAL_DATA_DIR=/srv/data","PATH=/usr/bin"],"cwd":"/srv/coral"}`)
	require.NoError(t, l.Start(context.Background(), install))
	assert.Equal(t, []string{"CORAL_DATA_DIR=/srv/data", "PATH=/usr/bin"}, spawnedEnv)

	// A record that names anything else is not run.
	spawned = nil
	for name, body := range map[string]string{
		"another program": `{"argv":["/bin/sh","-c","evil"],"cwd":"/"}`,
		"another folder":  `{"argv":["/tmp/x/coral"],"cwd":"/"}`,
		"a relative path": `{"argv":["coral"],"cwd":"/"}`,
		"another name":    `{"argv":["` + install + `/coral-board"],"cwd":"/"}`,
		"no argv":         `{"argv":[],"cwd":"/"}`,
		"not json":        `{`,
	} {
		write(body)
		assert.Error(t, l.Start(context.Background(), install), name)
	}
	assert.Empty(t, spawned)
}

func TestLinuxHealthNeedsTheNewInstallToStayUp(t *testing.T) {
	l := newLinux(t)
	install := linuxInstall(t, linuxPrograms, emX86_64)
	l.HealthFor = time.Second
	l.Sleep = func(time.Duration) { time.Sleep(5 * time.Millisecond) }
	fakeProc(t, l.ProcDir, map[int]string{200: filepath.Join(install, "coral")})
	assert.NoError(t, l.Healthy(context.Background(), install))
	assert.True(t, l.Running(install))
	fakeProc(t, l.ProcDir, map[int]string{200: "/usr/bin/vim"})
	l.HealthFor = 60 * time.Millisecond
	assert.Error(t, l.Healthy(context.Background(), install))
	assert.False(t, l.Running(install))
	assert.True(t, l.Pinned())
	assert.Error(t, l.Show(context.Background(), "/p.tar.gz"))
}

// The real package carries a licenses/ tree: it is Coral's own (an install is not "shared" because of it), a link or device in it is not, and the
// folder checks and the hash recheck cover the tree.
func TestLinuxLicensesTreeIsCoralsOwnAndCheckedLikeTheRest(t *testing.T) {
	l := newLinux(t)
	install := linuxInstall(t, linuxPrograms, emX86_64)
	require.NoError(t, os.MkdirAll(filepath.Join(install, "licenses", "go", "mod@v1.0.0"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(install, "licenses", "go", "mod@v1.0.0", "LICENSE"), []byte("text"), 0o444))
	require.NoError(t, os.WriteFile(filepath.Join(install, "licenses", "THIRD_PARTY_NOTICES_MPL.md"), []byte("x"), 0o644))
	assert.Empty(t, l.Unsupported(install), "a tarball install with its licenses folder")

	require.NoError(t, os.Symlink("/etc/passwd", filepath.Join(install, "licenses", "go", "evil")))
	assert.Equal(t, "shared_folder", l.Unsupported(install), "a link inside licenses is not Coral's")
	require.NoError(t, os.Remove(filepath.Join(install, "licenses", "go", "evil")))
	assert.Empty(t, l.Unsupported(install))

	// A file called licenses, or a folder with another name, is not.
	other := linuxInstall(t, linuxPrograms, emX86_64)
	require.NoError(t, os.WriteFile(filepath.Join(other, "licenses"), []byte("x"), 0o644))
	assert.Equal(t, "shared_folder", l.Unsupported(other))
	other2 := linuxInstall(t, linuxPrograms, emX86_64)
	require.NoError(t, os.MkdirAll(filepath.Join(other2, "docs"), 0o755))
	assert.Equal(t, "shared_folder", l.Unsupported(other2))

	// Build and Verify with a tree: every file counts.
	var entries []tarEntry
	for _, p := range linuxPrograms {
		entries = append(entries, tarEntry{name: p, body: string(elfHeader(emX86_64, 2)), mode: 0o755})
	}
	entries = append(entries, tarEntry{name: "licenses/", typ: tar.TypeDir}, tarEntry{name: "licenses/go/", typ: tar.TypeDir},
		tarEntry{name: "licenses/go/a/", typ: tar.TypeDir}, tarEntry{name: "licenses/go/a/LICENSE", body: "mit", mode: 0o444},
		tarEntry{name: "licenses/NOTICE.md", body: "n", mode: 0o644})
	newPath, cleanup, err := l.Build(context.Background(), makeTarGz(t, entries...), install, t.TempDir())
	require.NoError(t, err)
	defer cleanup()
	require.NoError(t, l.Verify(context.Background(), newPath, "1.4.130"))
	lic := filepath.Join(newPath, "licenses", "go", "a", "LICENSE")
	require.NoError(t, os.WriteFile(lic, []byte("changed"), 0o644))
	assert.Error(t, l.Verify(context.Background(), newPath, "1.4.130"), "a licence file changed after the unpack")
	require.NoError(t, os.WriteFile(lic, []byte("mit"), 0o644))
	require.NoError(t, l.Verify(context.Background(), newPath, "1.4.130"))
	require.NoError(t, os.WriteFile(filepath.Join(newPath, "licenses", "extra"), []byte("x"), 0o644))
	assert.Error(t, l.Verify(context.Background(), newPath, "1.4.130"), "a file added to the tree")
	require.NoError(t, os.Remove(filepath.Join(newPath, "licenses", "extra")))
	require.NoError(t, os.Symlink("/etc/passwd", filepath.Join(newPath, "licenses", "go", "link")))
	assert.Error(t, l.Verify(context.Background(), newPath, "1.4.130"), "a link in the tree")
}
