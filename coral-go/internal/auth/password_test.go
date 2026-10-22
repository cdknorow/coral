package auth

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/appmarker"
)

// #3949: the hash is argon2id with its parameters in the string; NFKC is applied; any non-empty
// password up to 4 KiB; "none set" costs and answers like a wrong password.
func TestPasswordHash(t *testing.T) {
	phc, err := HashPassword("correct horse")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(phc, "$argon2id$v=19$m=65536,t=3,p=1$"), phc)
	p, err := parsePHC(phc)
	require.NoError(t, err)
	assert.Len(t, p.salt, 16)
	assert.Len(t, p.key, 32)
	other, _ := HashPassword("correct horse")
	assert.NotEqual(t, phc, other, "a fresh salt each time")

	assert.True(t, VerifyPassword(phc, "correct horse"))
	assert.False(t, VerifyPassword(phc, "correct horsE"))
	assert.False(t, VerifyPassword(phc, ""))
	assert.False(t, VerifyPassword("", "correct horse"), "no password set: never a match")
	assert.False(t, VerifyPassword("$argon2id$garbage", "x"))

	// NFKC: a decomposed e-acute and the composed one are the same password; so is a full-width A.
	composed, decomposed := "caf\u00e9", "cafe\u0301"
	h, _ := HashPassword(composed)
	assert.True(t, VerifyPassword(h, decomposed))
	h, _ = HashPassword("\uff21bc")
	assert.True(t, VerifyPassword(h, "Abc"))

	// Any non-empty password: one character, a long pasted one; over 4 KiB is refused.
	h, err = HashPassword("x")
	require.NoError(t, err)
	assert.True(t, VerifyPassword(h, "x"))
	long := strings.Repeat("p", MaxPasswordBytes)
	h, err = HashPassword(long)
	require.NoError(t, err)
	assert.True(t, VerifyPassword(h, long))
	_, err = HashPassword(long + "p")
	assert.ErrorIs(t, err, ErrPasswordTooLong)
	_, err = HashPassword("")
	assert.ErrorIs(t, err, ErrPasswordEmpty)

	// A tampered file can't ask for an absurd cost.
	_, err = parsePHC("$argon2id$v=19$m=99999999,t=3,p=1$c2FsdHNhbHRzYWx0$a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2V5")
	assert.Error(t, err)
}

func TestPasswordFileIsPrivateAndCreateIsAtomic(t *testing.T) {
	dir := t.TempDir()
	_, err := ReadPasswordHash(dir)
	assert.ErrorIs(t, err, os.ErrNotExist)
	phc, _ := HashPassword("pw")

	// Ten concurrent creations: exactly one wins, the rest get ErrPasswordExists.
	var wins, exists int32
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch err := CreatePasswordHash(dir, phc); err {
			case nil:
				atomic.AddInt32(&wins, 1)
			case ErrPasswordExists:
				atomic.AddInt32(&exists, 1)
			default:
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, int32(1), wins)
	assert.Equal(t, int32(9), exists)
	got, err := ReadPasswordHash(dir)
	require.NoError(t, err)
	assert.Equal(t, phc, got)
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(filepath.Join(dir, PasswordFile))
		assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		assert.NotContains(t, e.Name(), "password-new", "no temp left behind")
	}
	require.NoError(t, RemovePasswordHash(dir))
	require.NoError(t, RemovePasswordHash(dir), "removing none is fine")
}

// Security 1: the initial password comes from a private file, read once and deleted; never
// used when a password exists or the file is readable by others.
func TestInitialPasswordFile(t *testing.T) {
	ks := newTestKeyStore(t)
	f := filepath.Join(t.TempDir(), "pw")
	if runtime.GOOS != "windows" {
		require.NoError(t, os.WriteFile(f, []byte("from-a-file\n"), 0o644))
		ks.ApplyInitialPasswordFile(f)
		assert.False(t, ks.PasswordSet(), "a file others can read is refused")
		_, err := os.Stat(f)
		assert.NoError(t, err, "and left for its owner to fix")
	}
	_ = os.Remove(f) // WriteFile keeps an existing file's mode
	require.NoError(t, os.WriteFile(f, []byte("from-a-file\n"), 0o600))
	ks.ApplyInitialPasswordFile(f)
	require.True(t, ks.PasswordSet())
	phc, _ := ReadPasswordHash(ks.dataDir())
	assert.True(t, VerifyPassword(phc, "from-a-file"), "one trailing newline dropped")
	_, err := os.Stat(f)
	assert.True(t, os.IsNotExist(err), "deleted once read")

	require.NoError(t, os.WriteFile(f, []byte("another\n"), 0o600))
	ks.ApplyInitialPasswordFile(f)
	phc, _ = ReadPasswordHash(ks.dataDir())
	assert.True(t, VerifyPassword(phc, "from-a-file"), "ignored when a password is set")
}

