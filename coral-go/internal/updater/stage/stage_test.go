package stage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/updater/verify"
)

// fakeSigner stands in for Sigstore (verify's own tests prove the real one against the real v1.4.120 bundle): a "bundle" is the artifact's hash and
// the commit the release was built from; verifying fails when either differs, or when the signer is told to refuse.
type fakeSigner struct {
	mu      sync.Mutex
	verdict error
}

type fakeBundle struct {
	Artifact string `json:"artifact"`
	Commit   string `json:"commit"`
}

func sum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func bundleOf(artifact []byte, commit string) []byte {
	b, _ := json.Marshal(fakeBundle{Artifact: sum(artifact), Commit: commit})
	return b
}

func (f *fakeSigner) VerifyBundle(bundleJSON, artifact []byte, id verify.Identity) (verify.Cert, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.verdict != nil {
		return verify.Cert{}, f.verdict
	}
	var b fakeBundle
	if json.Unmarshal(bundleJSON, &b) != nil || b.Artifact != sum(artifact) {
		return verify.Cert{}, errors.New("the signature is not over this artifact")
	}
	return verify.Cert{SAN: id.SAN, SourceRepositoryDigest: b.Commit}, nil
}

var (
	testNow    = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	testCommit = strings.Repeat("cd", 20)
)

// fakeRelease is GitHub as far as the updater sees it: the files of one promoted release, served under the paths the updater asks for.
type fakeRelease struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	version  string
	pkg      []byte
	served   []byte // what is sent for the package when it differs from pkg (a corrupted download)
	files    map[string][]byte
	hits     map[string]int
	status   map[string]int // a path suffix -> a status to answer
	redirect map[string]string
	extra    int // bytes appended to the package body past its declared length (chunked)
}

func asset(version string) string { return "coral-linux-amd64-" + version + ".tar.gz" }

func newFakeRelease(t *testing.T, version string) *fakeRelease {
	t.Helper()
	r := &fakeRelease{t: t, hits: map[string]int{}, status: map[string]int{}, redirect: map[string]string{}}
	r.srv = httptest.NewServer(http.HandlerFunc(r.serve))
	t.Cleanup(r.srv.Close)
	r.publish(version, []byte("the package bytes of "+version))
	return r
}

// publish makes version the newest promoted release with package pkg.
func (r *fakeRelease) publish(version string, pkg []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.version, r.pkg, r.served = version, pkg, nil
	sums := []byte(sum(pkg) + "  " + asset(version) + "\n" + strings.Repeat("0", 64) + "  Coral-" + version + "-x64.msi\n")
	m := verify.Manifest{Schema: 1, Repo: verify.Repo, Tag: "v" + version, Version: version, SourceSHA: testCommit, SHA256SumsSHA: sum(sums), Sequence: 7,
		PromotedAt: "2026-10-20T10:00:00Z", ExpiresAt: "2026-11-20T10:00:00Z", Revoked: []string{}}
	manifest, _ := json.Marshal(m)
	r.files = map[string][]byte{
		"promote.json": manifest, "promote.json.sigstore.json": bundleOf(manifest, ""),
		"SHA256SUMS.txt": sums, "SHA256SUMS.txt.sigstore.json": bundleOf(sums, testCommit),
		asset(version): pkg,
	}
}

func (r *fakeRelease) hitCount(name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hits[name]
}

