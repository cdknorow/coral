package verify

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// Stage names where a verification stopped, for the log and for the table test that none of them reaches the installer.
const (
	StageManifestSignature = "manifest_signature"
	StageManifestDocument  = "manifest_document"
	StageManifestRules     = "manifest_rules"
	StageSumsDigest        = "sums_digest"
	StageSumsSignature     = "sums_signature"
	StageSumsCommit        = "sums_commit"
	StageSumsFormat        = "sums_format"
	StageAssetMissing      = "asset_missing"
	StagePackage           = "package"
)

// Failure is a refused verification: the stage, the reason, and for a digest mismatch the two digests (section 9: always logged).
type Failure struct {
	Stage  string
	Reason string
}

func (f *Failure) Error() string { return "update not verified at " + f.Stage + ": " + f.Reason }

func fail(stage, format string, args ...any) error {
	return &Failure{Stage: stage, Reason: fmt.Sprintf(format, args...)}
}

// State is what the client remembers between checks (state.go): the last accepted version and sequence, and every version ever revoked.
type State struct {
	Version  string   `json:"version"`
	Sequence int64    `json:"sequence"`
	Revoked  []string `json:"revoked"`
	// LastFailure is the last refusal: Stage, Reason and When (RFC 3339), for Settings > Updates.
	LastFailure *FailureRecord `json:"last_failure,omitempty"`
	LastCheck   string         `json:"last_check,omitempty"`
	// HubRelaunched is the staged version a relaunch from Coral Hub was started for (one per version, #5245 section 13).
	HubRelaunched string         `json:"hub_relaunched,omitempty"`
	History       []HistoryEntry `json:"history,omitempty"`
	// Stage is what the staging step remembers between checks: the version it staged and its back-off (#5292).
	Stage *StageState `json:"stage,omitempty"`
}

// StageState is the staging step's own record. NextAttempt is an RFC 3339 time before which no download is tried again for Version; Attempts
// counts the failures in a row for it; ErrorKind is "network" or "verify".
type StageState struct {
	Staged      string `json:"staged,omitempty"`
	StagedAt    string `json:"staged_at,omitempty"`
	Version     string `json:"version,omitempty"`
	Attempts    int    `json:"attempts,omitempty"`
	NextAttempt string `json:"next_attempt,omitempty"`
	ErrorKind   string `json:"error_kind,omitempty"`
}

// FailureRecord is one refusal as it is kept.
type FailureRecord struct {
	Version string `json:"version"`
	Stage   string `json:"stage"`
	Reason  string `json:"reason"`
	At      string `json:"at"`
}

// HistoryEntry is one relaunch attempt, local or from the hub, as audited on the machine.
type HistoryEntry struct {
	At     string   `json:"at"`
	From   string   `json:"from"`
	To     string   `json:"to"`
	By     string   `json:"by"`
	Agents []string `json:"agents,omitempty"`
	Result string   `json:"result"`
	// Reason is the stage a failed or rolled-back attempt stopped at (a fixed word of the apply, never text from outside): coral-update status says it.
	Reason string `json:"reason,omitempty"`
}

// Candidate is what was fetched for a release: nothing in it is trusted until it has been verified.
type Candidate struct {
	Manifest, ManifestBundle []byte
	Sums, SumsBundle         []byte
}

