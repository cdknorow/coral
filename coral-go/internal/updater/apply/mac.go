package apply

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/subgentic/coral/internal/windowmark"
)

// The macOS part of the apply (#5231 section 4). Every tool is run by its absolute path.
const (
	hdiutil    = "/usr/bin/hdiutil"
	ditto      = "/usr/bin/ditto"
	codesign   = "/usr/bin/codesign"
	spctl      = "/usr/sbin/spctl"
	xcrun      = "/usr/bin/xcrun"
	open       = "/usr/bin/open"
	plistBuddy = "/usr/libexec/PlistBuddy"
	psTool     = "/bin/ps"
)

// Mac is the macOS Platform.
type Mac struct {
	Run Runner
	// TeamIDs are the pinned Developer ID team identifiers (verify.Pins.MacTeamIDs); with none, nothing is installed.
	TeamIDs []string
	// Exe is the running program's path (os.Executable); Parent is the pid of the process that started it (os.Getppid at start).
	Exe    string
	Parent int
	// WaitFor is how long Coral gets to end; HealthFor how long the new install must stay up. Zero values take the defaults.
	WaitFor, HealthFor time.Duration
	// Prefixes are the Homebrew prefixes to look in (default /opt/homebrew, /usr/local and $HOMEBREW_PREFIX).
	Prefixes []string
	// DataDir is Coral's data folder: the relaunch after an update opens the window again only if it was open (windowmark).
	DataDir string
	// Self is this program's pid, excluded when looking for Coral processes in the install (it runs from inside it).
	Self int
	// SwapSupported probes whether parent's volume has an atomic swap (a test replaces it).
	SwapSupported func(parent string) bool
	// Sleep is time.Sleep (a test replaces it).
	Sleep func(time.Duration)
}

var _ Platform = (*Mac)(nil)

func (m *Mac) sleep(d time.Duration) {
	if m.Sleep != nil {
		m.Sleep(d)
		return
	}
	time.Sleep(d)
}

func (m *Mac) Pinned() bool { return len(m.TeamIDs) > 0 }

