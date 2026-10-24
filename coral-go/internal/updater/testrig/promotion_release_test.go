package testrig

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/updater/stage"
	"github.com/subgentic/coral/internal/updater/verify"
)

// #6393: immutable releases refuse a new asset on a published release, so a promotion is a release of its own, promote-<tag>-<sequence>. The client lists the newest releases and takes
// the highest-sequence promotion whose manifest the promote workflow signed; a name decides nothing. These are the cases Security listed.

func (r *rig) promoHits() int {
	n := 0
	for p, c := range r.gh.Hits {
		if strings.Contains(p, "/releases/download/promote-") {
			n += c
		}
	}
	return n
}

func TestTwoPromotionsTakeTheHighestSequence(t *testing.T) {
	r := newRig(t)
	r.publish(Spec{Version: "1.4.131", Sequence: 5})
	r.publish(Spec{Version: "1.4.132", Sequence: 9})
	res := r.check()
	require.Equal(t, stage.ResultStaged, res.Result, "%+v", res)
	assert.Equal(t, "1.4.132", res.Version, "the highest sequence is the newest promotion")

	// the highest sequence wins even when a LOWER one is listed newer (a name and an order decide nothing)
	r3 := newRig(t)
	r3.publish(Spec{Version: "1.4.132", Sequence: 9})
	r3.publish(Spec{Version: "1.4.131", Sequence: 5}) // published after, listed first
	res3 := r3.check()
	require.Equal(t, stage.ResultStaged, res3.Result, "%+v", res3)
	assert.Equal(t, "1.4.132", res3.Version, "sequence 9 outranks the newer-listed 5")

	// the second promotion of the same version (a revocation) is a new release with a higher sequence, and it is the one taken
	r2 := newRig(t)
	r2.publish(Spec{Version: "1.4.131", Sequence: 5})
	r2.publish(Spec{Version: "1.4.131", Sequence: 8, Revoked: []string{"1.4.120"}})
	require.Equal(t, stage.ResultStaged, r2.check().Result)
	st, err := verify.LoadState(r2.cfg.DataDir + "/" + stage.UpdatesDir)
	require.NoError(t, err)
	assert.Equal(t, []string{"1.4.120"}, st.Revoked, "the manifest of sequence 8 was the one read: its revocation counts")
}

func TestJunkPromotionReleasesAreSkippedAndTheRealOneIsTaken(t *testing.T) {
	r := newRig(t)
	rel := r.publish(Spec{Version: "1.4.131", Sequence: 5})
	// newer than the real promotion, so they are looked at first
	r.gh.PublishPromotionWithoutAssets(PromotionTag("v1.4.140", 99))
	r.gh.PublishPromotion(PromotionTag("v1.4.131", 50), rel.Manifest, []byte("not a bundle"))                        // a bad bundle
	r.gh.PublishPromotion(PromotionTag("v1.4.150", 60), rel.Manifest, rel.ManifestBundle)                            // a real manifest under another tag
	r.gh.PublishPromotion(PromotionTag("v1.4.131", 6), rel.Manifest, rel.ManifestBundle)                             // the right tag, a sequence the manifest does not carry
	r.gh.PublishPromotion(PromotionTag("v1.4.160", 70), []byte(strings.Repeat("x", verify.MaxManifestBytes+1)), nil) // a manifest over the cap
	// a real, signed manifest of ANOTHER version under this version's name with the sequence its manifest carries: the name says v1.4.131, the manifest says v1.4.140, so it is junk
	other, err := r.s.NewRelease(Spec{Version: "1.4.140", Sequence: 99})
	require.NoError(t, err)
	r.gh.PublishPromotion(PromotionTag("v1.4.131", 99), other.Manifest, other.ManifestBundle)
	res := r.check()
	require.Equal(t, stage.ResultStaged, res.Result, "%+v", res)
	assert.Equal(t, "1.4.131", res.Version, "the manifest that names another tag than its release is not taken, though its sequence is the highest")
}

