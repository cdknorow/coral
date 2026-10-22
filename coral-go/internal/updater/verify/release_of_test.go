package verify

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #162 (b): ReleaseOf verifies the release of exactly one version for the Windows rollback: the same chain as Release (the manifest's signature,
// the sums file it names and its signature for that tag and commit, the package's entry), with a re-install's rules. The version Coral runs now
// is put back even though it isn't newer, its sequence is below the last accepted, and its manifest has expired; a revoked version, another
// version, a mismatched tag or a prerelease is never put back, and nothing in the chain is relaxed.
func TestReleaseOfIsTheSameChainWithAReinstallsRules(t *testing.T) {
	r := newRelease(t)
	later := time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC) // long after the manifest expired
	st := State{Sequence: 99}                            // a later version was accepted since
	rec, err := ReleaseOf(&fakeSigner{}, testPins, r.cand, asset, "1.4.130", st, later)
	require.NoError(t, err)
	assert.Equal(t, "1.4.130", rec.Version)
	assert.Equal(t, sha256Hex(r.pkg), rec.PackageSHA256)
	_, err = Release(&fakeSigner{}, testPins, r.cand, asset, st, "1.4.130", later)
	assert.Error(t, err, "Release (an update) refuses the same release: not newer, a lower sequence, expired")

	for name, tc := range map[string]struct {
		change  func(*Manifest)
		version string
		st      State
	}{
		"another version":             {func(*Manifest) {}, "1.4.129", State{}},
		"a tag that isn't v+version":  {func(m *Manifest) { m.Tag = "v1.4.131" }, "1.4.130", State{}},
		"revoked by its own manifest": {func(m *Manifest) { m.Revoked = []string{"1.4.130"} }, "1.4.130", State{}},
		"revoked by a later manifest": {func(*Manifest) {}, "1.4.130", State{Revoked: []string{"1.4.130"}}},
		"a prerelease":                {func(m *Manifest) { m.Prerelease = true }, "1.4.130", State{}},
		"another repository":          {func(m *Manifest) { m.Repo = "someone/else" }, "1.4.130", State{}},
		"promoted in the future":      {func(m *Manifest) { m.PromotedAt = "2027-04-01T00:00:00Z"; m.ExpiresAt = "2027-04-20T00:00:00Z" }, "1.4.130", State{}},
	} {
		r := newRelease(t)
		r.rebuild(tc.change, nil)
		_, err := ReleaseOf(&fakeSigner{}, testPins, r.cand, asset, tc.version, tc.st, later)
		assert.Error(t, err, name)
	}

	// The chain itself: a manifest signed by anyone but the promote workflow, and sums built from another commit, are refused as before.
	r = newRelease(t)
	r.cand.ManifestBundle = sign(r.manifest, releaseIdentity(testPins, r.m.Tag), "")
	_, err = ReleaseOf(&fakeSigner{}, testPins, r.cand, asset, "1.4.130", State{}, later)
	assert.Error(t, err, "the manifest must be the promote workflow's")
	r = newRelease(t)
	r.cand.SumsBundle = sign(r.sums, releaseIdentity(testPins, r.m.Tag), "ff"+testCommit[2:])
	_, err = ReleaseOf(&fakeSigner{}, testPins, r.cand, asset, "1.4.130", State{}, later)
	assert.Error(t, err, "the sums must be built from the manifest's commit")
}
