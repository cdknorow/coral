package verify

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The real thing: v1.4.120's SHA256SUMS.txt and the bundle release.yml signed it with (cosign v3), verified with sigstore-go against a snapshot of
// the public Sigstore trusted root, with no network: the signature, the chain, the certificate timestamp, the transparency log's inclusion proof and
// the observer timestamp, then the pinned release identity for that exact tag.
func realFixture(t *testing.T) (sums, bundle []byte, v Sigstore) {
	t.Helper()
	sums, err := os.ReadFile("testdata/SHA256SUMS.txt")
	require.NoError(t, err)
	bundle, err = os.ReadFile("testdata/SHA256SUMS.txt.sigstore.json")
	require.NoError(t, err)
	tr, err := root.NewTrustedRootFromPath("testdata/trusted_root.json")
	require.NoError(t, err)
	return sums, bundle, Sigstore{Trust: tr}
}

func TestTheRealV14120BundleVerifiesAgainstTheReleaseIdentity(t *testing.T) {
	sums, bundle, v := realFixture(t)
	id := releaseIdentity(oldHomePins(), "v1.4.120")
	cert, err := v.VerifyBundle(bundle, sums, id)
	require.NoError(t, err)
	assert.Equal(t, id.SAN, cert.SAN)
	assert.Equal(t, "https://github.com/cdknorow/coral", cert.SourceRepositoryURI)
	assert.Equal(t, "refs/tags/v1.4.120", cert.SourceRepositoryRef)
	assert.Regexp(t, `^[0-9a-f]{40}$`, cert.SourceRepositoryDigest, "the commit the release was built from")
}

func TestTheRealBundleIsRefusedForEveryWrongPin(t *testing.T) {
	sums, bundle, v := realFixture(t)
	right := releaseIdentity(oldHomePins(), "v1.4.120")
	wrong := map[string]func(Identity) Identity{
		"another tag":               func(i Identity) Identity { i.SAN = releaseSAN(oldHomePins().Repo, "v1.4.121"); return i },
		"another repository":        func(i Identity) Identity { i.SAN = releaseSAN("someone/coral", "v1.4.120"); return i },
		"another workflow":          func(i Identity) Identity { i.SAN = strings.Replace(i.SAN, "release.yml", "promote.yml", 1); return i },
		"the promote identity":      func(i Identity) Identity { return promoteIdentity(oldHomePins()) },
		"another issuer":            func(i Identity) Identity { i.Issuer = "https://accounts.google.com"; return i },
		"another ref extension":     func(i Identity) Identity { i.SourceRepositoryRef = "refs/tags/v1.4.121"; return i },
		"another source repository": func(i Identity) Identity { i.SourceRepositoryURI = "https://github.com/someone/coral"; return i },
		"another trigger":           func(i Identity) Identity { i.BuildTrigger = "workflow_dispatch"; return i },
		"another repository id":     func(i Identity) Identity { i.SourceRepositoryID = "1160696365"; return i }, // the name and everything else right, the id not: a re-created name (#5536)
		"no repository id":          func(i Identity) Identity { i.SourceRepositoryID = ""; return i },
		"another runner":            func(i Identity) Identity { i.RunnerEnvironment = "self-hosted"; return i },
		"a prefix of the SAN":       func(i Identity) Identity { i.SAN = strings.TrimSuffix(i.SAN, "0"); return i },
	}
	for name, change := range wrong {
		_, err := v.VerifyBundle(bundle, sums, change(right))
		assert.Error(t, err, name)
	}
	// And the right one still passes, so the failures above are the pins.
	_, err := v.VerifyBundle(bundle, sums, right)
	require.NoError(t, err)
}