var bundleName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,60}\.app$`)

// Install is the .app this program lives in: <name>.app/Contents/MacOS/coral-update, symlinks resolved. Anything else (a development build run from a
// build folder) is an error: there is nothing to update.
func (m *Mac) Install() (string, error) {
	exe := m.Exe
	if exe == "" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return "", err
		}
	}
	real, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", err
	}
	macos := filepath.Dir(real)
	contents := filepath.Dir(macos)
	app := filepath.Dir(contents)
	if filepath.Base(macos) != "MacOS" || filepath.Base(contents) != "Contents" || !bundleName.MatchString(filepath.Base(app)) {
		return "", errors.New("this program does not run from inside an app bundle")
	}
	return app, nil
}

func (m *Mac) prefixes() []string {
	if m.Prefixes != nil {
		return m.Prefixes
	}
	p := []string{"/opt/homebrew", "/usr/local"}
	if h := os.Getenv("HOMEBREW_PREFIX"); h != "" {
		p = append(p, h)
	}
	return p
}

// Unsupported: a Homebrew install (the Caskroom's Coral.app link points at the running bundle; the owner and the writability of the app cannot
// tell it from a dragged copy, #5231 section 9), and a volume without an atomic swap.
func (m *Mac) Unsupported(install string) string {
	real, err := filepath.EvalSymlinks(install)
	if err != nil {
		return "unreadable_install"
	}
	for _, prefix := range m.prefixes() {
		links, _ := filepath.Glob(filepath.Join(prefix, "Caskroom", "coral", "*", "Coral.app"))
		for _, l := range links {
			if target, err := filepath.EvalSymlinks(l); err == nil && target == real {
				return "homebrew"
			}
		}
	}
	probe := m.SwapSupported
	if probe == nil {
		probe = swapSupported
	}
	if !probe(filepath.Dir(install)) {
		return "no_atomic_swap"
	}
	return ""
}

// Writable: the user can create files next to the install and in it.
func (m *Mac) Writable(install string) bool {
	for _, d := range []string{filepath.Dir(install), install} {
		f, err := os.CreateTemp(d, ".coral-update-probe-*")
		if err != nil {
			return false
		}
		name := f.Name()
		f.Close()
		os.Remove(name)
	}
	return true
}

func newName(install string) string {
	return filepath.Join(filepath.Dir(install), "."+filepath.Base(install)+".new")
}

// Build mounts the package read-only, copies the app out of it with ditto to a hidden sibling of the install (the same volume, so the swap is
// atomic) and detaches. Only the app is copied: nothing else on the image comes along.
func (m *Mac) Build(ctx context.Context, pkg, install, work string) (string, func(), error) {
	mnt := filepath.Join(work, "mnt")
	if err := os.Mkdir(mnt, 0o700); err != nil {
		return "", nil, err
	}
	if _, err := m.Run.Run(ctx, hdiutil, "attach", "-nobrowse", "-readonly", "-noautoopen", "-noverify", "-mountpoint", mnt, pkg); err != nil {
		return "", nil, fmt.Errorf("mounting the package: %w", err)
	}
	detached := false
	detach := func() {
		if !detached {
			detached = true
			_, _ = m.Run.Run(context.WithoutCancel(ctx), hdiutil, "detach", "-force", mnt)
		}
	}
	src := filepath.Join(mnt, filepath.Base(install))
	if fi, err := os.Lstat(src); err != nil || !fi.IsDir() {
		detach()
		return "", nil, fmt.Errorf("the package has no %s", filepath.Base(install))
	}
	dst := newName(install)
	if err := os.RemoveAll(dst); err != nil {
		detach()
		return "", nil, err
	}
	if _, err := m.Run.Run(ctx, ditto, "--noqtn", src, dst); err != nil {
		detach()
		_ = os.RemoveAll(dst)
		return "", nil, fmt.Errorf("copying the app: %w", err)
	}
	detach()
	return dst, func() { detach() }, nil
}

var teamID = regexp.MustCompile(`^[A-Z0-9]{10}$`)

// teamRequirement is the code requirement "signed by an Apple-anchored Developer ID certificate whose organizational unit is one of teamIDs".
func teamRequirement(teamIDs []string) (string, error) {
	var ou []string
	for _, id := range teamIDs {
		if !teamID.MatchString(id) {
			return "", fmt.Errorf("%q is not a team identifier", id)
		}
		ou = append(ou, `certificate leaf[subject.OU] = "`+id+`"`)
	}
	if len(ou) == 0 {
		return "", errors.New("no team is pinned")
	}
	return "=anchor apple generic and (" + strings.Join(ou, " or ") + ")", nil
}

var teamIDLine = regexp.MustCompile(`(?m)^TeamIdentifier=([A-Z0-9]{10})$`)

// Verify checks the copy where it will run (#5231 section 4): the signature seals every file of the bundle (--deep --strict refuses an extra
// one), the system assesses it, the team is one of the pinned ones, the notarization ticket is stapled, and the bundle is the approved version.
func (m *Mac) Verify(ctx context.Context, newPath, version string) error {
	if _, err := m.Run.Run(ctx, codesign, "--verify", "--deep", "--strict", "--verbose=2", newPath); err != nil {
		return fmt.Errorf("codesign: %w", err)
	}
	if _, err := m.Run.Run(ctx, spctl, "--assess", "--type", "execute", newPath); err != nil {
		return fmt.Errorf("spctl: %w", err)
	}
	out, err := m.Run.Run(ctx, codesign, "-dv", "--verbose=4", newPath)
	if err != nil {
		return fmt.Errorf("codesign -dv: %w", err)
	}
	// The team is decided by the system's own requirement check on the certificate (never by what the display text says: the signing identifier is
	// free text, and one that holds a newline and "TeamIdentifier=<a pinned team>" puts that line into this output, #5338). The text is only
	// read as a second opinion, and two team lines are refused.
	req, err := teamRequirement(m.TeamIDs)
	if err != nil {
		return err
	}
	if _, err := m.Run.Run(ctx, codesign, "--verify", "--deep", "--strict", "-R", req, newPath); err != nil {
		return fmt.Errorf("the app is not signed by a pinned team: %w", err)
	}
	all := teamIDLine.FindAllStringSubmatch(out, -1)
	if len(all) != 1 {
		return fmt.Errorf("the app names %d team identifiers, not one", len(all))
	}
	pinned := false
	for _, id := range m.TeamIDs {
		if id == all[0][1] {
			pinned = true
		}
	}
	if !pinned {
		return fmt.Errorf("the app is signed by team %s, which is not pinned", all[0][1])
	}
	if _, err := m.Run.Run(ctx, xcrun, "stapler", "validate", newPath); err != nil {
		return fmt.Errorf("the notarization ticket is not stapled: %w", err)
	}
	got, err := m.Version(newPath)
	if err != nil {
		return err
	}
	if got != version {
		return fmt.Errorf("the app is version %q, the approved one is %q", got, version)
	}
	return nil
}

// Version is the bundle's CFBundleShortVersionString.
func (m *Mac) Version(path string) (string, error) {
	out, err := m.Run.Run(context.Background(), plistBuddy, "-c", "Print :CFBundleShortVersionString", filepath.Join(path, "Contents", "Info.plist"))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// Swap exchanges two bundles with renamex_np(RENAME_SWAP) (swap_darwin.go).
func (m *Mac) Swap(a, b string) error { return swapPaths(a, b) }

// CanRestart: the app is started with open, which every Mac has; nothing is recorded for it (#5419).
func (m *Mac) CanRestart(string) string {
	if _, err := os.Stat(open); err != nil {
		return "no_launcher"
	}
	return ""
}

func (m *Mac) Start(ctx context.Context, install string) error {
	// The relaunch reopens what the person had (#5827): the window, if it was open when the update began; the tray alone stays in the background (--no-browser), with no window
	// popping up. The window opens when the app is opened again.
	args := []string{install}
	if !windowmark.Present(m.DataDir) {
		args = append(args, "--args", "--no-browser")
	}
	_, err := m.Run.Run(ctx, open, args...)
	return err
}

// processes lists the commands of the running processes (pid and command).
func (m *Mac) processes(ctx context.Context) (map[int]string, error) {
	out, err := m.Run.Run(ctx, psTool, "-axo", "pid=,command=")
	if err != nil {
		return nil, err
	}
	res := map[int]string{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		sp := strings.IndexByte(line, ' ')
		if sp <= 0 {
			continue
		}
		var pid int
		if _, err := fmt.Sscanf(line[:sp], "%d", &pid); err == nil {
			res[pid] = strings.TrimSpace(line[sp:])
		}
	}
	return res, nil
}

func (m *Mac) running(ctx context.Context, install string) (int, error) {
	procs, err := m.processes(ctx)
	if err != nil {
		return 0, err
	}
	prefix := install + "/Contents/MacOS/"
	n := 0
	for pid, cmd := range procs {
		if pid != m.Self && pid != os.Getpid() && strings.HasPrefix(cmd, prefix) {
			n++
		}
	}
	return n, nil
}

// Running: a process of the install is running now.
func (m *Mac) Running(install string) bool {
	n, err := m.running(context.Background(), install)
	return err == nil && n > 0
}

// Healthy: a process of the install is running, and still is a few seconds later.
func (m *Mac) Healthy(ctx context.Context, install string) error {
	deadline := m.HealthFor
	if deadline == 0 {
		deadline = 45 * time.Second
	}
	end := time.Now().Add(deadline)
	seen := 0
	for time.Now().Before(end) {
		n, err := m.running(ctx, install)
		if err != nil {
			return err
		}
		if n > 0 {
			seen++
			if seen >= 3 {
				return nil
			}
		} else {
			seen = 0
		}
		m.sleep(2 * time.Second)
	}
	return errors.New("the new install did not stay up")
}

func (m *Mac) Show(ctx context.Context, pkg string) error {
	_, err := m.Run.Run(ctx, open, pkg)
	return err
}

// WaitCoralGone: the process that started this one (the server, which exited to be replaced) is gone, and no process of the install is running.
func (m *Mac) WaitCoralGone(ctx context.Context, install string) bool {
	wait := m.WaitFor
	if wait == 0 {
		wait = 2 * time.Minute
	}
	end := time.Now().Add(wait)
	for {
		parentGone := m.Parent <= 1 || !pidAlive(m.Parent)
		if parentGone {
			if n, err := m.running(ctx, install); err == nil && n == 0 {
				return true
			}
		}
		if time.Now().After(end) || ctx.Err() != nil {
			return false
		}
		m.sleep(time.Second)
	}
}
