package verify

import (
	"os"
	"testing"

	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The offline check of a REAL promotion (#6290, Security's addition before 160 is tagged). A test and nothing else: it is not a command, it is not built into any program, and it skips unless it is handed files.
//
// What it proves: the promote manifest that the real promote.yml signed (promote.json and its Sigstore bundle, downloaded from the release it promoted) verifies against the identity the BUILD PINS: DefaultPins'
// promote repository, repository id, promote.yml, refs/heads/promote and workflow_dispatch, through the same VerifyBundle the updater uses, offline against the embedded trusted root. A pass means the pins in this
// binary are exactly what the real workflow produces; a fail is found before the tag, not as a refusal at the first promotion. It deliberately stops before the manifest's RULES (a prerelease is refused by
// design: that is not what is being proved).
//
//	CORAL_REAL_PROMOTE_MANIFEST=/path/promote.json CORAL_REAL_PROMOTE_BUNDLE=/path/promote.json.sigstore.json \
//	  go test ./internal/updater/verify/ -run TestARealPromotionVerifiesAgainstTheBuildPins -count=1 -v
//
// CORAL_REAL_TRUST_ROOT=/path/trusted_root.json replaces the embedded trusted root (when Sigstore has rotated its keys since this build's snapshot).
const (
	envRealManifest = "CORAL_REAL_PROMOTE_MANIFEST"
	envRealBundle   = "CORAL_REAL_PROMOTE_BUNDLE"
	envRealRoot     = "CORAL_REAL_TRUST_ROOT"
)

// checkPromotion verifies manifest and bundle against id with trust, and reads the manifest strictly. It returns what the certificate says.
func checkPromotion(manifest, bundle []byte, trust root.TrustedMaterial, id Identity) (Cert, *Manifest, error) {
	m, err := ParseManifest(manifest)
	if err != nil {
		return Cert{}, nil, err
	}
	cert, err := Sigstore{Trust: trust}.VerifyBundle(bundle, manifest, id)
	if err != nil {
		return Cert{}, m, err
	}
	return cert, m, nil
}

func TestARealPromotionVerifiesAgainstTheBuildPins(t *testing.T) {
	mp, bp := os.Getenv(envRealManifest), os.Getenv(envRealBundle)
	if mp == "" || bp == "" {
		t.Skipf("set %s and %s to a real promote.json and its bundle to run this", envRealManifest, envRealBundle)
	}
	manifest, err := os.ReadFile(mp)
	require.NoError(t, err)
	bundle, err := os.ReadFile(bp)
	require.NoError(t, err)
	var trust root.TrustedMaterial
	if rp := os.Getenv(envRealRoot); rp != "" {
		trust, err = root.NewTrustedRootFromPath(rp)
	} else {
		trust, err = EmbeddedTrust()
	}
	require.NoError(t, err)

	p := DefaultPins()
	id := promoteIdentity(p)
	cert, m, err := checkPromotion(manifest, bundle, trust, id)
	require.NoError(t, err, "the real promote manifest does not verify against the pins this build carries")
	wantSAN := "https://github.com/" + p.PromoteRepo + "/" + p.PromoteWorkflow + "@" + p.PromoteRef
	assert.Equal(t, wantSAN, cert.SAN)
	assert.Equal(t, "refs/heads/promote", cert.SourceRepositoryRef)
	assert.Equal(t, p.PromoteRepoID, cert.SourceRepositoryID)
	assert.Equal(t, Repo, m.Repo, "the manifest names the repository whose releases are installed")
	t.Logf("verified: SAN %s, ref %s, repository id %s, commit %s; manifest: tag %s, version %s, sequence %d, prerelease %v, source_sha %s",
		cert.SAN, cert.SourceRepositoryRef, cert.SourceRepositoryID, cert.SourceRepositoryDigest, m.Tag, m.Version, m.Sequence, m.Prerelease, m.SourceSHA)
}

// The same pipeline on what is in the repository, so the check itself is proved not to be vacuous: the real, signed v1.4.120 bundle verifies against its own identity (a positive control through checkPromotion's
// code path: the same strict parse is replaced here by the verifier call because the fixture is a sums file, not a manifest), and is refused for the identity a promote manifest must have. A byte changed in the
// artifact, and a wrong trusted root, are refused too.
func TestTheOfflineCheckAcceptsARealSignatureAndRefusesTheWrongIdentity(t *testing.T) {
	sums, bundle, v := realFixture(t)
	release := releaseIdentity(oldHomePins(), "v1.4.120")
	_, err := v.VerifyBundle(bundle, sums, release)
	require.NoError(t, err, "positive control: the real bundle verifies against its own identity")

	_, err = v.VerifyBundle(bundle, sums, promoteIdentity(DefaultPins()))
	assert.Error(t, err, "a release signature is not a promotion under the build's promote pins")

	for name, changed := range map[string]func(Pins) Pins{
		"another promote ref":  func(p Pins) Pins { p.PromoteRef = "refs/heads/main"; return p },
		"another workflow":     func(p Pins) Pins { p.PromoteWorkflow = ".github/workflows/release.yml"; return p },
		"another promote repo": func(p Pins) Pins { p.PromoteRepo = "someone/coral"; return p },
		"another repo id":      func(p Pins) Pins { p.PromoteRepoID = "1"; return p },
	} {
		_, err := v.VerifyBundle(bundle, sums, promoteIdentity(changed(DefaultPins())))
		assert.Error(t, err, name)
	}

	flipped := append([]byte(nil), sums...)
	flipped[0] ^= 1
	_, err = v.VerifyBundle(bundle, flipped, release)
	assert.Error(t, err, "a signature over other bytes")

	_, _, err = checkPromotion([]byte(`{"not":"a manifest"}`), bundle, v.Trust, promoteIdentity(DefaultPins()))
	assert.Error(t, err, "a document that is not a manifest stops at the strict parse")
	_, _, err = checkPromotion(nil, bundle, v.Trust, promoteIdentity(DefaultPins()))
	assert.Error(t, err)
}
