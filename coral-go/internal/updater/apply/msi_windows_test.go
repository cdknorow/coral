//go:build windows

package apply

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// #162 D: the held package can't be written, renamed or deleted by any process while it is held, and a package that isn't the verified one,
// or that sits behind a link in the updates folder, is refused before anything uses it.
func TestAHeldPackageCannotBeChangedAndAChangedOneIsRefused(t *testing.T) {
	data := t.TempDir()
	dir := filepath.Join(data, "updates", "apply-msi")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	pkg := filepath.Join(dir, "Coral-1.4.130-x64.msi")
	body := []byte("the verified package")
	require.NoError(t, os.WriteFile(pkg, body, 0o600))
	w := &WindowsMSI{DataDir: data}

	h, err := w.Hold(pkg, msiSum(body))
	require.NoError(t, err)
	_, werr := os.OpenFile(pkg, os.O_WRONLY, 0)
	assert.Error(t, werr, "no one can open it for writing while it is held")
	assert.Error(t, os.Rename(pkg, pkg+".moved"), "or rename it")
	assert.Error(t, os.Remove(pkg), "or delete it")
	r, rerr := os.Open(pkg)
	require.NoError(t, rerr, "reading it, as msiexec does, is allowed")
	r.Close()
	require.NoError(t, h.Close())
	require.NoError(t, os.WriteFile(pkg, []byte("changed after the copy"), 0o600), "free again once the hold ends")

	_, err = w.Hold(pkg, msiSum(body))
	assert.Error(t, err, "a package that isn't the verified one is refused")

	// A junction in the updates folder that leads somewhere else: refused.
	elsewhere := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(elsewhere, "Coral-1.4.130-x64.msi"), body, 0o600))
	junction := filepath.Join(data, "updates", "linked")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", junction, elsewhere).CombinedOutput(); err != nil {
		t.Skipf("no junction here: %v %s", err, out)
	}
	_, err = w.Hold(filepath.Join(junction, "Coral-1.4.130-x64.msi"), msiSum(body))
	assert.Error(t, err, "a package behind a junction is refused")
}

// (iv) for real: an unsigned file is unsigned (it then goes by the sums and the bundle), and a validly signed program of another publisher is
// refused, Microsoft's own included (its chain isn't Trusted Signing's, and it isn't Subgentic LLC).
func TestAuthenticodeOnRealFiles(t *testing.T) {
	data := t.TempDir()
	unsigned := filepath.Join(data, "unsigned.msi")
	body := []byte("not a signed file")
	require.NoError(t, os.WriteFile(unsigned, body, 0o600))
	w := &WindowsMSI{DataDir: data}
	h, err := w.Hold(unsigned, msiSum(body))
	require.NoError(t, err)
	signed, err := w.Authenticode(h)
	h.Close()
	assert.False(t, signed)
	assert.NoError(t, err)

	tried := 0
	for _, p := range []string{
		filepath.Join(os.Getenv("ProgramFiles(x86)"), `Microsoft\Edge\Application\msedge.exe`),
		filepath.Join(os.Getenv("ProgramFiles"), `Git\git-bash.exe`),
	} {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		tried++
		h, err := w.Hold(p, msiSum(b))
		require.NoError(t, err, p)
		signed, err := w.Authenticode(h)
		h.Close()
		assert.True(t, signed, p)
		assert.Error(t, err, "%s is validly signed, by someone else: refused", p)
		t.Logf("%s: %v", filepath.Base(p), err)
	}
	if tried == 0 {
		t.Log("no signed program of another publisher found to check the refusal against")
	}
}

// The processes of a folder are found by their image (the wait for Coral to end), msiexec comes from the system folder Windows reports (E),
// and the restarted Coral doesn't get the server's CORAL_* values.
func TestTheWindowsMSIHelpers(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "stand-in.exe")
	src, err := exec.LookPath("ping.exe")
	require.NoError(t, err)
	b, err := os.ReadFile(src)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(exe, b, 0o700))
	cmd := exec.Command(exe, "-n", "30", "127.0.0.1")
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	w := &WindowsMSI{WaitFor: 2 * time.Second}
	procs, err := w.running(dir)
	require.NoError(t, err)
	assert.Contains(t, procs, "stand-in.exe")
	assert.False(t, w.WaitCoralGone(context.Background(), dir), "a process of the folder is still running")
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	assert.True(t, w.WaitCoralGone(context.Background(), dir))

	p, err := msiexecPath()
	require.NoError(t, err)
	sys, _ := windows.GetSystemDirectory()
	assert.Equal(t, filepath.Join(sys, "msiexec.exe"), p)

	env := trayEnv([]string{"PATH=x", "CORAL_SESSION_TOKEN=secret", "coral_url=http://x", "CORAL_DATA_DIR=d", "USERPROFILE=u"})
	assert.Equal(t, []string{"PATH=x", "CORAL_DATA_DIR=d", "USERPROFILE=u"}, env)

	assert.True(t, underFold(`C:\Program Files (x86)\Coral\coral.exe`, `c:\program files (x86)\coral`))
	assert.False(t, underFold(`C:\Program Files (x86)\CoralEvil\coral.exe`, `C:\Program Files (x86)\Coral`))
}

// The installed Coral, read only (skipped where none is installed): its product is registered for Coral's UpgradeCode, its folder is under
// Program Files, so it is supported.
func TestTheInstalledCoralIsFoundByItsUpgradeCode(t *testing.T) {
	w := &WindowsMSI{}
	product, err := w.RegisteredProduct()
	if err != nil {
		t.Skip("no Coral installed by its MSI here")
	}
	assert.True(t, strings.HasPrefix(product, "{") && len(product) == 38, product)
	install, err := w.Install()
	require.NoError(t, err)
	assert.Equal(t, "", w.Unsupported(install), install)
	_, err = os.Stat(filepath.Join(install, "coral.exe"))
	assert.NoError(t, err)
}