// An offline `coral reset-password` leaves a marker; the next start ends every session.
func TestRevokeMarker(t *testing.T) {
	ks := newTestKeyStore(t)
	a, _ := ks.CreateSession("", "x")
	ks.ApplyRevokeMarker()
	assert.True(t, ks.ValidateSession(a), "no marker: nothing ends")
	require.NoError(t, os.WriteFile(filepath.Join(ks.dataDir(), SessionsRevokedFile), []byte("reset\n"), 0o600))
	ks.ApplyRevokeMarker()
	assert.False(t, ks.ValidateSession(a))
	_, err := os.Stat(filepath.Join(ks.dataDir(), SessionsRevokedFile))
	assert.True(t, os.IsNotExist(err))
}

// The throttle: five free, then 2^(n-5) s, capped at 15 minutes; per address; cleared by a
// right password; forgotten after 30 minutes.
func TestPasswordThrottleBackoff(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	th := newPasswordThrottle()
	th.now = func() time.Time { return now }
	for i := 0; i < freeFails; i++ {
		assert.Zero(t, th.delay("a"))
		th.fail("a")
	}
	th.fail("a") // 6th
	assert.Equal(t, 2*time.Second, th.delay("a"))
	for i := 0; i < 30; i++ {
		th.fail("a")
	}
	assert.Equal(t, maxDelay, th.delay("a"), "capped at 15 minutes")
	assert.Zero(t, th.delay("b"), "another address is unaffected")
	now = now.Add(maxDelay)
	assert.Zero(t, th.delay("a"), "the cap passes")
	th.success("a")
	assert.Zero(t, th.delay("a"))
	th.fail("c")
	now = now.Add(forgetAfter)
	assert.Zero(t, th.delay("c"))
}

// Security 4: one attempt in flight per address, so twenty at once from one address leave a
// slot for another; a full gate waits slotWait, then refuses.
func TestPasswordThrottleFairGate(t *testing.T) {
	th := newPasswordThrottle()
	release, ok := th.acquire("flood")
	require.True(t, ok)
	var got int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r, ok := th.acquire("flood"); ok {
				atomic.AddInt32(&got, 1)
				r()
			}
		}()
	}
	wg.Wait()
	assert.Zero(t, got, "the same address never gets a second slot")
	r2, ok := th.acquire("someone-else")
	require.True(t, ok, "another address still gets one")
	start := time.Now()
	_, ok = th.acquire("third")
	assert.False(t, ok, "both slots busy: refused after the wait")
	assert.GreaterOrEqual(t, time.Since(start), slotWait-100*time.Millisecond)
	release()
	r2()
	r3, ok := th.acquire("third")
	require.True(t, ok)
	r3()

	// Past globalLimit wrong passwords a minute, every attempt pauses.
	var paused int32
	th.sleep = func(time.Duration) { atomic.AddInt32(&paused, 1) }
	for i := 0; i <= globalLimit; i++ {
		th.fail("x")
	}
	r4, ok := th.acquire("y")
	require.True(t, ok)
	r4()
	assert.Equal(t, int32(1), paused)
}

func TestSessionLabelIsPlainText(t *testing.T) {
	assert.Equal(t, "Chrome on macOS", sessionLabel("Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0 Safari/537.36"))
	assert.Equal(t, "Firefox on Linux", sessionLabel("Mozilla/5.0 (X11; Linux x86_64; rv:131.0) Gecko/20100101 Firefox/131.0"))
	hostile := "<img src=x onerror=alert(1)>\x00\x1b[31m\u200b" + strings.Repeat("A", 200)
	l := sessionLabel(hostile)
	assert.LessOrEqual(t, len([]rune(l)), maxLabel)
	assert.NotContains(t, l, "\x00")
	assert.NotContains(t, l, "\x1b")
	assert.NotContains(t, l, "\u200b")
	assert.Equal(t, "Unknown browser", sessionLabel("\x00\x01"))
}