func TestOnlyJunkIsARefusalThatBacksOffAnHour(t *testing.T) {
	r := newRig(t)
	rel := r.publish(Spec{Version: "1.4.131", Sequence: 5})
	r.gh.ListBody = nil
	// drop the real promotion from the list: only junk is left
	r.gh.PublishPromotion(PromotionTag("v1.4.131", 50), rel.Manifest, []byte("not a bundle"))
	r.gh.ListBody = []byte(`[{"tag_name":"` + PromotionTag("v1.4.131", 50) + `","draft":false,"prerelease":true,"assets":[{"name":"promote.json"},{"name":"promote.json.sigstore.json"}]}]`)
	res := r.check()
	assert.Equal(t, stage.ResultFailed, res.Result, "%+v", res)
	assert.Equal(t, verify.StageManifestSignature, res.FailedStage, "candidates and none verified is a refusal, recorded like any other")
	r.nothingStaged()
	st, err := verify.LoadState(r.cfg.DataDir + "/" + stage.UpdatesDir)
	require.NoError(t, err)
	require.NotNil(t, st.LastFailure)
	assert.Equal(t, verify.StageManifestSignature, st.LastFailure.Stage)

	// no faster than an hour: nothing is asked of GitHub until then
	before := r.promoHits()
	lists := r.gh.Hits["repos/"+FakeRepo+"/releases"]
	now := time.Now()
	r.cfg.Now = func() time.Time { return now.Add(59 * time.Minute) }
	assert.Equal(t, stage.ResultBackoff, r.check().Result)
	assert.Equal(t, lists, r.gh.Hits["repos/"+FakeRepo+"/releases"], "no list request inside the hour")
	assert.Equal(t, before, r.promoHits())
	r.cfg.Now = func() time.Time { return now.Add(61 * time.Minute) }
	assert.Equal(t, stage.ResultFailed, r.check().Result, "after the hour one pass is made again")
	assert.Greater(t, r.gh.Hits["repos/"+FakeRepo+"/releases"], lists)
}

// Anyone with write access can list a hundred promotion releases: one pass looks at ten of them, newest first, and downloads no more than their two files each.
func TestAHundredJunkReleasesCostABoundedPass(t *testing.T) {
	r := newRig(t)
	r.publish(Spec{Version: "1.4.131", Sequence: 5}) // the real one is the oldest
	for i := 0; i < 100; i++ {
		r.gh.PublishPromotion(fmt.Sprintf("promote-v1.4.131-%d", 1000+i), []byte("{}"), []byte("{}"))
	}
	res := r.check()
	assert.Equal(t, stage.ResultFailed, res.Result, "ten junk candidates looked at, none verified; the real one is beyond the bound %+v", res)
	assert.LessOrEqual(t, r.promoHits(), 20, "at most ten releases, two files each")
	assert.Positive(t, r.promoHits())
}

func TestAPromotionOfAPreReleaseOrAnExpiredOneStillGoesThroughTheRules(t *testing.T) {
	r := newRig(t)
	r.publish(Spec{Version: "1.4.131", Sequence: 5, Prerelease: true})
	res := r.check()
	assert.Equal(t, stage.ResultFailed, res.Result, "%+v", res)
	assert.Equal(t, verify.StageManifestRules, res.FailedStage, "a verified promotion of a prerelease is refused by the rules, as the rehearsal's real promotion is")
}

// The manifest is read from the promotion release and never from the product release: a product release carrying a promote.json (the old place) is not looked at.
func TestAManifestOnTheProductReleaseIsNotTheOldWayAnyMore(t *testing.T) {
	r := newRig(t)
	rel := r.publish(Spec{Version: "1.4.131", Sequence: 5})
	r.gh.ListBody = []byte(`[{"tag_name":"v1.4.131","draft":false,"prerelease":false,"assets":[{"name":"promote.json"},{"name":"promote.json.sigstore.json"},{"name":"SHA256SUMS.txt"}]}]`)
	_ = rel
	assert.Equal(t, stage.ResultNotPromoted, r.check().Result, "no promotion release listed: nothing is promoted, whatever the product release carries")
	r.nothingStaged()
}

