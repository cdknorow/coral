package testrig

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/updater/stage"
	"github.com/subgentic/coral/internal/updater/verify"
)

const installed = "1.4.130"

// rig is one check's world: a fake Sigstore, a fake GitHub with a release promoted on it, and the updater's own stage.Config pointed at them.
type rig struct {
	t   *testing.T
	s   *Signer
	gh  *FakeGitHub
	cfg stage.Config
}

func newRig(t *testing.T) *rig {
	t.Helper()
	s, err := NewSigner()
	require.NoError(t, err)
	gh := NewFakeGitHub()
	t.Cleanup(gh.Close)
	r := &rig{t: t, s: s, gh: gh}
	r.cfg = stage.Config{DataDir: t.TempDir(), Installed: installed, Pins: Pins(), Verifier: verify.Sigstore{Trust: s.Trust()}, Asset: Asset}
	gh.Config(&r.cfg)
	return r
}

func (r *rig) publish(sp Spec) *Release {
	r.t.Helper()
	rel, err := r.s.NewRelease(sp)
	require.NoError(r.t, err)
	r.gh.Publish(rel, true)
	return rel
}

func (r *rig) check() stage.Result {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return stage.Check(ctx, r.cfg)
}

// nothingStaged: a refused release leaves no package behind.
func (r *rig) nothingStaged() {
	r.t.Helper()
	_, err := stage.LoadStaged(r.cfg.DataDir)
	assert.ErrorIs(r.t, err, stage.ErrNoStaged, "nothing is staged")
}

// The updater's own check, with real signatures from a Sigstore we control: a promoted release is downloaded through a redirect, verified, staged byte for
// byte, found again as already staged, and not offered once it is the installed version.
func TestCheckStagesAReleaseSignedUnderTheFakeSigstore(t *testing.T) {
	r := newRig(t)
	rel := r.publish(Spec{Version: "1.4.131", Sequence: 7})
	res := r.check()
	require.Equal(t, stage.ResultStaged, res.Result, "%+v", res)
	assert.Equal(t, "1.4.131", res.Version)
	staged, err := stage.LoadStaged(r.cfg.DataDir)
	require.NoError(t, err)
	got, err := os.ReadFile(staged.PackagePath)
	require.NoError(t, err)
	assert.Equal(t, rel.Package, got, "the staged package is the released one, byte for byte")
	assert.Equal(t, sum(rel.Package), staged.Record.PackageSHA256)
	assert.Equal(t, int64(7), staged.Record.Sequence)
	assert.Positive(t, r.gh.Hits["repos/"+FakeRepo+"/releases"], "the list of the newest releases was asked for")
	assert.Positive(t, r.gh.Hits[FakeRepo+"/releases/download/"+PromotionTag("v1.4.131", 7)+"/promote.json"], "and the manifest came from the promotion release promote-v1.4.131-7 (#6393)")
	assert.Zero(t, r.gh.Hits[FakeRepo+"/releases/download/v1.4.131/promote.json"], "never from the product release: an immutable release cannot take it")

	assert.Equal(t, stage.ResultAlreadyStaged, r.check().Result, "the same release again")

	r.cfg.Installed = "1.4.131"
	assert.Equal(t, stage.ResultUpToDate, r.check().Result, "once it is installed there is nothing to stage")
}

// A release that is not promoted, or whose files are not all there, is "not promoted", never an error and never a stage.
func TestCheckWithNothingPromotedStagesNothing(t *testing.T) {
	r := newRig(t)
	assert.Equal(t, stage.ResultNotPromoted, r.check().Result, "no release at all")
	r.nothingStaged()
	r = newRig(t)
	r.publish(Spec{Version: "1.4.131"})
	r.gh.Status["v1.4.131/"+stage.SumsBundleFile] = 404
	assert.Equal(t, stage.ResultNotPromoted, r.check().Result, "a signature file that is not there yet")
	r.nothingStaged()
}