func (r *fakeRelease) serve(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := filepath.Base(req.URL.Path)
	r.hits[name]++
	for suffix, code := range r.status {
		if strings.HasSuffix(req.URL.Path, suffix) {
			http.Error(w, "no", code)
			return
		}
	}
	if to, ok := r.redirect[name]; ok {
		http.Redirect(w, req, to, http.StatusFound)
		return
	}
	switch {
	case strings.HasPrefix(req.URL.Path, "/download/"):
		tag := strings.Split(strings.TrimPrefix(req.URL.Path, "/download/"), "/")[0]
		body, ok := servedFrom(map[string]map[string][]byte{"v" + r.version: r.files}, "v"+r.version, tag, name)
		if name == asset(r.version) && r.served != nil {
			body = r.served
		}
		if !ok {
			http.NotFound(w, req)
			return
		}
		if name == asset(r.version) && r.extra > 0 {
			w.Write(body)
			w.Write(make([]byte, r.extra))
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		w.Write(body)
	default:
		http.NotFound(w, req)
	}
}

type env struct {
	t      *testing.T
	rel    *fakeRelease
	data   string
	signer *fakeSigner
	now    time.Time
	cfg    Config
}

func newEnv(t *testing.T, installed string) *env {
	t.Helper()
	e := &env{t: t, rel: newFakeRelease(t, "1.4.130"), data: t.TempDir(), signer: &fakeSigner{}, now: testNow}
	e.cfg = Config{
		DataDir: e.data, Installed: installed, Pins: verify.DefaultPins(), Verifier: e.signer, Asset: asset,
		Fetcher:      &verify.Fetcher{Scheme: "http", Allow: func(h string) bool { return h == "127.0.0.1" }},
		DownloadBase: e.rel.srv.URL + "/download/",
		Now:          func() time.Time { return e.now },
	}
	return e
}

func (e *env) check() Result { e.t.Helper(); return Check(context.Background(), e.cfg) }

func (e *env) state() verify.State {
	e.t.Helper()
	st, err := verify.LoadState(filepath.Join(e.data, UpdatesDir))
	require.NoError(e.t, err)
	return st
}

func (e *env) stagedVersions() []string {
	entries, _ := os.ReadDir(filepath.Join(e.data, UpdatesDir, StagingDir))
	var out []string
	for _, en := range entries {
		out = append(out, en.Name())
	}
	return out
}

func TestACheckStagesTheVerifiedPackageWithItsFiles(t *testing.T) {
	e := newEnv(t, "1.4.125")
	res := e.check()
	require.Equal(t, ResultStaged, res.Result, "%+v", res)
	assert.Equal(t, "1.4.130", res.Version)
	assert.Equal(t, []string{"1.4.130"}, e.stagedVersions())

	s, err := LoadStaged(e.data)
	require.NoError(t, err)
	assert.Equal(t, "1.4.130", s.Version)
	pkg, err := os.ReadFile(s.PackagePath)
	require.NoError(t, err)
	assert.Equal(t, e.rel.pkg, pkg)
	assert.Equal(t, asset("1.4.130"), filepath.Base(s.PackagePath), "named by the verified sums entry")
	assert.Equal(t, sum(e.rel.pkg), s.Record.PackageSHA256)
	for _, f := range []string{ManifestFile, ManifestBundleFile, SumsFile, SumsBundleFile, RecordFile, asset("1.4.130")} {
		_, err := os.Stat(filepath.Join(s.Dir, f))
		assert.NoError(t, err, f)
	}
	st := e.state()
	require.NotNil(t, st.Stage)
	assert.Equal(t, "1.4.130", st.Stage.Staged)
	assert.Empty(t, st.Version, "staging never moves the high-water mark: that is the apply's, after the health check")
	assert.Equal(t, e.now.Format(time.RFC3339), st.LastCheck)

	// The apply's half: verify again from the staged files.
	rec, err := Reverify(e.signer, e.cfg.Pins, s, asset, verify.State{}, "1.4.125", e.now, DefaultMaxPackage)
	require.NoError(t, err)
	assert.Equal(t, s.Record.PackageSHA256, rec.PackageSHA256)
}

func TestStagedFilesAreOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("modes are not meaningful on Windows (an owner-only ACL is set instead)")
	}
	e := newEnv(t, "1.4.125")
	require.Equal(t, ResultStaged, e.check().Result)
	root := filepath.Join(e.data, UpdatesDir)
	for _, d := range []string{root, filepath.Join(root, StagingDir), filepath.Join(root, StagingDir, "1.4.130")} {
		fi, err := os.Stat(d)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o700), fi.Mode().Perm(), d)
	}
	entries, _ := os.ReadDir(filepath.Join(root, StagingDir, "1.4.130"))
	require.Len(t, entries, 6)
	for _, en := range entries {
		fi, _ := en.Info()
		assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm(), en.Name())
	}
	fi, err := os.Stat(filepath.Join(root, verify.StateFile))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	_, err = os.Stat(filepath.Join(root, CheckLockFile))
	assert.True(t, errors.Is(err, os.ErrNotExist), "the lock is released")
}

