package stage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/updater/verify"
)

// The Windows rollback package (#162 (b)): GitHub as two releases, the new one promoted as the latest and the installed one under its own tag.

func msiName(v string) string { return "Coral-" + v + "-x64.msi" }

type twoReleases struct {
	mu     sync.Mutex
	srv    *httptest.Server
	tags   map[string]map[string][]byte // tag -> file -> body
	hits   map[string]int               // "tag/file" -> count
	latest string
	order  []string         // the release tags in the order they were made (the list answers them newest first)
	seq    map[string]int64 // version -> the sequence of its promotion
}

// promo is the tag of the promotion release of version (#6393): promote-v<version>-<sequence>, which carries the manifest and its bundle.
func (tr *twoReleases) promo(version string) string {
	return fmt.Sprintf("promote-v%s-%d", version, tr.seq[version])
}

func (tr *twoReleases) release(version string, seq int64, pkg []byte, revoked []string) {
	sums := []byte(sum(pkg) + "  " + msiName(version) + "\n")
	m := verify.Manifest{Schema: 1, Repo: verify.Repo, Tag: "v" + version, Version: version, SourceSHA: testCommit, SHA256SumsSHA: sum(sums), Sequence: seq,
		PromotedAt: "2026-10-01T10:00:00Z", ExpiresAt: "2026-10-30T10:00:00Z", Revoked: revoked}
	manifest, _ := json.Marshal(m)
	tr.seq[version] = seq
	tr.tags[tr.promo(version)] = map[string][]byte{ManifestFile: manifest, ManifestBundleFile: bundleOf(manifest, "")}
	tr.tags["v"+version] = map[string][]byte{
		SumsFile: sums, SumsBundleFile: bundleOf(sums, testCommit), msiName(version): pkg,
	}
	tr.order = append(tr.order, "v"+version, tr.promo(version))
}

func (tr *twoReleases) serve(w http.ResponseWriter, req *http.Request) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	parts := strings.Split(strings.TrimPrefix(req.URL.Path, "/"), "/")
	if req.URL.Path == "/list" {
		var items []string
		for i := len(tr.order) - 1; i >= 0; i-- {
			tag := tr.order[i]
			if strings.HasPrefix(tag, "promote-") {
				items = append(items, `{"tag_name":"`+tag+`","draft":false,"prerelease":true,"assets":[{"name":"promote.json"},{"name":"promote.json.sigstore.json"}]}`)
			} else {
				items = append(items, `{"tag_name":"`+tag+`","draft":false,"prerelease":false,"assets":[{"name":"SHA256SUMS.txt"}]}`)
			}
		}
		w.Write([]byte("[" + strings.Join(items, ",") + "]"))
		return
	}
	if len(parts) == 3 && parts[0] == "latest" && parts[1] == "download" {
		http.Redirect(w, req, "/download/"+tr.latest+"/"+parts[2], http.StatusFound)
		return
	}
	if len(parts) != 3 || parts[0] != "download" {
		http.NotFound(w, req)
		return
	}
	tr.hits[parts[1]+"/"+parts[2]]++
	body, ok := tr.tags[parts[1]][parts[2]]
	if !ok {
		http.NotFound(w, req)
		return
	}
	w.Header().Set("Content-Length", fmt.Sprint(len(body)))
	w.Write(body)
}

func (tr *twoReleases) hit(tagFile string) int {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return tr.hits[tagFile]
}

// newTwo: the installed 1.4.125 (whose manifest has expired by now: a re-install doesn't need it fresh) and the latest 1.4.130.
func newTwo(t *testing.T) (*twoReleases, Config) {
	t.Helper()
	tr := &twoReleases{tags: map[string]map[string][]byte{}, hits: map[string]int{}, latest: "v1.4.130", seq: map[string]int64{}}
	tr.release("1.4.125", 6, []byte("the msi of 1.4.125"), []string{})
	tr.release("1.4.130", 7, []byte("the msi of 1.4.130"), []string{})
	tr.tags[tr.promo("1.4.130")][ManifestFile] = mustManifest(t, "1.4.130", tr.tags["v1.4.130"][SumsFile], 7, "2026-11-20T10:00:00Z", nil)
	tr.tags[tr.promo("1.4.130")][ManifestBundleFile] = bundleOf(tr.tags[tr.promo("1.4.130")][ManifestFile], "")
	tr.srv = httptest.NewServer(http.HandlerFunc(tr.serve))
	t.Cleanup(tr.srv.Close)
	cfg := Config{DataDir: t.TempDir(), Installed: "1.4.125", Pins: verify.DefaultPins(), Verifier: &fakeSigner{},
		Fetcher: &verify.Fetcher{Scheme: "http", Allow: func(string) bool { return true }}, ReleasesURL: tr.srv.URL + "/list",
		DownloadBase: tr.srv.URL + "/download/", Asset: msiName, Now: func() time.Time { return testNow }, Rollback: true}
	return tr, cfg
}

