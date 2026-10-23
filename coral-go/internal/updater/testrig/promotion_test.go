package testrig

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/updater/stage"
	"github.com/subgentic/coral/internal/updater/verify"
)

// Finding the newest promotion (#326): what is published under a promote-<n> tag is not trusted. Junk, a promotion signed by anyone but promote.yml on
// main, and a manifest whose sequence isn't its tag's number are each skipped, and the highest real promotion below them is used.
func TestJunkUnderAPromoteTagIsSkippedAndNeverInstalled(t *testing.T) {
	relCert := ReleaseCert("v1.4.131")
	junk := map[string]func(r *rig, real *Release){
		"an unsigned file": func(r *rig, real *Release) { r.gh.PublishPromotion(2, []byte(`{"schema":1}`), nil) },
		"the manifest signed by release.yml": func(r *rig, real *Release) {
			b, err := r.s.Bundle(real.Manifest, relCert, Omit{})
			require.NoError(t, err)
			r.gh.PublishPromotion(2, real.Manifest, b)
		},
		"the real promotion filed again under the next number": func(r *rig, real *Release) {
			r.gh.PublishPromotion(2, real.Manifest, real.ManifestBundle)
		},
		"a signature from a Sigstore the updater doesn't trust": func(r *rig, real *Release) {
			other, err := NewSigner()
			require.NoError(t, err)
			b, err := other.Bundle(real.Manifest, PromoteCert(), Omit{})
			require.NoError(t, err)
			r.gh.PublishPromotion(2, real.Manifest, b)
		},
	}
	for name, add := range junk {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			real := r.publish(Spec{Version: "1.4.131", Sequence: 1})
			add(r, real)
			res := r.check()
			assert.Equal(t, stage.ResultStaged, res.Result, "%+v", res)
			assert.Equal(t, "1.4.131", res.Version, "the real promotion below the junk")
		})
	}
}

// Junk doesn't block the channel (Security on #143): forty junk names right above the real promotion and one far above it are passed over and the
// real one is found; the workflow's next promotion, numbered above the junk, is found too.
func TestJunkAboveTheRealPromotionDoesNotBlockIt(t *testing.T) {
	const real = int64(5)
	junk := func(r *rig) {
		for n := real + 1; n <= real+40; n++ {
			r.gh.PublishPromotion(n, []byte(`{"schema":1}`), nil)
		}
		r.gh.PublishPromotion(real+1_000_000, []byte(`{"schema":1}`), nil)
	}
	r := newRig(t)
	r.publish(Spec{Version: "1.4.131", Sequence: real})
	junk(r)
	res := r.check()
	require.Equal(t, stage.ResultStaged, res.Result, "%+v", res)
	assert.Equal(t, "1.4.131", res.Version, "the real promotion below the junk")
	assert.Zero(t, r.gh.Hits[FakeRepo+"/releases/download/promote-1000005/"+stage.ManifestFile], "a name far above isn't looked at")

	// The next promotion takes the name above the junk (the highest promote-<n> + 1), and is found by the next check.
	r.publish(Spec{Version: "1.4.132", Sequence: real + 41})
	res = r.check()
	require.Equal(t, stage.ResultStaged, res.Result, "%+v", res)
	assert.Equal(t, "1.4.132", res.Version)

	// A client that has seen nothing finds it the same way.
	fresh := newRig(t)
	fresh.s, fresh.cfg.Verifier = r.s, r.cfg.Verifier
	fresh.gh.Close()
	fresh.gh = r.gh
	fresh.gh.Config(&fresh.cfg)
	res = fresh.check()
	require.Equal(t, stage.ResultStaged, res.Result, "%+v", res)
	assert.Equal(t, "1.4.132", res.Version)
}

// More junk than a check passes over delays the update and never installs anything: the check fails closed.
func TestMoreJunkThanTheAllowanceInstallsNothing(t *testing.T) {
	r := newRig(t)
	r.publish(Spec{Version: "1.4.131", Sequence: 1})
	for n := int64(2); n <= 80; n++ {
		r.gh.PublishPromotion(n, []byte(`{"schema":1}`), nil)
	}
	res := r.check()
	assert.Equal(t, stage.ResultFailed, res.Result, "%+v", res)
	r.nothingStaged()
}

// A check finds the newest promotion by name from the highest sequence it has seen, in a few requests; the next check looks once above it.
func TestTheNewestPromotionIsFoundInAFewRequests(t *testing.T) {
	r := newRig(t)
	r.publish(Spec{Version: "1.4.131", Sequence: 300})
	res := r.check()
	require.Equal(t, stage.ResultStaged, res.Result, "%+v", res)
	probes := 0
	for p, n := range r.gh.Hits {
		if strings.Contains(p, "/promote-") && strings.HasSuffix(p, "/"+stage.ManifestFile) {
			probes += n
		}
	}
	assert.LessOrEqual(t, probes, 20, "about two requests per bit of the sequence, from nothing seen")
	st, err := verify.LoadState(filepath.Join(r.cfg.DataDir, stage.UpdatesDir))
	require.NoError(t, err)
	assert.Equal(t, int64(300), st.Seen, "the next check starts from here")

	r.gh.Hits = map[string]int{}
	assert.Equal(t, stage.ResultAlreadyStaged, r.check().Result)
	assert.Equal(t, 1, r.gh.Hits[FakeRepo+"/releases/download/promote-301/"+stage.ManifestFile], "one look above what was seen")
	assert.Zero(t, r.gh.Hits[FakeRepo+"/releases/download/promote-1/"+stage.ManifestFile], "no search from the start")
}

// A newer promotion appears: the next check finds it from where it left off.
func TestANewerPromotionIsFoundByTheNextCheck(t *testing.T) {
	r := newRig(t)
	r.publish(Spec{Version: "1.4.131", Sequence: 4})
	require.Equal(t, stage.ResultStaged, r.check().Result)
	r.publish(Spec{Version: "1.4.132", Sequence: 5})
	res := r.check()
	require.Equal(t, stage.ResultStaged, res.Result, "%+v", res)
	assert.Equal(t, "1.4.132", res.Version)
}

// The Windows rollback reads the installed version's manifest from promote-v<version>, its first promotion, and the sums and the package from its
// product release (#326).
func TestTheRollbackManifestComesFromPromoteV(t *testing.T) {
	r := newRig(t)
	r.cfg.Rollback = true
	inst, err := r.s.NewRelease(Spec{Version: installed, Sequence: 1})
	require.NoError(t, err)
	r.gh.Publish(inst, false)
	r.publish(Spec{Version: "1.4.131", Sequence: 2})
	require.Equal(t, stage.ResultStaged, r.check().Result)
	_, err = stage.LoadRollback(r.cfg.DataDir, installed)
	require.NoError(t, err, "the installed version's package is kept")
	assert.Positive(t, r.gh.Hits[FakeRepo+"/releases/download/promote-v"+installed+"/"+stage.ManifestFile])
	assert.Positive(t, r.gh.Hits[FakeRepo+"/releases/download/v"+installed+"/"+stage.SumsFile])
	assert.Zero(t, r.gh.Hits[FakeRepo+"/releases/download/v"+installed+"/"+stage.ManifestFile], "never from the product tag")
}