func TestASecondCheckDownloadsNothingAgain(t *testing.T) {
	e := newEnv(t, "1.4.125")
	require.Equal(t, ResultStaged, e.check().Result)
	require.Equal(t, 1, e.rel.hitCount(asset("1.4.130")))
	res := e.check()
	assert.Equal(t, ResultAlreadyStaged, res.Result)
	assert.Equal(t, 1, e.rel.hitCount(asset("1.4.130")), "the package was not fetched again")

	// A staged package that was damaged since is fetched again.
	s, _ := LoadStaged(e.data)
	require.NoError(t, os.WriteFile(s.PackagePath, []byte("damaged"), 0o600))
	assert.Equal(t, ResultStaged, e.check().Result)
	assert.Equal(t, 2, e.rel.hitCount(asset("1.4.130")))
	b, _ := os.ReadFile(s.PackagePath)
	assert.Equal(t, e.rel.pkg, b)
}

func TestNothingNewerOrNothingPromoted(t *testing.T) {
	e := newEnv(t, "1.4.130")
	res := e.check()
	assert.Equal(t, ResultUpToDate, res.Result, "the newest is the installed one")
	assert.Zero(t, e.rel.hitCount("SHA256SUMS.txt"))
	e = newEnv(t, "1.4.131")
	assert.Equal(t, ResultUpToDate, e.check().Result, "the installed one is newer")

	e = newEnv(t, "1.4.125")
	e.rel.status["promote.json"] = 404 // released, not promoted
	res = e.check()
	assert.Equal(t, ResultNotPromoted, res.Result)
	assert.Empty(t, e.stagedVersions())
	assert.Empty(t, res.NextAttempt, "not promoted yet is neutral: no back-off, nothing in the state")

	e = newEnv(t, "1.4.125")
	e.rel.status["SHA256SUMS.txt.sigstore.json"] = 404
	assert.Equal(t, ResultNotPromoted, e.check().Result)
	assert.Zero(t, e.rel.hitCount(asset("1.4.130")), "the package is fetched only after the release verified")
}

func TestAPackageOneByteOffIsRefusedAndNeverStaged(t *testing.T) {
	e := newEnv(t, "1.4.125")
	bad := append([]byte{}, e.rel.pkg...)
	bad[0] ^= 1
	e.rel.served = bad
	res := e.check()
	require.Equal(t, ResultFailed, res.Result)
	assert.Equal(t, verify.StagePackage, res.FailedStage)
	assert.Empty(t, e.stagedVersions(), "no folder, no partial")
	_, err := LoadStaged(e.data)
	assert.ErrorIs(t, err, ErrNoStaged)
	st := e.state()
	require.NotNil(t, st.Stage)
	assert.Equal(t, KindVerify, st.Stage.ErrorKind)
	assert.Equal(t, 1, st.Stage.Attempts)
	assert.Equal(t, e.now.Add(time.Hour).Format(time.RFC3339), st.Stage.NextAttempt)
}

func TestAVerificationFailureStopsBeforeTheDownload(t *testing.T) {
	e := newEnv(t, "1.4.125")
	e.signer.verdict = errors.New("identity is not the pinned one")
	res := e.check()
	require.Equal(t, ResultFailed, res.Result)
	assert.Equal(t, verify.StageManifestSignature, res.FailedStage)
	assert.Zero(t, e.rel.hitCount(asset("1.4.130")))
	assert.Empty(t, e.stagedVersions())
	st := e.state()
	require.NotNil(t, st.LastFailure)
	assert.Equal(t, verify.StageManifestSignature, st.LastFailure.Stage)
	assert.Empty(t, st.LastFailure.Version, "an unverified manifest's version is not recorded as if it were known (#326)")
}

