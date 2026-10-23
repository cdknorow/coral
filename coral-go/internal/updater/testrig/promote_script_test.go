package testrig

// The promote workflow's own manifest builder (tools/devops/promote_manifest.py, run here as promote.yml runs it) makes manifests that this updater
// verifies (#326): each one signed by the rig's fake Sigstore under the promote identity of the same repository, then put through verify.Release
// and verify.ReleaseOf, following a client's state across promotions. The repository is subgentic/coral, as the script writes it.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/updater/stage"
	"github.com/subgentic/coral/internal/updater/verify"
)

const scriptRepo = "subgentic/coral"

func python(t *testing.T) string {
	for _, p := range []string{"python3", "python"} {
		if path, err := exec.LookPath(p); err == nil {
			if exec.Command(path, "-c", "import sys; sys.exit(sys.version_info < (3, 8))").Run() == nil {
				return path
			}
		}
	}
	t.Skip("no Python 3")
	return ""
}

// scriptPins are the updater's pins for the one repository the script writes, with the rig's issuer and id.
func scriptPins() verify.Pins {
	p := Pins()
	p.Repo, p.PromoteRepo = scriptRepo, scriptRepo
	return p
}

func scriptPromoteCert() CertID {
	p := scriptPins()
	c := PromoteCert()
	c.SAN = "https://github.com/" + p.PromoteRepo + "/" + p.PromoteWorkflow + "@" + p.PromoteRef
	c.SourceRepositoryURI = "https://github.com/" + p.PromoteRepo
	return c
}

type scriptRelease struct {
	tag, sha, sumsPath, msi string
	sums, sumsBundle        []byte
}

func newScriptRelease(t *testing.T, s *Signer, version string) scriptRelease {
	h := sha256.Sum256([]byte("msi " + version))
	d := hex.EncodeToString(h[:])
	sums := []byte(fmt.Sprintf("%s  Coral-%s-x64.msi\n%s  Coral-x64.msi\n", d, version, d))
	sha := strings.Repeat("ab", 20)
	id := CertID{
		SAN: "https://github.com/" + scriptRepo + "/.github/workflows/release.yml@refs/tags/v" + version, Issuer: FakeIssuer,
		SourceRepositoryURI: "https://github.com/" + scriptRepo, SourceRepositoryRef: "refs/tags/v" + version, SourceRepositoryDigest: sha,
		RunnerEnvironment: "github-hosted", BuildTrigger: "push", SourceRepositoryID: FakeRepoID,
	}
	b, err := s.Bundle(sums, id, Omit{})
	require.NoError(t, err)
	p := filepath.Join(t.TempDir(), "SHA256SUMS.txt")
	require.NoError(t, os.WriteFile(p, sums, 0o600))
	return scriptRelease{tag: "v" + version, sha: sha, sumsPath: p, msi: d, sums: sums, sumsBundle: b}
}

// runScript runs the builder as the workflow's build step does; prev is the previous real promotion's manifest (nil for none), and the names
// taken are 1 to its sequence (no junk).
func runScript(t *testing.T, py, mode string, r scriptRelease, prev []byte, revoke string, now time.Time) ([]byte, error) {
	var taken []int64
	if prev != nil {
		m, err := verify.ParseManifest(prev)
		require.NoError(t, err)
		for n := int64(1); n <= m.Sequence; n++ {
			taken = append(taken, n)
		}
	}
	return runScriptTaken(t, py, mode, r, prev, taken, revoke, now)
}