// Record is what a verification found: the staged package's expected hash and the digests that led to it, kept next to the staged file. It is a
// pointer to what to check, not proof: the installer verifies again from the files (Verify again, then CheckPackage).
type Record struct {
	Version        string `json:"version"`
	Tag            string `json:"tag"`
	Sequence       int64  `json:"sequence"`
	PromotedAt     string `json:"promoted_at"`
	ExpiresAt      string `json:"expires_at"`
	SourceSHA      string `json:"source_sha"`
	Asset          string `json:"asset"`
	PackageSHA256  string `json:"package_sha256"`
	SumsSHA256     string `json:"sums_sha256"`
	ManifestSHA256 string `json:"manifest_sha256"`
	VerifiedAt     string `json:"verified_at"`
	// Revoked is the manifest's list, for the state to union once the version is accepted.
	Revoked []string `json:"revoked"`
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func releaseSAN(repo, tag string) string {
	return "https://github.com/" + repo + "/" + ReleaseWorkflow + "@refs/tags/" + tag
}

// Release verifies a fetched release in the order of the design (#5245 section 5) and returns its record, or a *Failure naming the stage. asset
// gives the platform's package name for the version in the manifest; installed is the running version; now is the clock (a parameter: tests).
//
// The order is the point: the manifest and its signature first; the manifest's rules; the sums file only once its sha256 equals the manifest's;
// then its bundle against the release signer for that exact tag, with the certificate's commit equal to the manifest's source_sha.
func Release(v BundleVerifier, pins Pins, c Candidate, asset func(version string) string, st State, installed string, now time.Time) (*Record, error) {
	return verifyRelease(v, pins, c, asset, now, func(m *Manifest) error { return CheckManifest(m, pins, st, installed, now) })
}

// ReleaseOf verifies the release of exactly version, for the Windows rollback (#162 (b)): the previous version's package, to put back when a
// new one didn't come up. It is the same chain as Release, the manifest's signature, the sums file it names and its signature for that exact
// tag and commit, and the package's entry, with the rules of a re-install instead of an update's (CheckManifestOf): the version is exactly
// the one asked for, and it is not revoked; being newer than the installed version, a sequence above the last accepted and an unexpired
// manifest are an update's rules and don't apply to the version Coral is running now.
func ReleaseOf(v BundleVerifier, pins Pins, c Candidate, asset func(version string) string, version string, st State, now time.Time) (*Record, error) {
	return verifyRelease(v, pins, c, asset, now, func(m *Manifest) error { return CheckManifestOf(m, pins, st, version, now) })
}

// verifyRelease is Release's chain, with the manifest's rules given.
func verifyRelease(v BundleVerifier, pins Pins, c Candidate, asset func(version string) string, now time.Time, rules func(*Manifest) error) (*Record, error) {
	// 2. The manifest's signature, against the promote workflow's identity.
	if len(c.Manifest) == 0 || len(c.Manifest) > MaxManifestBytes {
		return nil, fail(StageManifestDocument, "manifest size %d is outside 1..%d", len(c.Manifest), MaxManifestBytes)
	}
	if _, err := v.VerifyBundle(c.ManifestBundle, c.Manifest, promoteIdentity(pins)); err != nil {
		return nil, fail(StageManifestSignature, "%v", err)
	}
	// 3. The document and its rules.
	m, err := ParseManifest(c.Manifest)
	if err != nil {
		return nil, fail(StageManifestDocument, "%v", err)
	}
	if err := rules(m); err != nil {
		return nil, err
	}
	// 4. The release: its sums file is the one the manifest names, before anything in it is read.
	if len(c.Sums) == 0 || len(c.Sums) > MaxSumsBytes {
		return nil, fail(StageSumsFormat, "sums size %d is outside 1..%d", len(c.Sums), MaxSumsBytes)
	}
	if got := sha256Hex(c.Sums); got != m.SHA256SumsSHA {
		return nil, fail(StageSumsDigest, "SHA256SUMS.txt is %s, the manifest says %s", got, m.SHA256SumsSHA)
	}
	cert, err := v.VerifyBundle(c.SumsBundle, c.Sums, releaseIdentity(pins, m.Tag))
	if err != nil {
		return nil, fail(StageSumsSignature, "%v", err)
	}
	if cert.SourceRepositoryDigest != m.SourceSHA {
		return nil, fail(StageSumsCommit, "the release was built from %q, the manifest approves %q", cert.SourceRepositoryDigest, m.SourceSHA)
	}
	// 5. The package's hash, from the verified sums file only.
	entries, err := ParseSums(c.Sums)
	if err != nil {
		return nil, fail(StageSumsFormat, "%v", err)
	}
	if err := CheckStableAliases(entries, m.Version); err != nil {
		return nil, fail(StageSumsFormat, "%v", err)
	}
	name := asset(m.Version)
	want, ok := entries[name]
	if !ok {
		return nil, fail(StageAssetMissing, "%q is not in SHA256SUMS.txt", name)
	}
	return &Record{
		Version: m.Version, Tag: m.Tag, Sequence: m.Sequence, PromotedAt: m.PromotedAt, ExpiresAt: m.ExpiresAt, SourceSHA: m.SourceSHA,
		Asset: name, PackageSHA256: want, SumsSHA256: m.SHA256SumsSHA, ManifestSHA256: sha256Hex(c.Manifest),
		VerifiedAt: now.UTC().Format(time.RFC3339), Revoked: m.Revoked,
	}, nil
}

func promoteIdentity(p Pins) Identity {
	return Identity{
		SAN:                 "https://github.com/" + p.PromoteRepo + "/" + p.PromoteWorkflow + "@" + p.PromoteRef,
		Issuer:              p.Issuer,
		SourceRepositoryURI: "https://github.com/" + p.PromoteRepo,
		SourceRepositoryRef: p.PromoteRef,
		RunnerEnvironment:   RunnerEnvironment,
		BuildTrigger:        PromoteTrigger,
		SourceRepositoryID:  p.PromoteRepoID,
	}
}

func releaseIdentity(p Pins, tag string) Identity {
	return Identity{
		SAN:                 releaseSAN(p.Repo, tag),
		Issuer:              p.Issuer,
		SourceRepositoryURI: "https://github.com/" + p.Repo,
		SourceRepositoryRef: "refs/tags/" + tag,
		RunnerEnvironment:   RunnerEnvironment,
		BuildTrigger:        ReleaseTrigger,
		SourceRepositoryID:  p.RepoID,
	}
}

// CheckManifest applies the rules of section 5 step 3 and section 8 to a parsed, verified manifest: its repository, a release that is not a
// prerelease, strictly newer than the installed version, not lower in sequence than the last accepted, not from the future, not expired, within the
// longest lifetime, and not revoked (by this manifest or any earlier one).
func CheckManifest(m *Manifest, pins Pins, st State, installed string, now time.Time) error {
	if m.Repo != pins.Repo {
		return fail(StageManifestRules, "the manifest is for %q, not %q", m.Repo, pins.Repo)
	}
	if m.Prerelease {
		return fail(StageManifestRules, "version %s is a prerelease", m.Version)
	}
	cmp, err := CompareVersions(m.Version, installed)
	if err != nil {
		return fail(StageManifestRules, "installed version: %v", err)
	}
	if cmp <= 0 {
		return fail(StageManifestRules, "version %s is not newer than the installed %s", m.Version, installed)
	}
	if m.Sequence < st.Sequence {
		return fail(StageManifestRules, "sequence %d is lower than the last accepted %d (a replayed promotion)", m.Sequence, st.Sequence)
	}
	if m.promoted.After(now.Add(pins.MaxSkew)) {
		return fail(StageManifestRules, "promoted_at %s is in the future", m.PromotedAt)
	}
	if !now.Before(m.until) {
		return fail(StageManifestRules, "the manifest expired at %s", m.ExpiresAt)
	}
	if m.until.Sub(m.promoted) > pins.MaxManifestLifetime {
		return fail(StageManifestRules, "the manifest's lifetime %s is longer than the %s allowed", m.until.Sub(m.promoted), pins.MaxManifestLifetime)
	}
	for _, r := range append(append([]string{}, m.Revoked...), st.Revoked...) {
		if r == m.Version {
			return fail(StageManifestRules, "version %s is revoked", m.Version)
		}
	}
	return nil
}

// CheckManifestOf applies the rules of a re-install of exactly version (ReleaseOf): its repository, not a prerelease, exactly that version and
// its tag "v"+version, not from the future, within the longest lifetime, and not revoked (by this manifest or any earlier one). A revoked
// version is never put back.
func CheckManifestOf(m *Manifest, pins Pins, st State, version string, now time.Time) error {
	if m.Repo != pins.Repo {
		return fail(StageManifestRules, "the manifest is for %q, not %q", m.Repo, pins.Repo)
	}
	if m.Prerelease {
		return fail(StageManifestRules, "version %s is a prerelease", m.Version)
	}
	if m.Version != version || m.Tag != "v"+version {
		return fail(StageManifestRules, "the manifest is for %s (%s), not %s", m.Version, m.Tag, version)
	}
	if m.promoted.After(now.Add(pins.MaxSkew)) {
		return fail(StageManifestRules, "promoted_at %s is in the future", m.PromotedAt)
	}
	if m.until.Sub(m.promoted) > pins.MaxManifestLifetime {
		return fail(StageManifestRules, "the manifest's lifetime %s is longer than the %s allowed", m.until.Sub(m.promoted), pins.MaxManifestLifetime)
	}
	for _, r := range append(append([]string{}, m.Revoked...), st.Revoked...) {
		if r == m.Version {
			return fail(StageManifestRules, "version %s is revoked", m.Version)
		}
	}
	return nil
}

// ParseSums reads SHA256SUMS.txt: lines of "<64 hex>  <name>" (sha256sum's format, a star before the name for binary mode accepted), a name once, no
// path separators.
func ParseSums(data []byte) (map[string]string, error) {
	out := map[string]string{}
	for i, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if len(line) < 67 || line[64] != ' ' || (line[65] != ' ' && line[65] != '*') {
			return nil, fmt.Errorf("line %d is not \"<sha256>  <name>\"", i+1)
		}
		sum, name := line[:64], line[66:]
		if !hex64.MatchString(sum) {
			return nil, fmt.Errorf("line %d: %q is not a lowercase sha256", i+1, sum)
		}
		if name == "" || strings.ContainsAny(name, "/\\\x00") || name == "." || name == ".." {
			return nil, fmt.Errorf("line %d: %q is not a file name", i+1, name)
		}
		// No control character: a name such as "a\r" would read as "a" once written out again (the line's CR is dropped), and nothing the release
		// publishes has one (#5372, found by FuzzParseSums).
		for _, r := range name {
			if r < 0x20 || r == 0x7f {
				return nil, fmt.Errorf("line %d: the file name holds a control character", i+1)
			}
		}
		if _, dup := out[name]; dup {
			return nil, fmt.Errorf("line %d: %q appears twice", i+1, name)
		}
		out[name] = sum
	}
	if len(out) == 0 {
		return nil, errors.New("no entries")
	}
	return out, nil
}

