package verify

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/tuf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fakeFetch(t *testing.T, live, cache error, trust root.TrustedMaterial) (calls *[]*tuf.Options) {
	t.Helper()
	old := fetchTrusted
	t.Cleanup(func() { fetchTrusted = old })
	calls = &[]*tuf.Options{}
	fetchTrusted = func(o *tuf.Options) (root.TrustedMaterial, error) {
		*calls = append(*calls, o)
		err := live
		if o.ForceCache {
			err = cache
		}
		if err != nil {
			return nil, err
		}
		return trust, nil
	}
	return calls
}

func TestLoadTrustPrefersTUFThenItsCacheThenTheSnapshotOnAFirstRun(t *testing.T) {
	emb, err := EmbeddedTrust()
	require.NoError(t, err)
	tuftrust, err := root.NewTrustedRootFromPath("testdata/trusted_root.json")
	require.NoError(t, err)
	down := errors.New("no network")
	expired := errors.New("metadata expired")

	// TUF answers: used, refreshed through the fixed mirror into the updater's own folder, at most daily.
	dir := filepath.Join(t.TempDir(), "tuf")
	calls := fakeFetch(t, nil, nil, tuftrust)
	tm, src, err := LoadTrust(context.Background(), dir)
	require.NoError(t, err)
	assert.Equal(t, TrustTUF, src)
	assert.Same(t, root.TrustedMaterial(tuftrust), tm)
	require.Len(t, *calls, 1)
	o := (*calls)[0]
	assert.Equal(t, tuf.DefaultMirror, o.RepositoryBaseURL, "the mirror is the compiled-in one")
	assert.Equal(t, dir, o.CachePath)
	assert.Equal(t, 1, o.CacheValidity, "refreshed at most once a day")
	assert.NotEmpty(t, o.Root, "TUF starts from the embedded root")
	fi, err := os.Stat(dir)
	require.NoError(t, err)
	if runtime.GOOS != "windows" { // Windows reports 0777 for every folder: its protection is the access list (secfile), not mode bits
		assert.Equal(t, os.FileMode(0o700), fi.Mode().Perm())
	}

	// Offline, with an earlier fetch's cache: the cache is used while its metadata is valid.
	calls = fakeFetch(t, down, nil, tuftrust)
	_, src, err = LoadTrust(context.Background(), dir)
	require.NoError(t, err)
	assert.Equal(t, TrustTUFCache, src)
	require.Len(t, *calls, 2)
	assert.True(t, (*calls)[1].ForceCache)

	// Offline on a first run: nothing was ever cached (TUF's own root.json is not a cached trusted root), so the snapshot starts it.
	first := filepath.Join(t.TempDir(), "tuf")
	// The folder is made private first, as LoadTrust makes it, so what is put in it after takes that
	// owner-only ACL. Filled first, a Windows folder made private later leaves its old children with an
	// empty DACL (secfile.Protect's protected, non-inheriting ACL), unreadable to the very user.
	require.NoError(t, ensureDir(first))
	require.NoError(t, os.MkdirAll(filepath.Join(first, "mirror", "targets"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(first, "mirror", "root.json"), []byte("{}"), 0o600))
	fakeFetch(t, down, down, tuftrust)
	tm, src, err = LoadTrust(context.Background(), first)
	require.NoError(t, err)
	assert.Equal(t, TrustEmbedded, src)
	assert.NotNil(t, tm)
	_ = emb

	// Offline with a cache whose metadata has expired: no update, never a quiet fall back to the snapshot (it may name a key Sigstore retired).
	stale := filepath.Join(t.TempDir(), "tuf")
	require.NoError(t, ensureDir(stale)) // as above
	require.NoError(t, os.MkdirAll(filepath.Join(stale, "mirror", "targets"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(stale, "mirror", "targets", "trusted_root.json"), []byte("{}"), 0o600))
	fakeFetch(t, down, expired, tuftrust)
	_, _, err = LoadTrust(context.Background(), stale)
	require.Error(t, err)
	assert.ErrorIs(t, err, down)
}

func TestLoadTrustRefusesAFolderThatIsALink(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("symlinks need a privilege on Windows")
	}
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "tuf")
	require.NoError(t, os.Symlink(target, link))
	fakeFetch(t, nil, nil, nil)
	_, _, err := LoadTrust(context.Background(), link)
	assert.Error(t, err)
}

// The scheduled check (.github/workflows/trusted-root-check.yml): the root compiled in is the one TUF serves today. Run only with
// CORAL_TUF_LIVE=1, because it asks the Sigstore mirror. A failure means Sigstore rotated something: refresh verify/trusted_root.json and
// testdata/trusted_root.json from the live root, run the tests, and release, so builds in the field keep a current starting point.
func TestTheEmbeddedRootIsTheOneTUFServesNow(t *testing.T) {
	if os.Getenv("CORAL_TUF_LIVE") != "1" {
		t.Skip("set CORAL_TUF_LIVE=1 to ask the Sigstore TUF mirror")
	}
	opts := tuf.DefaultOptions().WithCachePath(t.TempDir())
	client, err := tuf.New(opts)
	require.NoError(t, err)
	live, err := client.GetTarget("trusted_root.json")
	require.NoError(t, err)
	var a, b any
	require.NoError(t, json.Unmarshal(live, &a))
	require.NoError(t, json.Unmarshal(embeddedRoot, &b))
	assert.Equal(t, a, b, "the embedded trusted root differs from the live TUF one: refresh it (see this test's comment)")
}