// runScriptTaken is runScript with the promote-<n> names taken given (junk can sit among and above them).
func runScriptTaken(t *testing.T, py, mode string, r scriptRelease, prev []byte, taken []int64, revoke string, now time.Time) ([]byte, error) {
	prevPath := ""
	if prev != nil {
		prevPath = filepath.Join(t.TempDir(), "promote.json")
		require.NoError(t, os.WriteFile(prevPath, prev, 0o600))
	}
	env := []string{"MODE=" + mode, "TAG=" + r.tag, "SOURCE_SHA=" + r.sha, "SUMS_FILE=" + r.sumsPath, "REVOKE=" + revoke,
		"NOW=" + now.UTC().Format(time.RFC3339), "PREV_MANIFEST=" + prevPath, "TAKEN=" + joinInts(taken)}
	for _, k := range []string{"SYSTEMROOT", "SYSTEMDRIVE", "WINDIR", "PROGRAMDATA"} { // what Python on Windows needs to start, and nothing else
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "tools", "devops", "promote_manifest.py"))
	require.NoError(t, err)
	cmd := exec.Command(py, "-I", script)
	cmd.Env, cmd.Dir = env, t.TempDir()
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%v: %s", err, stderr.String())
	}
	return out, nil
}

func scriptCandidate(t *testing.T, s *Signer, manifest []byte, r scriptRelease) verify.Candidate {
	b, err := s.Bundle(manifest, scriptPromoteCert(), Omit{})
	require.NoError(t, err)
	return verify.Candidate{Manifest: manifest, ManifestBundle: b, Sums: r.sums, SumsBundle: r.sumsBundle}
}

func msiAsset(v string) string { return "Coral-" + v + "-x64.msi" }

