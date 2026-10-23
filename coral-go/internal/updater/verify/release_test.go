package verify

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSigner stands in for Sigstore in the cases a real signer cannot make (a forged, expired or foreign manifest): a "bundle" is the artifact's hash,
// the signer's identity and the commit it was built from, and verifying compares them with what is asked for, exactly as Sigstore does.
type fakeSigner struct{ calls []Identity }

type fakeBundle struct {
	Artifact string `json:"artifact"`
	Identity Identity
	Commit   string `json:"commit"`
}

func sign(artifact []byte, id Identity, commit string) []byte {
	b, _ := json.Marshal(fakeBundle{Artifact: sha256Hex(artifact), Identity: id, Commit: commit})
	return b
}

func (f *fakeSigner) VerifyBundle(bundleJSON, artifact []byte, want Identity) (Cert, error) {
	f.calls = append(f.calls, want)
	var b fakeBundle
	if err := json.Unmarshal(bundleJSON, &b); err != nil {
		return Cert{}, errors.New("not a bundle")
	}
	if b.Artifact != sha256Hex(artifact) {
		return Cert{}, errors.New("the signature is not over this artifact")
	}
	got := b.Identity
	if got.SAN != want.SAN || got.Issuer != want.Issuer {
		return Cert{}, fmt.Errorf("identity %q (%q) is not %q (%q)", got.SAN, got.Issuer, want.SAN, want.Issuer)
	}
	for _, p := range [][2]string{{want.SourceRepositoryURI, got.SourceRepositoryURI}, {want.SourceRepositoryRef, got.SourceRepositoryRef}, {want.RunnerEnvironment, got.RunnerEnvironment}, {want.BuildTrigger, got.BuildTrigger}} {
		if p[0] != "" && p[0] != p[1] {
			return Cert{}, fmt.Errorf("extension %q is not %q", p[1], p[0])
		}
	}
	return Cert{SAN: got.SAN, SourceRepositoryDigest: b.Commit}, nil
}

var (
	testNow    = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	testCommit = strings.Repeat("ab", 20)
	testPins   = withPromoteID(DefaultPins())
)

// release is a well-formed promoted release 1.4.130 for linux amd64, built by the fake signer; each test changes one thing.
type release struct {
	m         Manifest
	sums      []byte
	manifest  []byte
	cand      Candidate
	pkg       []byte
	st        State
	installed string
}

func asset(version string) string { return "coral-linux-amd64-" + version + ".tar.gz" }

func newRelease(t *testing.T) *release {
	t.Helper()
	r := &release{pkg: []byte("the package bytes"), installed: "1.4.125"}
	r.rebuild(func(*Manifest) {}, nil)
	return r
}

// rebuild regenerates the sums, the manifest and the bundles; change edits the manifest before it is written, sums replaces the sums file when not nil.
func (r *release) rebuild(change func(*Manifest), sums []byte) {
	if sums == nil {
		sums = []byte(sha256Hex(r.pkg) + "  " + asset("1.4.130") + "\n" + strings.Repeat("0", 64) + "  Coral-1.4.130-x64.msi\n")
	}
	r.sums = sums
	r.m = Manifest{Schema: 1, Repo: Repo, Tag: "v1.4.130", Version: "1.4.130", SourceSHA: testCommit, SHA256SumsSHA: sha256Hex(sums), Sequence: 7,
		PromotedAt: "2026-10-20T10:00:00Z", ExpiresAt: "2026-11-20T10:00:00Z", Revoked: []string{"1.4.99"}}
	change(&r.m)
	r.manifest, _ = json.Marshal(r.m)
	r.cand = Candidate{
		Manifest: r.manifest, ManifestBundle: sign(r.manifest, promoteIdentity(testPins), ""),
		Sums: r.sums, SumsBundle: sign(r.sums, releaseIdentity(testPins, r.m.Tag), testCommit),
	}
}

func (r *release) verify(v BundleVerifier) (*Record, error) {
	return Release(v, testPins, r.cand, asset, r.st, r.installed, testNow)
}