func TestTheRealBundleIsRefusedWhenAnythingElseChanges(t *testing.T) {
	sums, bundle, v := realFixture(t)
	id := releaseIdentity(oldHomePins(), "v1.4.120")
	// Another artifact.
	other := append([]byte{}, sums...)
	other[0] ^= 1
	_, err := v.VerifyBundle(bundle, other, id)
	assert.Error(t, err, "a signature over another sums file")
	// No trusted root.
	_, err = Sigstore{}.VerifyBundle(bundle, sums, id)
	assert.Error(t, err)
	// An empty or oversize bundle.
	_, err = v.VerifyBundle(nil, sums, id)
	assert.Error(t, err)
	_, err = v.VerifyBundle(make([]byte, MaxBundleBytes+1), sums, id)
	assert.Error(t, err)
	// A bundle without its transparency log entry, and one whose log time was moved: no inclusion proof, or a proof that no longer holds.
	var doc map[string]any
	require.NoError(t, json.Unmarshal(bundle, &doc))
	vm := doc["verificationMaterial"].(map[string]any)
	entries := vm["tlogEntries"].([]any)
	require.NotEmpty(t, entries)
	vm["tlogEntries"] = []any{}
	noLog, _ := json.Marshal(doc)
	_, err = v.VerifyBundle(noLog, sums, id)
	assert.Error(t, err, "no transparency log entry")
	vm["tlogEntries"] = entries
	entries[0].(map[string]any)["integratedTime"] = "1"
	moved, _ := json.Marshal(doc)
	_, err = v.VerifyBundle(moved, sums, id)
	assert.Error(t, err, "a log time outside the certificate's validity")
	// A certificate with a changed byte.
	delete(vm, "tlogEntries")
	cert := vm["certificate"].(map[string]any)
	raw := cert["rawBytes"].(string)
	cert["rawBytes"] = raw[:40] + strings.Map(func(r rune) rune {
		if r == 'A' {
			return 'B'
		}
		return 'A'
	}, raw[40:41]) + raw[41:]
	vm["tlogEntries"] = entries
	entries[0].(map[string]any)["integratedTime"] = "1"
	broken, _ := json.Marshal(doc)
	_, err = v.VerifyBundle(broken, sums, id)
	assert.Error(t, err, "a tampered certificate")
}

// The whole order with the real bundle as the release half: a manifest approving v1.4.120's real SHA256SUMS.txt and commit goes through
// Release, with the manifest's own signature made by the fake signer (a real promote workflow does not exist yet).
func TestReleaseWithTheRealSumsBundle(t *testing.T) {
	sums, bundle, v := realFixture(t)
	cert, err := v.VerifyBundle(bundle, sums, releaseIdentity(oldHomePins(), "v1.4.120"))
	require.NoError(t, err)
	m := Manifest{Schema: 1, Repo: oldHomePins().Repo, Tag: "v1.4.120", Version: "1.4.120", SourceSHA: cert.SourceRepositoryDigest, SHA256SumsSHA: sha256Hex(sums), Sequence: 3,
		PromotedAt: "2026-10-20T10:00:00Z", ExpiresAt: "2026-11-20T10:00:00Z", Revoked: []string{}}
	doc, _ := json.Marshal(m)
	both := splitVerifier{manifest: &fakeSigner{}, release: v}
	c := Candidate{Manifest: doc, ManifestBundle: sign(doc, promoteIdentity(oldHomePins()), ""), Sums: sums, SumsBundle: bundle}
	rec, err := Release(both, oldHomePins(), c, func(ver string) string { return "coral-linux-amd64-" + ver + ".tar.gz" }, State{}, "1.4.119", testNow)
	require.NoError(t, err)
	assert.Equal(t, "bbc5ee38ac4a36315c2041c4052ba0e98bbd3be06917559f7b539b0399abb0d5", rec.PackageSHA256)
	// The same real bundle with a manifest naming another commit: refused at the commit stage.
	m.SourceSHA = strings.Repeat("ee", 20)
	doc, _ = json.Marshal(m)
	c.Manifest, c.ManifestBundle = doc, sign(doc, promoteIdentity(oldHomePins()), "")
	_, err = Release(both, oldHomePins(), c, func(ver string) string { return "coral-linux-amd64-" + ver + ".tar.gz" }, State{}, "1.4.119", testNow)
	var f *Failure
	require.ErrorAs(t, err, &f)
	assert.Equal(t, StageSumsCommit, f.Stage)
}

// splitVerifier sends the manifest's bundle to one verifier and the release's to another (by the identity asked for).
type splitVerifier struct {
	manifest BundleVerifier
	release  BundleVerifier
}

func (s splitVerifier) VerifyBundle(b, a []byte, id Identity) (Cert, error) {
	if id.BuildTrigger == PromoteTrigger {
		return s.manifest.VerifyBundle(b, a, id)
	}
	return s.release.VerifyBundle(b, a, id)
}
