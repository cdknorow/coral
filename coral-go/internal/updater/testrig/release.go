package testrig

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/subgentic/coral/internal/updater/verify"
)

// The identities of the rehearsal: nothing of Coral's real release or promote workflow, and an OIDC issuer that cannot exist (.invalid, RFC 6761).
const (
	FakeIssuer = "https://oidc.rehearsal.invalid"
	FakeRepo   = "rehearsal.invalid/coral"
	// FakePromoteRepo is the repository of the fake promote workflow: the release repository itself, as Coral's (#326).
	FakePromoteRepo = FakeRepo
	// The rehearsal repositories' numeric ids: the pins require them (#5536).
	FakeRepoID        = "9000001"
	FakePromoteRepoID = FakeRepoID
	// FakeCommit is the commit the fake release workflow says it built.
	FakeCommit = "0123456789abcdef0123456789abcdef01234567"
)

// Pins are the updater's pins for the fake identities: the same fields as verify.DefaultPins, with the repositories and issuer of the rehearsal.
func Pins() verify.Pins {
	return verify.Pins{
		Repo: FakeRepo, RepoID: FakeRepoID, Issuer: FakeIssuer,
		PromoteRepo: FakePromoteRepo, PromoteRepoID: FakePromoteRepoID, PromoteWorkflow: ".github/workflows/promote.yml", PromoteRef: "refs/heads/main",
		MaxManifestLifetime: 60 * 24 * time.Hour, MaxSkew: 5 * time.Minute,
	}
}

// ReleaseCert is the signing certificate of the fake release workflow for tag.
func ReleaseCert(tag string) CertID {
	return CertID{
		SAN: "https://github.com/" + FakeRepo + "/.github/workflows/release.yml@refs/tags/" + tag, Issuer: FakeIssuer,
		SourceRepositoryURI: "https://github.com/" + FakeRepo, SourceRepositoryRef: "refs/tags/" + tag, SourceRepositoryDigest: FakeCommit,
		RunnerEnvironment: "github-hosted", BuildTrigger: "push", SourceRepositoryID: FakeRepoID,
	}
}

// PromoteCert is the signing certificate of the fake promote workflow.
func PromoteCert() CertID {
	p := Pins()
	return CertID{
		SAN: "https://github.com/" + p.PromoteRepo + "/" + p.PromoteWorkflow + "@" + p.PromoteRef, Issuer: FakeIssuer,
		SourceRepositoryURI: "https://github.com/" + p.PromoteRepo, SourceRepositoryRef: p.PromoteRef, SourceRepositoryDigest: FakeCommit,
		RunnerEnvironment: "github-hosted", BuildTrigger: "workflow_dispatch", SourceRepositoryID: FakePromoteRepoID,
	}
}

// Asset is the Linux amd64 package of a version, as release.yml names it.
func Asset(version string) string { return "coral-linux-amd64-" + version + ".tar.gz" }

// Release is a fake release: the package, the sums file, the promote manifest and the two signatures. Sequence is the promotion's: FakeGitHub
// publishes the manifest as the release promote-<Sequence> (#326).
type Release struct {
	Version, Tag   string
	Sequence       int64
	Package        []byte
	Sums           []byte
	Manifest       []byte
	ManifestBundle []byte
	SumsBundle     []byte
}

// Spec is what a release is built from. The zero value is a good release, promoted as sequence 1; a field is a way to make it wrong.
type Spec struct {
	Version  string
	Sequence int64
	// Files are the package's contents (name to bytes); nil is one small file. Package, when it is set, is the package itself (a tarball built elsewhere) and
	// Files is not used.
	Files   map[string][]byte
	Package []byte
	// Manifest fields, changed to make the manifest wrong (zero values are the good ones).
	ManifestRepo string
	Prerelease   bool
	SourceSHA    string
	PromotedAt   time.Time
	ExpiresAt    time.Time
	Revoked      []string
	// ExtraManifestField adds a field the schema does not have.
	ExtraManifestField bool
	// SumsExtra is appended to the sums file as written (a stable alias with another digest, a malformed line).
	SumsExtra string
	// NoAssetInSums leaves the package out of the sums file.
	NoAssetInSums bool
	// PromoteSigner, ReleaseSigner, PromoteID and ReleaseID change who signed what (nil and zero: the right ones).
	PromoteSigner, ReleaseSigner *Signer
	PromoteID, ReleaseID         *CertID
	// ReleaseTag is the tag the release certificate names, when it is not the manifest's.
	ReleaseTag string
	Omit       Omit
	Now        time.Time
}