func TestFailuresBackOffAndASuccessResets(t *testing.T) {
	e := newEnv(t, "1.4.125")
	e.rel.status["promote.json"] = 500
	for i, want := range []time.Duration{5 * time.Minute, 15 * time.Minute, time.Hour, time.Hour} {
		res := e.check()
		require.Equal(t, ResultFailed, res.Result, i)
		assert.Equal(t, e.now.Add(want).Format(time.RFC3339), res.NextAttempt, "failure %d", i+1)
		// Inside the wait nothing is asked of GitHub.
		hits := e.rel.hitCount("promote.json")
		assert.Equal(t, ResultBackoff, e.check().Result)
		assert.Equal(t, hits, e.rel.hitCount("promote.json"), "no request during the back-off")
		e.now = e.now.Add(want + time.Second)
	}
	delete(e.rel.status, "promote.json")
	require.Equal(t, ResultStaged, e.check().Result)
	st := e.state()
	require.NotNil(t, st.Stage)
	assert.Zero(t, st.Stage.Attempts, "a success clears the failures")
	assert.Empty(t, st.Stage.NextAttempt)
}

func TestAVerificationBackOffDoublesToADay(t *testing.T) {
	e := newEnv(t, "1.4.125")
	e.signer.verdict = errors.New("refused")
	var got []time.Duration
	for i := 0; i < 7; i++ {
		res := e.check()
		require.Equal(t, ResultFailed, res.Result)
		next, _ := time.Parse(time.RFC3339, res.NextAttempt)
		got = append(got, next.Sub(e.now))
		e.now = next.Add(time.Second)
	}
	h := time.Hour
	assert.Equal(t, []time.Duration{h, 2 * h, 4 * h, 8 * h, 16 * h, 24 * h, 24 * h}, got)
}

func TestANewerVersionStartsTheBackOffAgain(t *testing.T) {
	e := newEnv(t, "1.4.125")
	e.signer.verdict = errors.New("refused")
	require.Equal(t, ResultFailed, e.check().Result)
	e.signer.verdict = nil
	// The back-off is still running for 1.4.130; 1.4.131 is promoted: the check waits (one download an hour at most)...
	assert.Equal(t, ResultBackoff, e.check().Result)
	// ...and once the wait is over, the attempt counter for the new version is its own.
	e.rel.publish("1.4.131", []byte("newer"))
	e.now = e.now.Add(time.Hour + time.Second)
	require.Equal(t, ResultStaged, e.check().Result)
	assert.Equal(t, []string{"1.4.131"}, e.stagedVersions())
}

func TestAFailedNewerStageKeepsTheOlderStagedVersion(t *testing.T) {
	e := newEnv(t, "1.4.125")
	require.Equal(t, ResultStaged, e.check().Result)
	e.rel.publish("1.4.131", []byte("newer package"))
	e.rel.served = []byte("not the package")
	res := e.check()
	require.Equal(t, ResultFailed, res.Result)
	assert.Equal(t, []string{"1.4.130"}, e.stagedVersions(), "the older verified package is untouched")
	s, err := LoadStaged(e.data)
	require.NoError(t, err)
	assert.Equal(t, "1.4.130", s.Version)
	assert.Equal(t, "1.4.130", e.state().Stage.Staged, "and the state still says so")

	// Once the newer one stages, the older one goes.
	e.rel.served = nil
	e.now = e.now.Add(2 * time.Hour)
	require.Equal(t, ResultStaged, e.check().Result)
	assert.Equal(t, []string{"1.4.131"}, e.stagedVersions())
}

func TestThePackageHasASizeCapAndAHardOne(t *testing.T) {
	e := newEnv(t, "1.4.125")
	e.cfg.MaxPackage = 10
	res := e.check()
	require.Equal(t, ResultFailed, res.Result)
	assert.Empty(t, e.stagedVersions())

	// A server that sends more than it declared (or no length at all) is cut at the cap, not followed.
	e = newEnv(t, "1.4.125")
	e.cfg.MaxPackage = int64(len(e.rel.pkg))
	e.rel.extra = 4096
	res = e.check()
	require.Equal(t, ResultFailed, res.Result)
	assert.Empty(t, e.stagedVersions())
}