func TestThePromoteScriptsManifestsVerifyUnderThisUpdater(t *testing.T) {
	py := python(t)
	s, err := NewSigner()
	require.NoError(t, err)
	v, pins := verify.Sigstore{Trust: s.Trust()}, scriptPins()
	now := time.Now().UTC().Truncate(time.Second)
	r157, r158 := newScriptRelease(t, s, "1.4.157"), newScriptRelease(t, s, "1.4.158")
	var st verify.State // a client on 1.4.150

	// The first promotion: sequence 1, verified and accepted.
	m1, err := runScript(t, py, "promote", r157, nil, "", now)
	require.NoError(t, err)
	rec, err := verify.Release(v, pins, scriptCandidate(t, s, m1, r157), msiAsset, st, "1.4.150", now)
	require.NoError(t, err, "the first promotion verifies: %s", m1)
	assert.Equal(t, r157.msi, rec.PackageSHA256)
	assert.Equal(t, int64(1), rec.Sequence)
	require.NoError(t, st.Accept(rec))

	// A refresh: the same tag, the next sequence, a new expiry.
	m2, err := runScript(t, py, "refresh", r157, m1, "", now.Add(20*24*time.Hour))
	require.NoError(t, err)
	_, err = verify.Release(v, pins, scriptCandidate(t, s, m2, r157), msiAsset, verify.State{}, "1.4.150", now.Add(20*24*time.Hour))
	require.NoError(t, err)
	m, _ := verify.ParseManifest(m2)
	assert.Equal(t, int64(2), m.Sequence, "no gap: the updater probes promote-<n> by name")

	// The next release, then its revoke: the earlier release again, with a higher sequence and the bad version revoked.
	m3, err := runScript(t, py, "promote", r158, m2, "", now.Add(21*24*time.Hour))
	require.NoError(t, err)
	_, err = verify.Release(v, pins, scriptCandidate(t, s, m3, r158), msiAsset, st, "1.4.157", now.Add(21*24*time.Hour))
	require.NoError(t, err)
	m4, err := runScript(t, py, "revoke", r157, m3, "1.4.158", now.Add(22*24*time.Hour))
	require.NoError(t, err)
	rec4, err := verify.Release(v, pins, scriptCandidate(t, s, m4, r157), msiAsset, verify.State{Sequence: 3}, "1.4.150", now.Add(22*24*time.Hour))
	require.NoError(t, err, "the revoke's manifest is accepted by a client that saw sequence 3")
	assert.Equal(t, []string{"1.4.158"}, rec4.Revoked)
	after := verify.State{Sequence: 4}
	after.Observe(rec4)
	_, err = verify.Release(v, pins, scriptCandidate(t, s, m3, r158), msiAsset, after, "1.4.157", now.Add(22*24*time.Hour))
	assert.ErrorContains(t, err, "lower than the last accepted", "the revoked release's promotion, replayed, is refused")

	// Backfill: an older version's manifest for the rollback lookup (ReleaseOf), taking no number from the promotions.
	r150 := newScriptRelease(t, s, "1.4.150")
	m5, err := runScript(t, py, "backfill", r150, m4, "", now.Add(22*24*time.Hour))
	require.NoError(t, err)
	m, _ = verify.ParseManifest(m5)
	assert.Equal(t, int64(4), m.Sequence, "a backfill keeps the current sequence")
	_, err = verify.ReleaseOf(v, pins, scriptCandidate(t, s, m5, r150), msiAsset, "1.4.150", verify.State{}, now.Add(40*24*time.Hour))
	require.NoError(t, err, "a backfilled manifest verifies as the rollback package's, expired or not")

	// Junk names among and above the real promotion: the new sequence is the smallest free name above the previous one, and still verifies.
	upTo := func(n int64, extra ...int64) []int64 {
		var out []int64
		for i := int64(1); i <= n; i++ {
			out = append(out, i)
		}
		return append(out, extra...)
	}
	for name, c := range map[string]struct {
		taken []int64
		want  int64
	}{
		"junk right above":          {upTo(44), 45},
		"junk two above, a gap":     {upTo(4, 6), 5},
		"junk far above":            {upTo(4, 1_000_004), 5},
		"junk above, then far away": {upTo(44, 1_000_004), 45},
	} {
		m6, err := runScriptTaken(t, py, "refresh", r157, m4, c.taken, "", now.Add(23*24*time.Hour))
		require.NoError(t, err, name)
		m, _ = verify.ParseManifest(m6)
		assert.Equal(t, c.want, m.Sequence, "%s: the smallest name above the previous promotion that isn't taken", name)
		_, err = verify.Release(v, pins, scriptCandidate(t, s, m6, r157), msiAsset, verify.State{Sequence: 4}, "1.4.150", now.Add(23*24*time.Hour))
		require.NoError(t, err, name)
	}
	_, err = runScriptTaken(t, py, "refresh", r157, m4, []int64{1, 2, 3}, "", now)
	assert.Error(t, err, "taken names without the previous promotion's own are refused")

	// What the script refuses.
	r159 := newScriptRelease(t, s, "1.4.159")
	for name, c := range map[string]struct {
		mode   string
		r      scriptRelease
		prev   []byte
		revoke string
	}{
		"an older promote":          {"promote", r157, m3, ""},
		"a refresh of another tag":  {"refresh", r158, m2, ""},
		"a revoke of another":       {"revoke", r157, m3, "1.4.100"},
		"a revoked version":         {"promote", r158, m4, ""},
		"revoke without a version":  {"revoke", r157, m3, ""},
		"a revoke list in promote":  {"promote", r159, m2, "1.4.100"},
		"a backfill that is newer":  {"backfill", r158, m2, ""},
		"a tag that is not a tag":   {"promote", scriptRelease{tag: "v1.4", sha: r159.sha, sumsPath: r159.sumsPath}, m2, ""},
		"a commit that isn't a sha": {"promote", scriptRelease{tag: r159.tag, sha: "main", sumsPath: r159.sumsPath}, m2, ""},
	} {
		_, err := runScript(t, py, c.mode, c.r, c.prev, c.revoke, now)
		assert.Error(t, err, name)
	}
}

func joinInts(ns []int64) string {
	var parts []string
	for _, n := range ns {
		parts = append(parts, fmt.Sprint(n))
	}
	return strings.Join(parts, " ")
}