// tarGz packs files as a gzip tar, in name order.
func tarGz(files map[string][]byte) []byte {
	var names []string
	for n := range files {
		names = append(names, n)
	}
	sortStrings(names)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, n := range names {
		_ = tw.WriteHeader(&tar.Header{Name: n, Mode: 0o755, Size: int64(len(files[n])), Typeflag: tar.TypeReg})
		_, _ = tw.Write(files[n])
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// NewRelease builds a release as the fake release and promote workflows would: the package, SHA256SUMS.txt (the versioned name and its stable alias,
// the same bytes), the manifest that names the sums file's digest, and a Sigstore bundle for each, signed by s unless the spec says otherwise.
func (s *Signer) NewRelease(sp Spec) (*Release, error) {
	if sp.Version == "" {
		return nil, fmt.Errorf("a release needs a version")
	}
	now := sp.Now
	if now.IsZero() {
		now = time.Now()
	}
	tag := "v" + sp.Version
	files := sp.Files
	if files == nil {
		files = map[string][]byte{"coral": []byte("coral " + sp.Version + "\n")}
	}
	r := &Release{Version: sp.Version, Tag: tag, Package: sp.Package, Sequence: sp.Sequence}
	if r.Sequence == 0 {
		r.Sequence = 1
	}
	if r.Package == nil {
		r.Package = tarGz(files)
	}
	digest := sum(r.Package)
	var sums bytes.Buffer
	if !sp.NoAssetInSums {
		fmt.Fprintf(&sums, "%s  %s\n%s  %s\n", digest, Asset(sp.Version), digest, "coral-linux-amd64.tar.gz")
	}
	fmt.Fprintf(&sums, "%s  Coral.v%s.dmg\n", sum([]byte("a dmg")), sp.Version)
	sums.WriteString(sp.SumsExtra)
	r.Sums = sums.Bytes()

	seq := sp.Sequence
	if seq == 0 {
		seq = 1
	}
	repo := sp.ManifestRepo
	if repo == "" {
		repo = FakeRepo
	}
	sha := sp.SourceSHA
	if sha == "" {
		sha = FakeCommit
	}
	promoted, expires := sp.PromotedAt, sp.ExpiresAt
	if promoted.IsZero() {
		promoted = now.Add(-time.Minute)
	}
	if expires.IsZero() {
		expires = now.Add(30 * 24 * time.Hour)
	}
	revoked := sp.Revoked
	if revoked == nil {
		revoked = []string{}
	}
	m := map[string]any{
		"schema": 1, "repo": repo, "tag": tag, "version": sp.Version, "prerelease": sp.Prerelease, "source_sha": sha,
		"sha256sums_sha256": sum(r.Sums), "sequence": seq, "promoted_at": promoted.UTC().Format(time.RFC3339), "expires_at": expires.UTC().Format(time.RFC3339),
		"revoked": revoked,
	}
	if sp.ExtraManifestField {
		m["approved_by"] = "someone"
	}
	var err error
	if r.Manifest, err = json.Marshal(m); err != nil {
		return nil, err
	}

	ps, rs := sp.PromoteSigner, sp.ReleaseSigner
	if ps == nil {
		ps = s
	}
	if rs == nil {
		rs = s
	}
	pid := PromoteCert()
	if sp.PromoteID != nil {
		pid = *sp.PromoteID
	}
	rtag := tag
	if sp.ReleaseTag != "" {
		rtag = sp.ReleaseTag
	}
	rid := ReleaseCert(rtag)
	if sp.ReleaseID != nil {
		rid = *sp.ReleaseID
	}
	if r.ManifestBundle, err = ps.Bundle(r.Manifest, pid, sp.Omit); err != nil {
		return nil, err
	}
	if r.SumsBundle, err = rs.Bundle(r.Sums, rid, sp.Omit); err != nil {
		return nil, err
	}
	return r, nil
}