func TestRedirectsAndSchemesOutsideTheAllowListAreRefused(t *testing.T) {
	e := newEnv(t, "1.4.125")
	// The manifest's redirect leaves the allowed host.
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("{}")) }))
	defer other.Close()
	e.rel.redirect["promote.json"] = strings.Replace(other.URL, "127.0.0.1", "localhost", 1) + "/promote.json"
	res := e.check()
	require.Equal(t, ResultFailed, res.Result)
	assert.Empty(t, e.stagedVersions())

	// https only: a plain-http base is refused by the default fetcher.
	e = newEnv(t, "1.4.125")
	e.cfg.Fetcher = &verify.Fetcher{}
	assert.Equal(t, ResultFailed, e.check().Result)
	assert.Zero(t, e.rel.hitCount("promote.json"), "no request was made")

	// A redirect for the package to a host that is not allowed.
	e = newEnv(t, "1.4.125")
	e.rel.redirect[asset("1.4.130")] = "http://localhost:1/" + asset("1.4.130")
	assert.Equal(t, ResultFailed, e.check().Result)
	assert.Empty(t, e.stagedVersions())
}

func TestOneCheckAtATimeAndNoneDuringAnApply(t *testing.T) {
	e := newEnv(t, "1.4.125")
	dir := filepath.Join(e.data, UpdatesDir)
	require.NoError(t, verify.EnsureDir(dir))
	live, _ := json.Marshal(lockBody{PID: os.Getpid(), Taken: e.now.Unix()})
	require.NoError(t, os.WriteFile(filepath.Join(dir, CheckLockFile), live, 0o600))
	assert.Equal(t, ResultBusy, e.check().Result)
	assert.Zero(t, e.rel.hitCount("promote.json"))
	require.NoError(t, os.Remove(filepath.Join(dir, CheckLockFile)))

	require.NoError(t, os.WriteFile(filepath.Join(dir, ApplyLockFile), live, 0o600))
	assert.Equal(t, ResultApplying, e.check().Result)
	assert.Zero(t, e.rel.hitCount("promote.json"))
	require.NoError(t, os.Remove(filepath.Join(dir, ApplyLockFile)))

	// A lock whose owner is gone, or that is too old, is stale and taken over.
	for name, body := range map[string]lockBody{
		"dead owner": {PID: 2147483646, Taken: e.now.Unix()},
		"too old":    {PID: os.Getpid(), Taken: e.now.Add(-3 * time.Hour).Unix()},
	} {
		b, _ := json.Marshal(body)
		require.NoError(t, os.WriteFile(filepath.Join(dir, CheckLockFile), b, 0o600))
		res := e.check()
		assert.Equal(t, ResultStaged, res.Result, name)
		require.NoError(t, os.RemoveAll(filepath.Join(dir, StagingDir)))
		require.NoError(t, os.Remove(filepath.Join(dir, verify.StateFile)))
	}
	// A garbage lock is stale too; a lock that is not a plain file is never removed.
	junk := filepath.Join(dir, CheckLockFile)
	require.NoError(t, os.WriteFile(junk, []byte("junk"), 0o600))
	assert.Equal(t, ResultBusy, e.check().Result, "a lock written this second may still be being filled in")
	old := time.Now().Add(-time.Minute)
	require.NoError(t, os.Chtimes(junk, old, old))
	assert.Equal(t, ResultStaged, e.check().Result)
	require.NoError(t, os.RemoveAll(filepath.Join(dir, StagingDir)))
	require.NoError(t, os.Remove(filepath.Join(dir, verify.StateFile)))
	require.NoError(t, os.Mkdir(filepath.Join(dir, CheckLockFile), 0o700))
	assert.Equal(t, ResultBusy, e.check().Result)
}