func TestAWellFormedReleaseIsVerifiedInOrder(t *testing.T) {
	r := newRelease(t)
	f := &fakeSigner{}
	rec, err := r.verify(f)
	require.NoError(t, err)
	assert.Equal(t, "1.4.130", rec.Version)
	assert.Equal(t, sha256Hex(r.pkg), rec.PackageSHA256)
	assert.Equal(t, asset("1.4.130"), rec.Asset)
	// The manifest's signature is checked first, the sums file's second, and for exactly this tag.
	require.Len(t, f.calls, 2)
	assert.Equal(t, "https://github.com/subgentic/coral/.github/workflows/promote.yml@refs/heads/promote", f.calls[0].SAN)
	assert.Equal(t, "https://github.com/subgentic/coral/.github/workflows/release.yml@refs/tags/v1.4.130", f.calls[1].SAN)
	assert.Equal(t, "https://token.actions.githubusercontent.com", f.calls[1].Issuer)
	assert.Equal(t, "refs/tags/v1.4.130", f.calls[1].SourceRepositoryRef)
	// The package is checked against the verified sums entry, at staging and again at install.
	n, err := rec.CheckPackage(strings.NewReader(string(r.pkg)), 1<<20)
	require.NoError(t, err)
	assert.Equal(t, int64(len(r.pkg)), n)
}