func mustManifest(t *testing.T, version string, sums []byte, seq int64, expires string, revoked []string) []byte {
	if revoked == nil {
		revoked = []string{}
	}
	m := verify.Manifest{Schema: 1, Repo: verify.Repo, Tag: "v" + version, Version: version, SourceSHA: testCommit, SHA256SumsSHA: sum(sums), Sequence: seq,
		PromotedAt: "2026-10-19T10:00:00Z", ExpiresAt: expires, Revoked: revoked}
	b, err := json.Marshal(m)
	require.NoError(t, err)
	return b
}

// A check that stages an update on a Windows MSI install also keeps the installed version's own package, verified (its manifest expired, as an
// installed version's usually has: a re-install doesn't need it fresh), and the apply can verify it again at use.
func TestACheckKeepsTheInstalledVersionsPackageForTheRollback(t *testing.T) {
	tr, cfg := newTwo(t)
	res := Check(context.Background(), cfg)
	require.Equal(t, ResultStaged, res.Result, res.FailedStage)
	s, err := LoadRollback(cfg.DataDir, "1.4.125")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(cfg.DataDir, UpdatesDir, RollbackDir, "1.4.125", msiName("1.4.125")), s.PackagePath)
	rec, err := ReverifyRollback(cfg.Verifier, cfg.Pins, s, msiName, "1.4.125", verify.State{}, testNow, 0)
	require.NoError(t, err)
	assert.Equal(t, "1.4.125", rec.Version)
	assert.Equal(t, sum([]byte("the msi of 1.4.125")), rec.PackageSHA256)
	assert.Equal(t, 1, tr.hit("v1.4.125/"+msiName("1.4.125")))

	// Once per version: the next check finds it still verifying and fetches nothing again.
	res = Check(context.Background(), cfg)
	assert.Equal(t, ResultAlreadyStaged, res.Result)
	assert.Equal(t, 1, tr.hit("v1.4.125/"+msiName("1.4.125")), "kept, not fetched again")
}

// Without the option (macOS, Linux, a portable Windows copy) nothing is fetched for the rollback.
func TestNoRollbackPackageWithoutTheOption(t *testing.T) {
	tr, cfg := newTwo(t)
	cfg.Rollback = false
	require.Equal(t, ResultStaged, Check(context.Background(), cfg).Result)
	assert.Zero(t, tr.hit("v1.4.125/"+msiName("1.4.125")))
	_, err := LoadRollback(cfg.DataDir, "1.4.125")
	assert.ErrorIs(t, err, ErrNoStaged)
}

// A rollback package that doesn't verify (a tampered download, a revoked version, a manifest for another version) is not kept; the update is
// staged all the same.
func TestARollbackPackageThatDoesntVerifyIsNotKeptAndTheUpdateIsStillStaged(t *testing.T) {
	tr, cfg := newTwo(t)
	tr.tags["v1.4.125"][msiName("1.4.125")] = []byte("a tampered download")
	require.Equal(t, ResultStaged, Check(context.Background(), cfg).Result)
	_, err := LoadRollback(cfg.DataDir, "1.4.125")
	assert.Error(t, err, "a package that doesn't hash to the verified sums entry is not kept")

	tr, cfg = newTwo(t)
	sums := tr.tags["v1.4.125"][SumsFile]
	tr.tags[tr.promo("1.4.125")][ManifestFile] = mustManifest(t, "1.4.125", sums, 6, "2026-10-30T10:00:00Z", []string{"1.4.125"})
	tr.tags[tr.promo("1.4.125")][ManifestBundleFile] = bundleOf(tr.tags[tr.promo("1.4.125")][ManifestFile], "")
	require.Equal(t, ResultStaged, Check(context.Background(), cfg).Result)
	_, err = LoadRollback(cfg.DataDir, "1.4.125")
	assert.Error(t, err, "a revoked version is never kept to put back")

	tr, cfg = newTwo(t)
	tr.tags["v1.4.125"] = tr.tags["v1.4.130"] // the installed version's tag serving another release
	require.Equal(t, ResultStaged, Check(context.Background(), cfg).Result)
	_, err = LoadRollback(cfg.DataDir, "1.4.125")
	assert.Error(t, err, "a manifest for another version is refused")
}