func TestTwoChecksAtOnceStageOnce(t *testing.T) {
	e := newEnv(t, "1.4.125")
	var wg sync.WaitGroup
	results := make([]Result, 6)
	for i := range results {
		wg.Add(1)
		go func(i int) { defer wg.Done(); results[i] = e.check() }(i)
	}
	wg.Wait()
	staged := 0
	for _, r := range results {
		if r.Result == ResultStaged {
			staged++
		}
		assert.Contains(t, []string{ResultStaged, ResultBusy, ResultAlreadyStaged}, r.Result, "%+v", r)
	}
	assert.GreaterOrEqual(t, staged, 1)
	assert.Equal(t, staged, e.rel.hitCount(asset("1.4.130")), "one download for each check that staged, never two at once")
	assert.Equal(t, []string{"1.4.130"}, e.stagedVersions())
}

func TestACorruptStateIsAFirstRunNotPermission(t *testing.T) {
	e := newEnv(t, "1.4.125")
	dir := filepath.Join(e.data, UpdatesDir)
	require.NoError(t, verify.EnsureDir(dir))
	require.NoError(t, os.WriteFile(filepath.Join(dir, verify.StateFile), []byte("{not json"), 0o600))
	require.Equal(t, ResultStaged, e.check().Result)
	// The installed version still bounds it from below: an equal or older release is never staged, state or no state.
	e = newEnv(t, "1.4.130")
	require.NoError(t, verify.EnsureDir(filepath.Join(e.data, UpdatesDir)))
	require.NoError(t, os.WriteFile(filepath.Join(e.data, UpdatesDir, verify.StateFile), []byte("{not json"), 0o600))
	assert.Equal(t, ResultUpToDate, e.check().Result)
	assert.Empty(t, e.stagedVersions())
}

func TestALinkInTheStagingPathIsRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating a symlink needs a privilege on Windows")
	}
	e := newEnv(t, "1.4.125")
	dir := filepath.Join(e.data, UpdatesDir)
	require.NoError(t, verify.EnsureDir(dir))
	elsewhere := t.TempDir()
	require.NoError(t, os.Symlink(elsewhere, filepath.Join(dir, StagingDir)))
	res := e.check()
	assert.Equal(t, ResultFailed, res.Result)
	entries, _ := os.ReadDir(elsewhere)
	assert.Empty(t, entries, "nothing was written through the link")

	// A staged folder whose files are links is not a staged update.
	e = newEnv(t, "1.4.125")
	require.Equal(t, ResultStaged, e.check().Result)
	s, _ := LoadStaged(e.data)
	secret := filepath.Join(t.TempDir(), "other")
	require.NoError(t, os.WriteFile(secret, []byte("x"), 0o600))
	for _, name := range []string{RecordFile, SumsFile, ManifestBundleFile, filepath.Base(s.PackagePath)} {
		p := filepath.Join(s.Dir, name)
		orig, _ := os.ReadFile(p)
		require.NoError(t, os.Remove(p))
		require.NoError(t, os.Symlink(secret, p))
		_, err := LoadStaged(e.data)
		assert.ErrorIs(t, err, ErrNoStaged, name)
		require.NoError(t, os.Remove(p))
		require.NoError(t, os.WriteFile(p, orig, 0o600))
	}
	_, err := LoadStaged(e.data)
	assert.NoError(t, err)
}

