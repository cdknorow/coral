package stage

// Finding the newest promotion (#326). The product releases are immutable, so the promote workflow publishes each promotion as a release of
// its own, tagged promote-<sequence>, with promote.json and its bundle. The sequence goes up by one per promotion (the workflow takes the
// highest published one plus one), so the newest is found by name alone, with no API host and no list: from the highest sequence this
// computer has seen, probe promote-<n> upward (1, 2, 4, ... further, then halve the gap) to the highest tag that exists.
//
// What exists is not trusted. Anyone who can create a release could publish a promote-<n> of their own, and a published release can't be
// removed. The workflow numbers the next promotion above every promote-<n> name that exists (tools/devops/promote.sh), so junk leaves names
// taken, not holes. From the highest name down (none below the accepted sequence, which the rules would refuse anyway) each is checked in full:
// signed by the promote workflow, a well-formed manifest, and its sequence the name's own number. The first that passes is the newest promotion.
// A name that doesn't exist costs a request and is not counted; up to maxInvalid that exist but fail are passed over. Junk delays an update at
// worst; it never installs anything. Probing reaches at most maxAhead past the highest sequence seen, so a junk name far above isn't looked at.
//
// GitHub's releases feed (releases.atom) lists only the ten newest releases, and the product releases outnumber promotions, so a promotion
// falls off it within a day; that is why the names are probed instead.

import (
	"context"
	"errors"
	"strconv"

	"github.com/subgentic/coral/internal/updater/verify"
)

const (
	// PromotionPrefix tags a promotion's release: promote-<sequence>. RollbackPrefix tags the first promotion of a version: promote-v<version>.
	PromotionPrefix = "promote-"
	RollbackPrefix  = "promote-v"

	// maxProbes bounds the requests that find the highest promote-<n> (doubling, then halving: about two per bit of the sequence), and maxAhead
	// how far past the highest sequence seen they reach.
	maxProbes = 64
	maxAhead  = 1 << 16
	// maxInvalid is how many promote-<n> that exist but aren't real promotions the walk down passes over (junk: #326, Security); maxMissing
	// bounds the names that don't exist on the way (each a request).
	maxInvalid = 64
	maxMissing = 256
)

var (
	errNotPromoted = errors.New("no promotion")
	errNetwork     = errors.New("the promotions could not be read")
)

// PromotionBase is the folder of promote-<seq>'s files under base (a DownloadBase).
func PromotionBase(base string, seq int64) string {
	return base + PromotionPrefix + strconv.FormatInt(seq, 10) + "/"
}

// RollbackBase is the folder of promote-v<version>'s files under base.
func RollbackBase(base, version string) string { return base + RollbackPrefix + version + "/" }

// newestPromotion returns the newest promotion that checks out: its manifest, its bundle and the parsed manifest. errNotPromoted when there is
// none, errNetwork when GitHub could not be asked, or the first candidate's refusal (a *verify.Failure) when every candidate failed its checks.
func (c *checker) newestPromotion(ctx context.Context) ([]byte, []byte, *verify.Manifest, error) {
	start := c.st.Sequence
	if c.st.Seen > start {
		start = c.st.Seen
	}
	if start < 0 {
		start = 0
	}
	manifests := map[int64][]byte{}
	probes := 0
	// exists asks for promote-<n>'s manifest: true when it is there (the bytes kept), false on 404.
	exists := func(n int64) (bool, error) {
		probes++
		b, err := c.get(ctx, PromotionBase(c.cfg.DownloadBase, n)+ManifestFile, verify.MaxManifestBytes)
		if notFound(err) {
			return false, nil
		}
		if err != nil {
			return false, errNetwork
		}
		manifests[n] = b
		return true, nil
	}

	// The highest promote-<n> from start: lo exists (or is start), hi doesn't.
	lo, hi := start, int64(0)
	for step := int64(1); probes < maxProbes; step *= 2 {
		if lo+step > start+maxAhead {
			hi = start + maxAhead + 1 // nothing further is looked at
			break
		}
		ok, err := exists(lo + step)
		if err != nil {
			return nil, nil, nil, err
		}
		if !ok {
			hi = lo + step
			break
		}
		lo += step
	}
	if hi == 0 {
		return nil, nil, nil, errNetwork // no end within the probes: not a sequence the workflow made
	}
	for hi-lo > 1 && probes < maxProbes {
		mid := lo + (hi-lo)/2
		ok, err := exists(mid)
		if err != nil {
			return nil, nil, nil, err
		}
		if ok {
			lo = mid
		} else {
			hi = mid
		}
	}
	if lo == 0 {
		return nil, nil, nil, errNotPromoted
	}

	// The candidates, from the highest down: the first that checks out in full.
	var first error
	floor := c.st.Sequence
	if floor < 1 {
		floor = 1
	}
	invalid, missing := 0, 0
	for n := lo; n >= floor && invalid < maxInvalid && missing < maxMissing; n-- {
		base := PromotionBase(c.cfg.DownloadBase, n)
		manifest, ok := manifests[n]
		if !ok {
			b, err := c.get(ctx, base+ManifestFile, verify.MaxManifestBytes)
			if notFound(err) {
				missing++ // not there: not a candidate
				continue
			}
			if err != nil {
				return nil, nil, nil, errNetwork
			}
			manifest = b
		}
		bundle, err := c.get(ctx, base+ManifestBundleFile, verify.MaxBundleBytes)
		if err != nil && !notFound(err) {
			return nil, nil, nil, errNetwork
		}
		m, verr := verify.PromotedManifest(c.cfg.Verifier, c.cfg.Pins, manifest, bundle)
		if verr == nil && m.Sequence != n {
			verr = &verify.Failure{Stage: verify.StageManifestDocument, Reason: "promote-" + strconv.FormatInt(n, 10) + " carries the manifest of sequence " + strconv.FormatInt(m.Sequence, 10)}
		}
		if verr == nil {
			return manifest, bundle, m, nil
		}
		invalid++
		if first == nil {
			first = verr
		}
	}
	if first == nil {
		return nil, nil, nil, errNotPromoted
	}
	return nil, nil, nil, first
}