// A draft is not a release: a draft promotion carrying a real, signed manifest of a newer version is never taken, however high its sequence (Security's LOW 4).
func TestADraftPromotionIsNeverTaken(t *testing.T) {
	r := newRig(t)
	r.publish(Spec{Version: "1.4.131", Sequence: 5})
	newer, err := r.s.NewRelease(Spec{Version: "1.4.140", Sequence: 99})
	require.NoError(t, err)
	r.gh.PublishDraftPromotion(PromotionTag("v1.4.140", 99), newer.Manifest, newer.ManifestBundle)
	res := r.check()
	require.Equal(t, stage.ResultStaged, res.Result, "%+v", res)
	assert.Equal(t, "1.4.131", res.Version, "the draft's version and sequence are ignored")
	assert.Zero(t, r.gh.Hits[FakeRepo+"/releases/download/"+PromotionTag("v1.4.140", 99)+"/promote.json"], "its files are not even asked for")
}

// A rehearsal run after the real promotion of the same tag carries a higher sequence: the client skips a pre-release manifest before it picks the highest, so the real promotion is still what it
// stages (Security's #6393 follow-up).
func TestARehearsalWithAHigherSequenceDoesNotHideTheRealPromotion(t *testing.T) {
	r := newRig(t)
	r.publish(Spec{Version: "1.4.131", Sequence: 5})
	rehearsal, err := r.s.NewRelease(Spec{Version: "1.4.131", Sequence: 9, Prerelease: true})
	require.NoError(t, err)
	r.gh.PublishPromotion(PromotionTag("v1.4.131", 9), rehearsal.Manifest, rehearsal.ManifestBundle)
	res := r.check()
	require.Equal(t, stage.ResultStaged, res.Result, "%+v", res)
	assert.Equal(t, "1.4.131", res.Version)
	// before the guard the rehearsal (sequence 9) was picked and then refused by the rules: the result was a refusal and nothing was staged
}

// A revocation published seconds ago can be listed before the download host serves its files. Its 404 stops the pass (the network back-off), so the older promotion it revokes is
// not staged in the meantime; once its files are served, the revocation is the one taken (Security's #337).
func TestANewerPromotionThatIsNotServedYetDoesNotLetAnOlderOneWin(t *testing.T) {
	r := newRig(t)
	r.publish(Spec{Version: "1.4.131", Sequence: 5})
	r.publish(Spec{Version: "1.4.132", Sequence: 6})
	r.publish(Spec{Version: "1.4.131", Sequence: 8, Revoked: []string{"1.4.132"}}) // the revocation of 1.4.132
	newest := PromotionTag("v1.4.131", 8) + "/" + stage.ManifestFile
	r.gh.Status[newest] = 404
	res := r.check()
	assert.Equal(t, stage.ResultFailed, res.Result, "%+v", res)
	assert.Equal(t, "manifest", res.FailedStage, "a network failure, not a refusal")
	assert.NotEmpty(t, res.NextAttempt, "backed off like any download that failed")
	r.nothingStaged()
	assert.Zero(t, r.gh.Hits[FakeRepo+"/releases/download/"+PromotionTag("v1.4.132", 6)+"/"+stage.ManifestFile], "the pass stopped at the newest promotion, before the one it revokes")

	delete(r.gh.Status, newest)
	r.cfg.Now = func() time.Time { return time.Now().Add(time.Hour) }
	res = r.check()
	require.Equal(t, stage.ResultStaged, res.Result, "%+v", res)
	assert.Equal(t, "1.4.131", res.Version, "the revocation (sequence 8) is the newest promotion, and 1.4.132 is revoked")
	st, err := verify.LoadState(r.cfg.DataDir + "/" + stage.UpdatesDir)
	require.NoError(t, err)
	assert.Equal(t, []string{"1.4.132"}, st.Revoked)
}