// CheckPackage hashes r (at most max bytes) and compares it with the record's package hash: at staging, as the file is written, and again at
// install, since the staging folder is user-writable. It returns the number of bytes read.
func (r *Record) CheckPackage(src io.Reader, max int64) (int64, error) {
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(src, max+1))
	if err != nil {
		return n, fail(StagePackage, "reading the package: %v", err)
	}
	if n > max {
		return n, fail(StagePackage, "the package is larger than %d bytes", max)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != r.PackageSHA256 {
		return n, fail(StagePackage, "%s is %s, the verified sums file says %s", r.Asset, got, r.PackageSHA256)
	}
	return n, nil
}

// StableAliases are the release's stable-name copies and the versioned files they copy (#5333): every release also publishes the same bytes without the version in the name
// (so /releases/latest/download/<name> never goes stale), and SHA256SUMS.txt lists both names. The updater installs only the versioned name; a stable name that listed another
// digest than its versioned file would be a release whose own checksum list contradicts itself.
func StableAliases(version string) map[string]string {
	return map[string]string{
		"coral-linux-amd64.tar.gz": "coral-linux-amd64-" + version + ".tar.gz",
		"coral-linux-arm64.tar.gz": "coral-linux-arm64-" + version + ".tar.gz",
		"Coral.dmg":                "Coral.v" + version + ".dmg",
		"Coral-x64.msi":            "Coral-" + version + "-x64.msi",
		"Coral-x64-portable.zip":   "Coral-" + version + "-x64-portable.zip",
	}
}

// CheckStableAliases requires, for every stable name present in the verified sums entries together with its versioned file, the same digest. Releases cut before the copies existed
// have none, and a pair with one side missing is not this check's business (the asset the updater wants is looked up by its versioned name).
func CheckStableAliases(entries map[string]string, version string) error {
	for stable, versioned := range StableAliases(version) {
		a, okA := entries[stable]
		b, okB := entries[versioned]
		if okA && okB && a != b {
			return fmt.Errorf("%s is listed as %s and %s as %s: a stable-name copy must be the same bytes", stable, a, versioned, b)
		}
	}
	return nil
}
