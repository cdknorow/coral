package testrig

// The promote workflow's own manifest builder (tools/devops/promote_manifest.py, run here as promote.yml runs it) makes manifests that this updater
// verifies (#326): each one signed by the rig's fake Sigstore under the promote identity of the same repository, then put through verify.Release
// and verify.ReleaseOf, following a client's state across promotions. The repository is subgentic/coral, as the script writes it.

import (
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

// runScript runs the builder as the workflow's build step does; prev is the previous promotion's manifest (nil for none).
func runScript(t *testing.T, py, mode string, r scriptRelease, prev []byte, revoke string, now time.Time) ([]byte, error) {
	prevPath := ""
	if prev != nil {
		prevPath = filepath.Join(t.TempDir(), "promote.json")
		require.NoError(t, os.WriteFile(prevPath, prev, 0o600))
	}
	env := []string{"MODE=" + mode, "TAG=" + r.tag, "SOURCE_SHA=" + r.sha, "SUMS_FILE=" + r.sumsPath, "REVOKE=" + revoke,
		"NOW=" + now.UTC().Format(time.RFC3339), "PREV_MANIFEST=" + prevPath}
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
