package verify

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/tuf"
)

// The Sigstore trusted root the updater verifies with (#5303): fetched and refreshed through TUF from the public-good mirror, starting from the
// TUF root sigstore-go embeds, at most once a day. The mirror is a constant; nothing a request, a setting or the environment says changes it.
//
// When Sigstore rotates its signing CA or log key, an updater that only had a static snapshot would refuse every release signed after it and
// could not update itself. With the refresh it follows the rotation, and the metadata's own signatures and expiry decide what it trusts.

// TrustSource says where a trusted root came from, for the log and the JSON.
type TrustSource string

const (
	TrustTUF      TrustSource = "tuf"       // fetched and verified through TUF now
	TrustTUFCache TrustSource = "tuf_cache" // the cache of an earlier fetch, its TUF metadata still valid
	TrustEmbedded TrustSource = "embedded"  // the snapshot compiled in: only on a first run with no TUF cache and no network
)

// fetchTrusted is the TUF fetch (a test replaces it): the trusted root a client with these options gets.
var fetchTrusted = func(opts *tuf.Options) (root.TrustedMaterial, error) {
	tr, err := root.FetchTrustedRootWithOptions(opts)
	if err != nil {
		return nil, err
	}
	return tr, nil
}

const tufTimeout = 45 * time.Second

func tufOptions(ctx context.Context, cacheDir string) *tuf.Options {
	return tuf.DefaultOptions().WithRepositoryBaseURL(tuf.DefaultMirror).WithCachePath(cacheDir).WithCacheValidity(1).WithContext(ctx)
}

// LoadTrust returns the trusted root to verify with, caching TUF's files under cacheDir (the updater's own folder, 0700):
//
//  1. TUF, refreshed when the cache is over a day old;
//  2. when that fails (offline, the mirror down), the cache, for as long as its TUF metadata has not expired;
//  3. when no trusted root has ever been cached (a first run with no network), the snapshot compiled in;
//  4. otherwise (a cache that has expired and no network) an error: no update is better than trusting what TUF no longer vouches for.
func LoadTrust(ctx context.Context, cacheDir string) (root.TrustedMaterial, TrustSource, error) {
	if err := ensureDir(cacheDir); err != nil {
		return nil, "", err
	}
	ctx, cancel := context.WithTimeout(ctx, tufTimeout)
	defer cancel()
	live, liveErr := fetchTrusted(tufOptions(ctx, cacheDir))
	if liveErr == nil {
		return live, TrustTUF, nil
	}
	if cached, err := fetchTrusted(tufOptions(ctx, cacheDir).WithForceCache()); err == nil {
		return cached, TrustTUFCache, nil
	}
	if !hasCachedRoot(cacheDir) {
		tm, err := EmbeddedTrust()
		if err != nil {
			return nil, "", err
		}
		return tm, TrustEmbedded, nil
	}
	return nil, "", fmt.Errorf("the Sigstore trusted root could not be refreshed and the cached one has expired: %w", liveErr)
}

// hasCachedRoot reports whether an earlier fetch left the trusted root target in the cache. A fetch that failed on a first run still leaves TUF's
// own root.json there, which is not a cached trusted root.
func hasCachedRoot(dir string) bool {
	found := false
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() && strings.HasSuffix(d.Name(), "trusted_root.json") && filepath.Base(filepath.Dir(path)) == "targets" {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}