// Every case fails at its own stage and returns no record: none of them reaches the installer.
func TestEveryFailureStopsAtItsStageWithNoRecord(t *testing.T) {
	signer := func(r *release, id Identity, commit string, artifact []byte) []byte {
		return sign(artifact, id, commit)
	}
	cases := []struct {
		name  string
		stage string
		edit  func(r *release)
	}{
		{"manifest signed by the release identity, not the promote one", StageManifestSignature, func(r *release) {
			r.cand.ManifestBundle = signer(r, releaseIdentity(testPins, r.m.Tag), "", r.manifest)
		}},
		{"manifest signed by another issuer", StageManifestSignature, func(r *release) {
			id := promoteIdentity(testPins)
			id.Issuer = "https://accounts.google.com"
			r.cand.ManifestBundle = signer(r, id, "", r.manifest)
		}},
		{"manifest signed by another workflow", StageManifestSignature, func(r *release) {
			id := promoteIdentity(testPins)
			id.SAN = "https://github.com/subgentic/coral/.github/workflows/other.yml@refs/heads/promote"
			r.cand.ManifestBundle = signer(r, id, "", r.manifest)
		}},
		{"right promote SAN, wrong ref in the certificate", StageManifestSignature, func(r *release) {
			id := promoteIdentity(testPins)
			id.SourceRepositoryRef = "refs/heads/feature"
			r.cand.ManifestBundle = signer(r, id, "", r.manifest)
		}},
		{"right SAN, run by push not by hand", StageManifestSignature, func(r *release) {
			id := promoteIdentity(testPins)
			id.BuildTrigger = "push"
			r.cand.ManifestBundle = signer(r, id, "", r.manifest)
		}},
		{"a signature over another manifest", StageManifestSignature, func(r *release) {
			r.cand.Manifest = append([]byte{}, r.cand.Manifest...)
			r.cand.Manifest[len(r.cand.Manifest)-2] ^= 1
		}},
		{"manifest with an unknown field", StageManifestDocument, func(r *release) {
			r.manifest = []byte(strings.Replace(string(r.manifest), `"schema":1`, `"schema":1,"force":true`, 1))
			r.cand.Manifest, r.cand.ManifestBundle = r.manifest, sign(r.manifest, promoteIdentity(testPins), "")
		}},
		{"manifest with a missing field", StageManifestDocument, func(r *release) {
			r.manifest = []byte(strings.Replace(string(r.manifest), `"revoked":["1.4.99"]`, `"x":1`, 1))
			r.cand.Manifest, r.cand.ManifestBundle = r.manifest, sign(r.manifest, promoteIdentity(testPins), "")
		}},
		{"a manifest for another repository", StageManifestRules, func(r *release) { r.rebuild(func(m *Manifest) { m.Repo = "someone/coral" }, nil) }},
		{"a prerelease", StageManifestRules, func(r *release) { r.rebuild(func(m *Manifest) { m.Prerelease = true }, nil) }},
		{"installed equal", StageManifestRules, func(r *release) { r.installed = "1.4.130" }},
		{"installed higher (a downgrade)", StageManifestRules, func(r *release) { r.installed = "1.5.0" }},
		{"sequence lower than the last accepted (a replay)", StageManifestRules, func(r *release) { r.st.Sequence = 8 }},
		{"expired", StageManifestRules, func(r *release) {
			r.rebuild(func(m *Manifest) { m.PromotedAt, m.ExpiresAt = "2026-08-01T00:00:00Z", "2026-09-01T00:00:00Z" }, nil)
		}},
		{"from the future", StageManifestRules, func(r *release) {
			r.rebuild(func(m *Manifest) { m.PromotedAt, m.ExpiresAt = "2026-10-20T13:00:00Z", "2026-11-20T13:00:00Z" }, nil)
		}},
		{"a lifetime longer than allowed", StageManifestRules, func(r *release) {
			r.rebuild(func(m *Manifest) { m.ExpiresAt = "2030-01-01T00:00:00Z" }, nil)
		}},
		{"revoked by its own manifest", StageManifestRules, func(r *release) { r.rebuild(func(m *Manifest) { m.Revoked = []string{"1.4.130"} }, nil) }},
		{"revoked by an earlier manifest (sticky)", StageManifestRules, func(r *release) { r.st.Revoked = []string{"1.4.130"} }},
		{"a sums file that is not the one approved", StageSumsDigest, func(r *release) {
			r.cand.Sums = []byte(strings.Repeat("1", 64) + "  " + asset("1.4.130") + "\n")
			r.cand.SumsBundle = sign(r.cand.Sums, releaseIdentity(testPins, r.m.Tag), testCommit)
		}},
		{"sums signed by the promote identity", StageSumsSignature, func(r *release) {
			r.cand.SumsBundle = sign(r.sums, promoteIdentity(testPins), testCommit)
		}},
		{"sums signed for another tag", StageSumsSignature, func(r *release) {
			r.cand.SumsBundle = sign(r.sums, releaseIdentity(testPins, "v1.4.129"), testCommit)
		}},
		{"sums signed by another issuer", StageSumsSignature, func(r *release) {
			id := releaseIdentity(testPins, r.m.Tag)
			id.Issuer = "https://accounts.google.com"
			r.cand.SumsBundle = sign(r.sums, id, testCommit)
		}},
		{"a release built from another commit than the one approved", StageSumsCommit, func(r *release) {
			r.cand.SumsBundle = sign(r.sums, releaseIdentity(testPins, r.m.Tag), strings.Repeat("cd", 20))
		}},
		{"the package named by the API but not in the sums", StageAssetMissing, func(r *release) {
			r.rebuild(func(*Manifest) {}, []byte(strings.Repeat("0", 64)+"  Coral-1.4.130-x64.msi\n"))
		}},
		{"a sums file with a path in a name", StageSumsFormat, func(r *release) {
			r.rebuild(func(*Manifest) {}, []byte(strings.Repeat("0", 64)+"  ../coral-linux-amd64-1.4.130.tar.gz\n"))
		}},
		{"a sums file that lists a name twice", StageSumsFormat, func(r *release) {
			line := strings.Repeat("0", 64) + "  " + asset("1.4.130") + "\n"
			r.rebuild(func(*Manifest) {}, []byte(line+line))
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRelease(t)
			c.edit(r)
			rec, err := r.verify(&fakeSigner{})
			require.Error(t, err)
			assert.Nil(t, rec, "no record: nothing reaches the installer")
			var f *Failure
			require.True(t, errors.As(err, &f), "%v", err)
			assert.Equal(t, c.stage, f.Stage, f.Reason)
		})
	}
}