// #6100: the Coral app's web view adds the marker to its User-Agent (Edge on Windows, WebKit on macOS), and the Sessions page names those
// sessions "Coral app", so a person can tell them from their own browser. The label follows the marker, nothing else.
func TestSessionLabelFollowsTheCoralAppMarker(t *testing.T) {
	edge := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36 Edg/141.0.3537.71"
	assert.Equal(t, "Edge on Windows", sessionLabel(edge), "the person's own Edge")
	assert.Equal(t, "Coral app on Windows", sessionLabel(edge+" "+appmarker.Marker("1.4.157")), "the app's window on the same Edge")
	safari := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko)"
	assert.Equal(t, "Coral app on macOS", sessionLabel(safari+" "+appmarker.Marker("1.4.157")), "WebKit has no Safari/ token in a web view")
	assert.Equal(t, "Coral app", sessionLabel(appmarker.Marker("1.4.157")))
	assert.Equal(t, "Safari on macOS", sessionLabel(safari+" Version/17.4 Safari/605.1.15"), "no marker, no change")
	assert.Equal(t, "Chrome on Linux", sessionLabel("Mozilla/5.0 (X11; Linux x86_64) Chrome/141.0 CoralAppX/1"), "a token that only starts like the marker")
}

// Security, #3949 round 1: the code is printed only when stderr is a terminal; the tray and the
// app send stderr to tray.log.
func TestSetupCodeNeverPrintedToANonTerminal(t *testing.T) {
	out := &strings.Builder{}
	setupConsole = out
	defer func(f func() bool) { setupConsoleIsTerminal = f }(setupConsoleIsTerminal)
	setupConsoleIsTerminal = func() bool { return false }
	ks := newTestKeyStore(t)
	c := ks.SetupCode()
	require.Len(t, c, setupCodeLen)
	assert.Empty(t, out.String(), "nothing printed to a log file")
}

func TestSetupCode(t *testing.T) {
	setupConsole = &strings.Builder{}
	defer func(f func() bool) { setupConsoleIsTerminal = f }(setupConsoleIsTerminal)
	setupConsoleIsTerminal = func() bool { return true }
	ks := newTestKeyStore(t)
	c := ks.SetupCode()
	require.Len(t, c, setupCodeLen)
	for _, r := range c {
		assert.Contains(t, setupAlphabet, string(r))
	}
	assert.Contains(t, setupConsole.(*strings.Builder).String(), FormatSetupCode(c), "printed on the console")
	assert.Equal(t, c, ks.SetupCode(), "the same code until used, expired or replaced")

	typed := strings.ToLower(FormatSetupCode(c))
	typed = strings.NewReplacer("0", "o", "1", "l").Replace(typed)
	assert.True(t, ks.checkSetupCode(" "+typed+" "), "case, dashes, spaces and look-alikes are forgiven")
	assert.False(t, ks.checkSetupCode(""))

	// Twenty wrong codes replace it.
	for i := 0; i < setupWrongMax; i++ {
		ks.checkSetupCode("WRONGWRO")
	}
	assert.NotEqual(t, c, ks.SetupCode())
	assert.False(t, ks.checkSetupCode(c), "the old one is dead")

	// Expired after 60 minutes.
	c = ks.SetupCode()
	ks.mu.Lock()
	ks.setupExpires = time.Now().Add(-time.Second)
	ks.mu.Unlock()
	assert.False(t, ks.checkSetupCode(c))
	assert.NotEqual(t, c, ks.SetupCode(), "a new one is made")

	// A password set: no code at all.
	phc, _ := HashPassword("pw")
	require.NoError(t, CreatePasswordHash(ks.dataDir(), phc))
	ks.retireSetupCode()
	assert.Equal(t, "", ks.SetupCode())
}

// Two creations interleaved (#4240, a v1.4.72 gate flake of the test above): one creation runs
// from start to end while another is about to make its temp file. The other must still get
// ErrPasswordExists, never a file-system error from the first one's cleanup.
func TestCreatePasswordHashInterleaved(t *testing.T) {
	dir := t.TempDir()
	phc, _ := HashPassword("pw")
	var first error
	beforePasswordTemp = func() {
		beforePasswordTemp = func() {}
		first = CreatePasswordHash(dir, phc)
	}
	t.Cleanup(func() { beforePasswordTemp = func() {} })
	second := CreatePasswordHash(dir, phc)
	require.NoError(t, first, "the creation that ran in between wins")
	assert.ErrorIs(t, second, ErrPasswordExists, "the other is told a password exists")
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		assert.NotContains(t, e.Name(), "password-new", "no temp left behind")
	}
}
