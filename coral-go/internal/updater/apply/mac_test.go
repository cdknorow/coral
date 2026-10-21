//go:build !windows

// Not built on Windows: these test the Linux and macOS apply (POSIX file modes, renames over open files, symlinks, a staged record read back by path); the Windows apply is a separate piece (journal, MSI hand-off), #5395.

package apply

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRunner answers the system's tools from a table and records every command.
type fakeRunner struct {
	cmds    []string
	answers map[string]string // a command's first words -> its output
	fail    map[string]error
	onRun   func(name string, args []string)
}

func (f *fakeRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	line := name + " " + strings.Join(args, " ")
	f.cmds = append(f.cmds, line)
	if f.onRun != nil {
		f.onRun(name, args)
	}
	for prefix, err := range f.fail {
		if strings.HasPrefix(line, prefix) {
			return "", err
		}
	}
	for prefix, out := range f.answers {
		if strings.HasPrefix(line, prefix) {
			return out, nil
		}
	}
	return "", nil
}

func (f *fakeRunner) ran(prefix string) bool {
	for _, c := range f.cmds {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func newMac(t *testing.T) (*Mac, *fakeRunner, string) {
	t.Helper()
	root := t.TempDir()
	install := filepath.Join(root, "Coral.app")
	require.NoError(t, os.MkdirAll(filepath.Join(install, "Contents", "MacOS"), 0o755))
	fr := &fakeRunner{answers: map[string]string{
		"/usr/bin/codesign -dv":   "Executable=/x\nTeamIdentifier=ABCDE12345\n",
		"/usr/libexec/PlistBuddy": "1.4.130\n",
	}}
	m := &Mac{Run: fr, TeamIDs: []string{"ABCDE12345"}, SwapSupported: func(string) bool { return true }, Sleep: func(time.Duration) {}, Prefixes: []string{}}
	return m, fr, install
}

func TestMacInstallIsTheBundleTheProgramRunsFrom(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "Coral.app")
	exe := filepath.Join(app, "Contents", "MacOS", "coral-update")
	require.NoError(t, os.MkdirAll(filepath.Dir(exe), 0o755))
	require.NoError(t, os.WriteFile(exe, []byte("x"), 0o755))
	real, err := filepath.EvalSymlinks(app)
	require.NoError(t, err)
	m := &Mac{Exe: exe}
	got, err := m.Install()
	require.NoError(t, err)
	assert.Equal(t, real, got)

	// A development build run from a folder, or a program somewhere else in a bundle, has no install to update.
	for name, p := range map[string]string{
		"a build folder": filepath.Join(root, "bin", "coral-update"),
		"not MacOS":      filepath.Join(app, "Contents", "Resources", "coral-update"),
		"not an app":     filepath.Join(root, "Tool.pkg", "Contents", "MacOS", "coral-update"),
		"a odd name":     filepath.Join(root, "-rf.app", "Contents", "MacOS", "coral-update"),
	} {
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o755))
		_, err := (&Mac{Exe: p}).Install()
		assert.Error(t, err, name)
	}
	// A symlink to the program (a tool in /usr/local/bin) is resolved to the bundle.
	link := filepath.Join(root, "link-update")
	require.NoError(t, os.Symlink(exe, link))
	got, err = (&Mac{Exe: link}).Install()
	require.NoError(t, err)
	assert.Equal(t, real, got)
}