func TestApplyVerifiesAgainFromTheStagedFiles(t *testing.T) {
	e := newEnv(t, "1.4.125")
	require.Equal(t, ResultStaged, e.check().Result)
	load := func() *Staged { s, err := LoadStaged(e.data); require.NoError(t, err); return s }
	reverify := func(s *Staged) error {
		_, err := Reverify(e.signer, e.cfg.Pins, s, asset, verify.State{}, "1.4.125", e.now, DefaultMaxPackage)
		return err
	}
	require.NoError(t, reverify(load()))
	stageOf := func(err error) string {
		var f *verify.Failure
		require.True(t, errors.As(err, &f), "%v", err)
		return f.Stage
	}

	// The package is swapped after staging.
	s := load()
	orig, _ := os.ReadFile(s.PackagePath)
	require.NoError(t, os.WriteFile(s.PackagePath, append(orig, 'x'), 0o600))
	assert.Equal(t, verify.StagePackage, stageOf(reverify(s)))
	require.NoError(t, os.WriteFile(s.PackagePath, orig, 0o600))

	// The record is edited to match a swapped package: it proves nothing, the sums file decides.
	require.NoError(t, os.WriteFile(s.PackagePath, []byte("evil"), 0o600))
	s.Record.PackageSHA256 = sum([]byte("evil"))
	assert.Equal(t, verify.StagePackage, stageOf(reverify(s)))
	require.NoError(t, os.WriteFile(s.PackagePath, orig, 0o600))

	// The sums file is swapped: it no longer hashes to the manifest's digest.
	s = load()
	sums := append([]byte{}, s.Candidate.Sums...)
	s.Candidate.Sums = append(sums, '\n')
	assert.Equal(t, verify.StageSumsDigest, stageOf(reverify(s)))

	// A bundle that is not the signature's.
	s = load()
	s.Candidate.SumsBundle = bundleOf([]byte("other"), testCommit)
	assert.Equal(t, verify.StageSumsSignature, stageOf(reverify(s)))

	// A downgrade: the installed version is now newer than the staged one.
	s = load()
	_, err := Reverify(e.signer, e.cfg.Pins, s, asset, verify.State{}, "1.4.131", e.now, DefaultMaxPackage)
	assert.Equal(t, verify.StageManifestRules, stageOf(err))

	// An expired manifest.
	_, err = Reverify(e.signer, e.cfg.Pins, s, asset, verify.State{}, "1.4.125", e.now.Add(60*24*time.Hour), DefaultMaxPackage)
	assert.Equal(t, verify.StageManifestRules, stageOf(err))

	// A revoked version.
	_, err = Reverify(e.signer, e.cfg.Pins, s, asset, verify.State{Revoked: []string{"1.4.130"}}, "1.4.125", e.now, DefaultMaxPackage)
	assert.Equal(t, verify.StageManifestRules, stageOf(err))

	// A folder named for another version than its record.
	require.NoError(t, os.Rename(s.Dir, filepath.Join(filepath.Dir(s.Dir), "1.4.129")))
	_, err = LoadStaged(e.data)
	assert.ErrorIs(t, err, ErrNoStaged)
}

func TestLoadStagedNeedsExactlyOneFinishedVersion(t *testing.T) {
	e := newEnv(t, "1.4.125")
	_, err := LoadStaged(e.data)
	assert.ErrorIs(t, err, ErrNoStaged, "nothing yet")
	require.Equal(t, ResultStaged, e.check().Result)
	root := filepath.Join(e.data, UpdatesDir, StagingDir)
	require.NoError(t, os.Mkdir(filepath.Join(root, ".1.4.131.partial"), 0o700))
	_, err = LoadStaged(e.data)
	assert.NoError(t, err, "a partial download is not a staged version")
	require.NoError(t, os.Mkdir(filepath.Join(root, "1.4.131"), 0o700))
	_, err = LoadStaged(e.data)
	assert.ErrorIs(t, err, ErrNoStaged, "two versions are none: apply must not pick one")
}

func TestTheDownloadIsPaced(t *testing.T) {
	e := newEnv(t, "1.4.125")
	e.rel.publish("1.4.130", make([]byte, 96<<10))
	e.cfg.BytesPerSecond = 256 << 10
	start := time.Now()
	require.Equal(t, ResultStaged, e.check().Result)
	assert.GreaterOrEqual(t, time.Since(start), 300*time.Millisecond, "96 KiB at 256 KiB/s")
}

func TestAResultHoldsNoPathURLOrHash(t *testing.T) {
	e := newEnv(t, "1.4.125")
	for _, res := range []Result{e.check(), e.check()} {
		b, _ := json.Marshal(res)
		for _, bad := range []string{e.data, "http", "/", sum(e.rel.pkg)[:16]} {
			assert.NotContains(t, string(b), bad)
		}
	}
}
