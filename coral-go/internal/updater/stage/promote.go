package stage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"

	"github.com/subgentic/coral/internal/updater/verify"
)

// A promotion is its own release (#6393). Immutable releases refuse a new asset on a published release, so the promote workflow publishes promote.json and its bundle as a release of
// their own, named promote-<product tag>-<sequence> (a prerelease, never Latest), and a second promotion (a revocation) is a new such release, never a replacement. The client finds the
// manifest of the newest promotion by listing the newest releases, never by trusting a name or an order: a candidate counts only if the promote workflow signed it.
const (
	// ReleasesList is the newest releases of the repository, newest first (one page).
	ReleasesList = "https://api.github.com/repos/" + verify.Repo + "/releases?per_page=30"

	// maxListBytes caps the list's size: 30 releases are well under it, and a larger answer is refused, not truncated.
	maxListBytes = 1 << 20
	// maxPromoteCandidates is how many promotion releases one pass looks at, newest first: junk by anyone with write access costs this many small downloads, no more.
	maxPromoteCandidates = 10
)

// promoteTagRe is a promotion release's tag: promote-<product tag>-<sequence>. The name only narrows what is looked at; the signature and the manifest's own tag and sequence decide.
var promoteTagRe = regexp.MustCompile(`^promote-(v[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,4})-([0-9]{1,12})$`)

type listedRelease struct {
	TagName string `json:"tag_name"`
	Draft   bool   `json:"draft"`
	Assets  []struct {
		Name string `json:"name"`
	} `json:"assets"`
}

func (r listedRelease) hasPromotionAssets() bool {
	var m, b bool
	for _, a := range r.Assets {
		switch a.Name {
		case ManifestFile:
			m = true
		case ManifestBundleFile:
			b = true
		}
	}
	return m && b
}

// promotion is a verified candidate: a manifest the promote workflow signed, for the product tag its release names.
type promotion struct {
	tag      string
	manifest []byte
	bundle   []byte
	m        *verify.Manifest
}

// errNoPromotion says there is no promotion release listed (for the tag asked for): nothing was promoted.
var errNoPromotion = errors.New("no promotion")

// noneVerified is what a pass returns when there were candidates and none verified: the newest candidate's refusal, which is what the check records (Settings shows it) and backs off on, the
// same back-off as any verification failure (an hour, doubling to a day), so junk costs one small pass an hour at most.
type noneVerified struct {
	version string // the newest candidate's version, from its name
	failure *verify.Failure
	junk    int
}

func (e *noneVerified) Error() string {
	return fmt.Sprintf("%d promotion release(s) listed and none verified (newest: %s)", e.junk, e.failure.Error())
}

// findPromotion lists the newest releases and returns the highest-sequence promotion that verifies, for any product tag, or, when onlyTag is not "", for that tag. A candidate is a non-draft
// release named promote-<tag>-<n> carrying both assets, at most maxPromoteCandidates of them; it is skipped (and counted) unless its manifest verifies against the pinned promote identity,
// names the tag and the sequence its release names. It returns errNoPromotion when none is listed, and a *noneVerified when there were candidates and none verified.
func (c *checker) findPromotion(ctx context.Context, onlyTag string) (p *promotion, junk int, err error) {
	raw, err := c.cfg.Fetcher.Get(ctx, c.cfg.ReleasesURL, maxListBytes)
	if err != nil {
		return nil, 0, err
	}
	var list []listedRelease
	if json.Unmarshal(raw, &list) != nil {
		return nil, 0, errors.New("the list of releases is not a list")
	}
	if len(list) > 100 {
		list = list[:100] // a page is 30: anything more is not read
	}
	looked := 0
	var rehearsal *promotion
	var first *noneVerified
	skip := func(version string, f *verify.Failure) {
		junk++
		if first == nil {
			first = &noneVerified{version: version, failure: f}
		}
	}
	for _, r := range list {
		sm := promoteTagRe.FindStringSubmatch(r.TagName)
		if sm == nil || r.Draft || !r.hasPromotionAssets() {
			continue
		}
		if onlyTag != "" && sm[1] != onlyTag {
			continue
		}
		if looked++; looked > maxPromoteCandidates {
			break
		}
		seq, _ := strconv.ParseInt(sm[2], 10, 64)
		version := strings.TrimPrefix(sm[1], "v")
		base := c.cfg.DownloadBase + r.TagName + "/"
		manifest, merr := c.cfg.Fetcher.Get(ctx, base+ManifestFile, verify.MaxManifestBytes)
		var bundle []byte
		var berr error
		if merr == nil {
			bundle, berr = c.cfg.Fetcher.Get(ctx, base+ManifestBundleFile, verify.MaxBundleBytes)
		}
		if merr != nil || berr != nil {
			if notFound(merr) || notFound(berr) {
				junk++ // listed with the assets and not there (being published, or gone): not a candidate, and not a refusal: nothing was promoted yet
				continue
			}
			if errors.Is(merr, verify.ErrTooLarge) || errors.Is(berr, verify.ErrTooLarge) {
				skip(version, &verify.Failure{Stage: verify.StageManifestDocument, Reason: "a file of the promotion release is larger than the limit"}) // junk, not an outage: it must not hide the releases behind it
				continue
			}
			if merr != nil {
				return nil, junk, merr
			}
			return nil, junk, berr
		}
		m, verr := verify.PromotionManifest(c.cfg.Verifier, c.cfg.Pins, manifest, bundle)
		if verr != nil {
			var f *verify.Failure
			if !errors.As(verr, &f) {
				f = &verify.Failure{Stage: verify.StageManifestDocument, Reason: verr.Error()}
			}
			skip(version, f)
			continue
		}
		if m.Tag != sm[1] || m.Sequence != seq {
			skip(version, &verify.Failure{Stage: verify.StageManifestRules, Reason: "the manifest names another tag or sequence than its release"})
			continue
		}
		if m.Prerelease {
			// A rehearsal's manifest is signed and valid but is never what a Coral updates to: it is skipped before the highest sequence is picked, so a rehearsal with a higher
			// sequence than the real promotion of the same tag cannot hide the real one (Security's #6393 follow-up). When there is no other candidate the highest one is kept, so the
			// rules refuse it by its flag as before and the check says why nothing was staged.
			if rehearsal == nil || m.Sequence > rehearsal.m.Sequence {
				rehearsal = &promotion{tag: r.TagName, manifest: manifest, bundle: bundle, m: m}
			}
			continue
		}
		if p == nil || m.Sequence > p.m.Sequence {
			p = &promotion{tag: r.TagName, manifest: manifest, bundle: bundle, m: m}
		}
	}
	if p == nil && rehearsal != nil {
		p = rehearsal
	}
	if p == nil {
		if first != nil {
			first.junk = junk
			return nil, junk, first
		}
		return nil, junk, errNoPromotion
	}
	if junk > 0 {
		log.Printf("[update] %d promotion release(s) listed did not verify and were skipped", junk)
	}
	return p, junk, nil
}