// promoteThroughScript is one run of the promote workflow against the fake GitHub: a product release for version, its manifest numbered by the
// script from the names taken on GitHub, signed as promote.yml, published as promote-<sequence>. It returns the manifest.
func promoteThroughScript(t *testing.T, py string, s *Signer, gh *FakeGitHub, version string, prev []byte, now time.Time) []byte {
	t.Helper()
	pkg := []byte("the package of " + version)
	sums := []byte(sum(pkg) + "  " + Asset(version) + "\n")
	r := newScriptRelease(t, s, version)
	b, err := s.Bundle(sums, CertID{
		SAN: "https://github.com/" + scriptRepo + "/.github/workflows/release.yml@refs/tags/v" + version, Issuer: FakeIssuer,
		SourceRepositoryURI: "https://github.com/" + scriptRepo, SourceRepositoryRef: "refs/tags/v" + version, SourceRepositoryDigest: r.sha,
		RunnerEnvironment: "github-hosted", BuildTrigger: "push", SourceRepositoryID: FakeRepoID,
	}, Omit{})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(r.sumsPath, sums, 0o600))
	manifest, err := runScriptTaken(t, py, "promote", r, prev, gh.Taken(), "", now)
	require.NoError(t, err)
	mb, err := s.Bundle(manifest, scriptPromoteCert(), Omit{})
	require.NoError(t, err)
	m, err := verify.ParseManifest(manifest)
	require.NoError(t, err)
	gh.Publish(&Release{Version: version, Tag: "v" + version, Sequence: m.Sequence, Package: pkg, Sums: sums, SumsBundle: b, Manifest: manifest, ManifestBundle: mb}, true)
	return manifest
}

// Through the script's own numbering (Security on #143's delta): after junk is published above the real promotion N, the workflow's next two
// promotions are each found by a client that has seen N. The case the old rule (the highest tag + 1) broke is junk at N+2 alone: the next
// promotion took N+3, and a client probing up from N stopped at the missing N+1.
func TestTheWorkflowsNextPromotionsAreFoundPastJunk(t *testing.T) {
	py := python(t)
	junk := []byte(`{"schema":1}`)
	for name, c := range map[string]struct {
		junk []int64 // above N = 1
		want []int64 // the sequences of the next two promotions
	}{
		"junk at N+2 only":             {[]int64{3}, []int64{2, 4}},
		"junk at N+10^6 only":          {[]int64{1_000_001}, []int64{2, 3}},
		"junk at N+1..N+40 and N+10^6": {append(seqs(2, 41), 1_000_001), []int64{42, 43}},
	} {
		t.Run(name, func(t *testing.T) {
			s, err := NewSigner()
			require.NoError(t, err)
			gh := NewFakeGitHub()
			defer gh.Close()
			cfg := stage.Config{DataDir: t.TempDir(), Installed: installed, Pins: scriptPins(), Verifier: verify.Sigstore{Trust: s.Trust()}, Asset: Asset}
			gh.Config(&cfg)
			check := func() stage.Result {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				return stage.Check(ctx, cfg)
			}
			now := time.Now().UTC().Truncate(time.Second)
			prev := promoteThroughScript(t, py, s, gh, "1.4.131", nil, now)
			res := check()
			require.Equal(t, stage.ResultStaged, res.Result, "%+v", res) // the client has seen N = 1
			for _, n := range c.junk {
				gh.PublishPromotion(n, junk, nil)
			}
			for i, v := range []string{"1.4.132", "1.4.133"} {
				prev = promoteThroughScript(t, py, s, gh, v, prev, now)
				m, _ := verify.ParseManifest(prev)
				assert.Equal(t, c.want[i], m.Sequence, "the script's number for %s", v)
				res = check()
				require.Equal(t, stage.ResultStaged, res.Result, "%s: %+v", v, res)
				assert.Equal(t, v, res.Version)
			}
		})
	}
}

func seqs(from, to int64) []int64 {
	var out []int64
	for n := from; n <= to; n++ {
		out = append(out, n)
	}
	return out
}