func TestMacBuildMountsReadOnlyCopiesOnlyTheAppBesideTheInstallAndAlwaysDetaches(t *testing.T) {
	m, fr, install := newMac(t)
	work := t.TempDir()
	fr.onRun = func(name string, args []string) {
		if name == hdiutil && args[0] == "attach" { // the "mounted" image holds the app
			mnt := args[len(args)-2]
			require.NoError(t, os.MkdirAll(filepath.Join(mnt, "Coral.app", "Contents"), 0o755))
		}
		if name == ditto { // the copy
			require.NoError(t, os.MkdirAll(args[len(args)-1], 0o755))
		}
	}
	newPath, cleanup, err := m.Build(context.Background(), "/work/Coral.v1.4.130.dmg", install, work)
	require.NoError(t, err)
	defer cleanup()
	assert.Equal(t, filepath.Join(filepath.Dir(install), ".Coral.app.new"), newPath, "a hidden sibling of the install: the same volume")
	mnt := filepath.Join(work, "mnt")
	assert.Equal(t, "/usr/bin/hdiutil attach -nobrowse -readonly -noautoopen -noverify -mountpoint "+mnt+" /work/Coral.v1.4.130.dmg", fr.cmds[0])
	assert.Equal(t, "/usr/bin/ditto --noqtn "+filepath.Join(mnt, "Coral.app")+" "+newPath, fr.cmds[1], "only the app is copied")
	assert.Equal(t, "/usr/bin/hdiutil detach -force "+mnt, fr.cmds[2])
	cleanup()
	assert.Len(t, fr.cmds, 3, "detached once")

	// The copy fails: the image is still detached and no half copy is left.
	m, fr, install = newMac(t)
	work = t.TempDir()
	fr.onRun = func(name string, args []string) {
		if name == hdiutil && args[0] == "attach" {
			require.NoError(t, os.MkdirAll(filepath.Join(args[len(args)-2], "Coral.app"), 0o755))
		}
	}
	fr.fail = map[string]error{"/usr/bin/ditto": errors.New("no space")}
	_, _, err = m.Build(context.Background(), "/p.dmg", install, work)
	require.Error(t, err)
	assert.True(t, fr.ran("/usr/bin/hdiutil detach"))
	_, serr := os.Stat(filepath.Join(filepath.Dir(install), ".Coral.app.new"))
	assert.ErrorIs(t, serr, os.ErrNotExist)

	// An image without the app: detached, refused.
	m, fr, install = newMac(t)
	_, _, err = m.Build(context.Background(), "/p.dmg", install, t.TempDir())
	require.Error(t, err)
	assert.True(t, fr.ran("/usr/bin/hdiutil detach"))
	// A mount that fails: nothing else runs.
	m, fr, install = newMac(t)
	fr.fail = map[string]error{"/usr/bin/hdiutil attach": errors.New("corrupt image")}
	_, _, err = m.Build(context.Background(), "/p.dmg", install, t.TempDir())
	require.Error(t, err)
	assert.False(t, fr.ran("/usr/bin/ditto"))
}