func TestVersionAndSequenceRules(t *testing.T) {
	for _, c := range []struct {
		name      string
		installed string
		seq       int64
		ok        bool
	}{
		{"much newer than the installed", "1.2.3", 7, true},
		{"just newer", "1.4.129", 7, true},
		{"equal sequence, a retry", "1.4.125", 7, true},
		{"higher state sequence", "1.4.125", 6, true},
		{"lower sequence", "1.4.125", 8, false},
		{"equal version", "1.4.130", 7, false},
		{"a lower version", "1.4.131", 7, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRelease(t)
			r.installed, r.st.Sequence = c.installed, c.seq
			_, err := r.verify(&fakeSigner{})
			assert.Equal(t, c.ok, err == nil, "%v", err)
		})
	}
	// An installed version that is not X.Y.Z is never "older than anything".
	r := newRelease(t)
	r.installed = "dev"
	_, err := r.verify(&fakeSigner{})
	assert.Error(t, err)
}

func TestAPackageOneByteOffIsRefusedAtStagingAndAtInstall(t *testing.T) {
	r := newRelease(t)
	rec, err := r.verify(&fakeSigner{})
	require.NoError(t, err)
	bad := append([]byte{}, r.pkg...)
	bad[3] ^= 1
	_, err = rec.CheckPackage(strings.NewReader(string(bad)), 1<<20)
	var f *Failure
	require.True(t, errors.As(err, &f))
	assert.Equal(t, StagePackage, f.Stage)
	assert.Contains(t, f.Reason, rec.PackageSHA256, "both digests are in the line")
	// Too large is refused whatever it hashes to.
	_, err = rec.CheckPackage(strings.NewReader(string(r.pkg)), 4)
	assert.Error(t, err)
}

func TestParseManifestIsStrict(t *testing.T) {
	good := newRelease(t).manifest
	_, err := ParseManifest(good)
	require.NoError(t, err)
	for name, doc := range map[string]string{
		"data after the document":      string(good) + `{}`,
		"a non-UTC time":               strings.Replace(string(good), `"2026-10-20T10:00:00Z"`, `"2026-10-20T10:00:00+02:00"`, 1),
		"a tag that is not v+version":  strings.Replace(string(good), `"tag":"v1.4.130"`, `"tag":"v1.4.131"`, 1),
		"a version with a suffix":      strings.Replace(strings.Replace(string(good), `"version":"1.4.130"`, `"version":"1.4.130-rc1"`, 1), `"tag":"v1.4.130"`, `"tag":"v1.4.130-rc1"`, 1),
		"an uppercase commit":          strings.Replace(string(good), testCommit, strings.ToUpper(testCommit), 1),
		"sequence 0":                   strings.Replace(string(good), `"sequence":7`, `"sequence":0`, 1),
		"schema 2":                     strings.Replace(string(good), `"schema":1`, `"schema":2`, 1),
		"expiry before promotion":      strings.Replace(string(good), `"expires_at":"2026-11-20T10:00:00Z"`, `"expires_at":"2026-10-01T10:00:00Z"`, 1),
		"a revoked entry that is junk": strings.Replace(string(good), `"1.4.99"`, `"latest"`, 1),
		"not JSON":                     `nope`,
		"empty":                        ``,
	} {
		_, err := ParseManifest([]byte(doc))
		assert.Error(t, err, name)
	}
	_, err = ParseManifest(make([]byte, MaxManifestBytes+1))
	assert.Error(t, err)
}

func TestSumsParsing(t *testing.T) {
	h := strings.Repeat("a", 64)
	got, err := ParseSums([]byte(h + "  one.tar.gz\n" + h + " *two.msi\n"))
	require.NoError(t, err)
	assert.Equal(t, h, got["one.tar.gz"])
	assert.Equal(t, h, got["two.msi"])
	for _, bad := range []string{"", "\n", h + " one", h + "  ", "zz" + h[2:] + "  one", strings.ToUpper(h) + "  one", h + "  a/b", h + "  ..", h[:63] + "  one"} {
		_, err := ParseSums([]byte(bad))
		assert.Error(t, err, "%q", bad)
	}
}