// #162 C: the kept copy is verified again at use. A package changed in the folder after it was kept is refused; so is the kept copy of another
// version (LoadRollback), and other versions are removed when the installed version's copy is kept.
func TestTheKeptRollbackPackageIsVerifiedAgainAtUse(t *testing.T) {
	_, cfg := newTwo(t)
	stale := filepath.Join(cfg.DataDir, UpdatesDir, RollbackDir, "1.4.120")
	require.NoError(t, os.MkdirAll(stale, 0o700))
	require.Equal(t, ResultStaged, Check(context.Background(), cfg).Result)
	_, err := os.Stat(stale)
	assert.True(t, os.IsNotExist(err), "another version's copy is removed")

	s, err := LoadRollback(cfg.DataDir, "1.4.125")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(s.PackagePath, []byte("changed in the folder"), 0o600))
	_, err = ReverifyRollback(cfg.Verifier, cfg.Pins, s, msiName, "1.4.125", verify.State{}, testNow, 0)
	assert.Error(t, err)

	_, err = LoadRollback(cfg.DataDir, "1.4.124")
	assert.ErrorIs(t, err, ErrNoStaged, "only the copy for the version asked for")
	_, err = ReverifyRollback(cfg.Verifier, cfg.Pins, s, msiName, "1.4.124", verify.State{}, testNow, 0)
	assert.Error(t, err, "verified for exactly the installed version")
}

// #162 (b), Security's ordering note: a NEW release whose manifest revokes the installed version makes the kept rollback copy refused at use.
// The check records the new manifest's revocations in the state when it verifies (Observe, #5301), and the apply's Rollback verifies again
// against the state on disk, so a revoked version is never put back even though its own manifest doesn't revoke it.
func TestANewReleaseThatRevokesTheInstalledVersionBlocksItsRollback(t *testing.T) {
	tr, cfg := newTwo(t)
	require.Equal(t, ResultStaged, Check(context.Background(), cfg).Result)
	s, err := LoadRollback(cfg.DataDir, "1.4.125")
	require.NoError(t, err, "kept while nothing revokes it")

	// 1.4.131 is promoted and revokes 1.4.125.
	tr.mu.Lock()
	tr.release("1.4.131", 8, []byte("the msi of 1.4.131"), []string{"1.4.125"})
	tr.tags[tr.promo("1.4.131")][ManifestFile] = mustManifest(t, "1.4.131", tr.tags["v1.4.131"][SumsFile], 8, "2026-11-20T10:00:00Z", []string{"1.4.125"})
	tr.tags[tr.promo("1.4.131")][ManifestBundleFile] = bundleOf(tr.tags[tr.promo("1.4.131")][ManifestFile], "")
	tr.latest = "v1.4.131"
	tr.mu.Unlock()
	require.Equal(t, ResultStaged, Check(context.Background(), cfg).Result)

	st, err := verify.LoadState(filepath.Join(cfg.DataDir, UpdatesDir))
	require.NoError(t, err)
	assert.Contains(t, st.Revoked, "1.4.125", "the check recorded the new manifest's revocation")
	_, err = ReverifyRollback(cfg.Verifier, cfg.Pins, s, msiName, "1.4.125", st, testNow, 0)
	assert.Error(t, err, "the apply's re-verification at use refuses a version a later release revoked")
	_, err = LoadRollback(cfg.DataDir, "1.4.125")
	assert.Error(t, err, "and the check no longer keeps it")
}