func TestMacVerifyChecksTheSignatureTheSignerTheTicketAndTheVersionOfTheCopyInPlace(t *testing.T) {
	m, fr, install := newMac(t)
	newPath := filepath.Join(filepath.Dir(install), ".Coral.app.new")
	require.NoError(t, m.Verify(context.Background(), newPath, "1.4.130"))
	assert.Equal(t, []string{
		"/usr/bin/codesign --verify --deep --strict --verbose=2 " + newPath,
		"/usr/sbin/spctl --assess --type execute " + newPath,
		"/usr/bin/codesign -dv --verbose=4 " + newPath,
		"/usr/bin/codesign --verify --deep --strict -R =anchor apple generic and (certificate leaf[subject.OU] = \"ABCDE12345\") " + newPath,
		"/usr/bin/xcrun stapler validate " + newPath,
		"/usr/libexec/PlistBuddy -c Print :CFBundleShortVersionString " + filepath.Join(newPath, "Contents", "Info.plist"),
	}, fr.cmds, "each check runs on the copy, where it will run")

	for name, setup := range map[string]func(m *Mac, fr *fakeRunner){
		"the signature is broken": func(m *Mac, fr *fakeRunner) {
			fr.fail = map[string]error{"/usr/bin/codesign --verify": errors.New("a file is not sealed")}
		},
		"the system refuses it":  func(m *Mac, fr *fakeRunner) { fr.fail = map[string]error{"/usr/sbin/spctl": errors.New("rejected")} },
		"another team signed it": func(m *Mac, fr *fakeRunner) { fr.answers["/usr/bin/codesign -dv"] = "TeamIdentifier=ZZZZZ99999\n" },
		"no team is named":       func(m *Mac, fr *fakeRunner) { fr.answers["/usr/bin/codesign -dv"] = "Signature=adhoc\n" },
		"a team named in a comment": func(m *Mac, fr *fakeRunner) {
			fr.answers["/usr/bin/codesign -dv"] = "Authority=x TeamIdentifier=ABCDE12345\n"
		},
		"two team lines (a signing identifier with a newline in it)": func(m *Mac, fr *fakeRunner) {
			fr.answers["/usr/bin/codesign -dv"] = "Identifier=x\nTeamIdentifier=ABCDE12345\nTeamIdentifier=ZZZZZ99999\n"
		},
		"the certificate is not a pinned team's": func(m *Mac, fr *fakeRunner) {
			fr.fail = map[string]error{"/usr/bin/codesign --verify --deep --strict -R": errors.New("failed to satisfy the requirement")}
		},
		"the ticket is not stapled": func(m *Mac, fr *fakeRunner) { fr.fail = map[string]error{"/usr/bin/xcrun": errors.New("no ticket")} },
		"it is another version":     func(m *Mac, fr *fakeRunner) { fr.answers["/usr/libexec/PlistBuddy"] = "1.4.129\n" },
		"no version": func(m *Mac, fr *fakeRunner) {
			fr.fail = map[string]error{"/usr/libexec/PlistBuddy": errors.New("no key")}
		},
		"no team is pinned":         func(m *Mac, fr *fakeRunner) { m.TeamIDs = nil },
		"a pinned team is a prefix": func(m *Mac, fr *fakeRunner) { m.TeamIDs = []string{"ABCDE1234"} },
	} {
		m, fr, install := newMac(t)
		setup(m, fr)
		err := m.Verify(context.Background(), filepath.Join(filepath.Dir(install), ".Coral.app.new"), "1.4.130")
		assert.Error(t, err, name)
	}
	// A set of pinned teams: any one of them.
	m, _, install = newMac(t)
	m.TeamIDs = []string{"OLDTEAM123", "ABCDE12345"}
	assert.NoError(t, m.Verify(context.Background(), filepath.Join(filepath.Dir(install), ".Coral.app.new"), "1.4.130"))
}

func TestMacRefusesAHomebrewInstallAndAVolumeWithoutASwap(t *testing.T) {
	m, _, install := newMac(t)
	assert.Empty(t, m.Unsupported(install), "a dragged copy")

	// Homebrew moves the app into /Applications and leaves a link to it in the Caskroom: that link marks it.
	prefix := t.TempDir()
	caskroom := filepath.Join(prefix, "Caskroom", "coral", "1.4.126")
	require.NoError(t, os.MkdirAll(caskroom, 0o755))
	require.NoError(t, os.Symlink(install, filepath.Join(caskroom, "Coral.app")))
	m.Prefixes = []string{t.TempDir(), prefix}
	assert.Equal(t, "homebrew", m.Unsupported(install))
	// A Caskroom entry that points elsewhere is not this install.
	other := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(other, "Coral.app"), 0o755))
	require.NoError(t, os.Remove(filepath.Join(caskroom, "Coral.app")))
	require.NoError(t, os.Symlink(filepath.Join(other, "Coral.app"), filepath.Join(caskroom, "Coral.app")))
	assert.Empty(t, m.Unsupported(install))

	m.SwapSupported = func(string) bool { return false }
	assert.Equal(t, "no_atomic_swap", m.Unsupported(install))
}

func TestMacWritable(t *testing.T) {
	m, _, install := newMac(t)
	assert.True(t, m.Writable(install))
	if os.Getuid() != 0 {
		require.NoError(t, os.Chmod(filepath.Dir(install), 0o555))
		t.Cleanup(func() { os.Chmod(filepath.Dir(install), 0o755) })
		assert.False(t, m.Writable(install), "a folder the user cannot write to")
	}
}