func TestVersionCompare(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{{"1.4.130", "1.4.99", 1}, {"1.4.99", "1.4.130", -1}, {"1.10.0", "1.9.9", 1}, {"2.0.0", "1.999.9999", 1}, {"1.4.1", "1.4.1", 0}} {
		got, err := CompareVersions(c.a, c.b)
		require.NoError(t, err)
		assert.Equal(t, c.want, got, "%s %s", c.a, c.b)
	}
	for _, bad := range []string{"", "v1.4.1", "1.4", "1.4.1-rc1", "1.4.1.1", "01234.1.1", "1.4.x", "1.4.1 "} {
		_, err := ParseVersion(bad)
		assert.Error(t, err, "%q", bad)
	}
}

// #5333: the stable-name copies are listed in SHA256SUMS.txt under both names; a stable name with another digest than its versioned file is refused.
func TestStableAliasesMustCarryTheVersionedFilesDigest(t *testing.T) {
	good := sha256Hex([]byte("the package bytes"))
	other := sha256Hex([]byte("other bytes"))
	build := func(stableDigest string) []byte {
		return []byte(good + "  " + asset("1.4.130") + "\n" + stableDigest + "  coral-linux-amd64.tar.gz\n" + strings.Repeat("0", 64) + "  Coral-1.4.130-x64.msi\n" + strings.Repeat("0", 64) + "  Coral-x64.msi\n")
	}
	// Equal digests under both names: verified, and the installer's asset is the versioned one.
	r := newRelease(t)
	r.rebuild(func(*Manifest) {}, build(good))
	rec, err := r.verify(&fakeSigner{})
	require.NoError(t, err)
	assert.Equal(t, asset("1.4.130"), rec.Asset)
	// A stable name with another digest: refused at the sums format stage, and no record.
	r = newRelease(t)
	r.rebuild(func(*Manifest) {}, build(other))
	rec, err = r.verify(&fakeSigner{})
	require.Error(t, err)
	assert.Nil(t, rec)
	var f *Failure
	require.True(t, errors.As(err, &f))
	assert.Equal(t, StageSumsFormat, f.Stage)
	assert.Contains(t, f.Reason, "coral-linux-amd64.tar.gz")
	// Every pair is checked, not only the first; a release with no stable names (cut before them) is unaffected.
	for stable, versioned := range StableAliases("1.4.130") {
		entries := map[string]string{versioned: good, stable: other}
		assert.Error(t, CheckStableAliases(entries, "1.4.130"), stable)
		entries[stable] = good
		assert.NoError(t, CheckStableAliases(entries, "1.4.130"), stable)
		delete(entries, stable)
		assert.NoError(t, CheckStableAliases(entries, "1.4.130"), "a stable name that is not listed")
	}
	assert.NoError(t, CheckStableAliases(map[string]string{asset("1.4.130"): good}, "1.4.130"))
}

// #5372, found by FuzzParseSums: a name with a control character (a CR at its end reads back as another name once written out again) is refused.
func TestParseSumsRefusesAControlCharacterInAName(t *testing.T) {
	h := strings.Repeat("ab", 32)
	for name, line := range map[string]string{
		"a trailing CR kept after the line's own": h + "  a\r\r\n",
		"a CR inside": h + "  a\rb\n",
		"a tab":       h + "  a\tb\n",
		"an escape":   h + "  a\x1bb\n",
		"a delete":    h + "  a\x7fb\n",
		"a bell":      h + "  \x07x\n",
	} {
		_, err := ParseSums([]byte(line))
		assert.Error(t, err, name)
	}
	got, err := ParseSums([]byte(h + "  Coral.dmg\r\n"))
	require.NoError(t, err, "a CRLF line ending is still fine")
	assert.Equal(t, map[string]string{"Coral.dmg": h}, got)
}