func TestMacWaitsForCoralToEndAndNeverStopsIt(t *testing.T) {
	m, fr, install := newMac(t)
	m.Self = 1
	proc := func(pid int, cmd string) string { return strings.Repeat(" ", 3) + itoa(pid) + " " + cmd + "\n" }
	coral := install + "/Contents/MacOS/coral --host 127.0.0.1"
	// Running for two polls, gone at the third.
	polls := 0
	fr.onRun = func(name string, args []string) {
		if name == psTool {
			polls++
			out := proc(1, "/sbin/launchd") + proc(4242, "/usr/bin/vim notes.txt")
			if polls <= 2 {
				out += proc(31337, coral)
			}
			fr.answers[psTool] = out
		}
	}
	assert.True(t, m.WaitCoralGone(context.Background(), install))
	assert.GreaterOrEqual(t, polls, 3)
	for _, c := range fr.cmds {
		assert.NotContains(t, c, "kill", "it never stops Coral")
	}

	// It does not end in time: false, and the caller changes nothing.
	m, fr, install = newMac(t)
	m.WaitFor = 50 * time.Millisecond
	m.Sleep = func(time.Duration) { time.Sleep(20 * time.Millisecond) }
	fr.answers[psTool] = proc(31337, install+"/Contents/MacOS/coral")
	assert.False(t, m.WaitCoralGone(context.Background(), install))

	// A live parent (the server that started the updater) is waited for even when no process of the install shows.
	m, fr, install = newMac(t)
	m.Parent = os.Getpid()
	m.WaitFor = 50 * time.Millisecond
	m.Sleep = func(time.Duration) { time.Sleep(20 * time.Millisecond) }
	assert.False(t, m.WaitCoralGone(context.Background(), install))
	// A process outside the install does not count, and the updater itself (it runs from inside the install) is not Coral.
	m, fr, install = newMac(t)
	m.Self = 777
	fr.answers[psTool] = proc(777, install+"/Contents/MacOS/coral-update apply") + proc(5, "/Applications/Other.app/Contents/MacOS/coral")
	assert.True(t, m.WaitCoralGone(context.Background(), install))
}

func itoa(n int) string {
	return strings.TrimSpace(strings.Join(strings.Fields(strings.Repeat(" ", 0)+fmtInt(n)), ""))
}

func fmtInt(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

func TestMacHealthNeedsTheNewInstallToStayUp(t *testing.T) {
	m, fr, install := newMac(t)
	m.HealthFor = time.Second
	m.Sleep = func(time.Duration) { time.Sleep(5 * time.Millisecond) }
	fr.answers[psTool] = " 100 " + install + "/Contents/MacOS/coral\n"
	assert.NoError(t, m.Healthy(context.Background(), install))

	m, fr, install = newMac(t)
	m.HealthFor = 100 * time.Millisecond
	m.Sleep = func(time.Duration) { time.Sleep(5 * time.Millisecond) }
	fr.answers[psTool] = " 100 /usr/bin/vim\n"
	assert.Error(t, m.Healthy(context.Background(), install), "never up")

	// Up once and gone again: not healthy.
	m, fr, install = newMac(t)
	m.HealthFor = 300 * time.Millisecond
	m.Sleep = func(time.Duration) { time.Sleep(5 * time.Millisecond) }
	n := 0
	fr.onRun = func(name string, args []string) {
		if name == psTool {
			n++
			if n == 1 {
				fr.answers[psTool] = " 100 " + install + "/Contents/MacOS/coral\n"
			} else {
				fr.answers[psTool] = ""
			}
		}
	}
	assert.Error(t, m.Healthy(context.Background(), install))
}

func TestMacStartAndShowRunTheFixedTools(t *testing.T) {
	m, fr, install := newMac(t)
	require.NoError(t, m.Start(context.Background(), install))
	require.NoError(t, m.Show(context.Background(), "/p/Coral.v1.4.130.dmg"))
	// the relaunch after an update stays in the background: --no-browser (#5827)
	assert.Equal(t, []string{"/usr/bin/open " + install + " --args --no-browser", "/usr/bin/open /p/Coral.v1.4.130.dmg"}, fr.cmds)
	assert.True(t, m.Pinned())
	assert.False(t, (&Mac{}).Pinned(), "no team pinned, nothing installs")
}
