package testrig

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/updater/stage"
	"github.com/subgentic/coral/internal/updater/verify"
)

// Every way the chain's files can be wrong, each made on a release that is otherwise good: the check must end in "failed" at the stage that owns the
// mistake, name no other, and leave nothing staged. A stage named here is the one the design gives that mistake; a different stage, or a stage of success,
// is a failure of the verifier, not of the case.
func TestEveryTamperedReleaseIsRefusedAtItsOwnStageAndStagesNothing(t *testing.T) {
	other, err := NewSigner() // a Sigstore the updater does not trust
	require.NoError(t, err)
	releaseAsPromote := ReleaseCert("v1.4.131")
	promoteAsRelease := PromoteCert()
	wrongPromoteRepo := PromoteCert() // the promote workflow of another repository (a name that is not the pinned one)
	wrongPromoteRepo.SAN = strings.Replace(wrongPromoteRepo.SAN, FakeRepo, FakeRepo+"-other", 1)
	wrongPromoteRepo.SourceRepositoryURI = strings.Replace(wrongPromoteRepo.SourceRepositoryURI, FakeRepo, FakeRepo+"-other", 1)
	now := time.Now()
	// The same-repository cases (#6274): the promote workflow and the release workflow are one repository, told apart only by the SAN's path and ref and by the trigger. Each of these is the right repository
	// with one thing wrong, and none may be accepted as a promote manifest (or, for the last two, as the release's sums).
	promoteAt := func(ref string) CertID { // promote.yml at another ref: the stub on main, another branch, a tag
		c := PromoteCert()
		c.SAN = strings.Replace(c.SAN, FakePromoteRef, ref, 1)
		c.SourceRepositoryRef = ref
		return c
	}
	promoteMain, promoteFeature, promoteTag := promoteAt("refs/heads/main"), promoteAt("refs/heads/feature"), promoteAt("refs/tags/v1.4.131")
	promotePush, promoteSchedule := PromoteCert(), PromoteCert()
	promotePush.BuildTrigger, promoteSchedule.BuildTrigger = "push", "schedule"
	releaseDispatched := ReleaseCert("v1.4.131")
	releaseDispatched.BuildTrigger = "workflow_dispatch"
	promoteFork, releaseFork := PromoteCert(), ReleaseCert("v1.4.131")
	promoteFork.SourceRepositoryID, releaseFork.SourceRepositoryID = "9000999", "9000999" // a fork or a re-created repository: every name right, another id

	cases := []struct {
		name  string
		spec  Spec
		mut   func(path string, b []byte) []byte
		stage string
	}{
		{name: "a bit flipped in the package", mut: func(p string, b []byte) []byte {
			if strings.HasSuffix(p, ".tar.gz") {
				b[len(b)/2] ^= 1
			}
			return b
		}, stage: verify.StagePackage},
		{name: "the package cut short", mut: func(p string, b []byte) []byte {
			if strings.HasSuffix(p, ".tar.gz") {
				return b[:len(b)-3]
			}
			return b
		}, stage: verify.StagePackage},
		{name: "a package with a byte added", mut: func(p string, b []byte) []byte {
			if strings.HasSuffix(p, ".tar.gz") {
				return append(b, 0)
			}
			return b
		}, stage: verify.StagePackage},
		{name: "another release's sums file under this manifest", mut: func(p string, b []byte) []byte {
			if strings.HasSuffix(p, stage.SumsFile) {
				return append(b, "00  extra\n"...)
			}
			return b
		}, stage: verify.StageSumsDigest},
		{name: "the manifest signed as the release workflow", spec: Spec{PromoteID: &releaseAsPromote}, stage: verify.StageManifestSignature},
		{name: "the manifest signed by another repository's promote workflow", spec: Spec{PromoteID: &wrongPromoteRepo}, stage: verify.StageManifestSignature},
		{name: "the manifest from promote.yml at main (the stub's identity)", spec: Spec{PromoteID: &promoteMain}, stage: verify.StageManifestSignature},
		{name: "the manifest from promote.yml at another branch", spec: Spec{PromoteID: &promoteFeature}, stage: verify.StageManifestSignature},
		{name: "the manifest from promote.yml at a tag", spec: Spec{PromoteID: &promoteTag}, stage: verify.StageManifestSignature},
		{name: "the manifest from promote.yml run by a push", spec: Spec{PromoteID: &promotePush}, stage: verify.StageManifestSignature},
		{name: "the manifest from promote.yml run on a schedule", spec: Spec{PromoteID: &promoteSchedule}, stage: verify.StageManifestSignature},
		{name: "the manifest from a fork or a re-created repository (another id, every name right)", spec: Spec{PromoteID: &promoteFork}, stage: verify.StageManifestSignature},
		{name: "the sums from release.yml run by a dispatch", spec: Spec{ReleaseID: &releaseDispatched}, stage: verify.StageSumsSignature},
		{name: "the sums from a fork or a re-created repository (another id, every name right)", spec: Spec{ReleaseID: &releaseFork}, stage: verify.StageSumsSignature},
		{name: "the manifest signed under a Sigstore this updater does not trust", spec: Spec{PromoteSigner: other}, stage: verify.StageManifestSignature},
		{name: "the sums signed as the promote workflow", spec: Spec{ReleaseID: &promoteAsRelease}, stage: verify.StageSumsSignature},
		{name: "the sums signed for another tag", spec: Spec{ReleaseTag: "v1.4.99"}, stage: verify.StageSumsSignature},
		{name: "the sums signed under a Sigstore this updater does not trust", spec: Spec{ReleaseSigner: other}, stage: verify.StageSumsSignature},
		{name: "the release built from another commit than the manifest approves", spec: Spec{SourceSHA: strings.Repeat("ab", 20)}, stage: verify.StageSumsCommit},
		{name: "a manifest for another repository", spec: Spec{ManifestRepo: "other.invalid/coral"}, stage: verify.StageManifestRules},
		{name: "a prerelease", spec: Spec{Prerelease: true}, stage: verify.StageManifestRules},
		{name: "an expired manifest", spec: Spec{PromotedAt: now.Add(-48 * time.Hour), ExpiresAt: now.Add(-time.Hour)}, stage: verify.StageManifestRules},
		{name: "a manifest promoted in the future", spec: Spec{PromotedAt: now.Add(2 * time.Hour), ExpiresAt: now.Add(48 * time.Hour)}, stage: verify.StageManifestRules},
		{name: "a manifest that approves forever", spec: Spec{PromotedAt: now.Add(-time.Minute), ExpiresAt: now.Add(400 * 24 * time.Hour)}, stage: verify.StageManifestRules},
		{name: "a version that revokes itself", spec: Spec{Revoked: []string{"1.4.131"}}, stage: verify.StageManifestRules},
		{name: "a manifest with a field the schema does not have", spec: Spec{ExtraManifestField: true}, stage: verify.StageManifestDocument},
		{name: "the package missing from the sums file", spec: Spec{NoAssetInSums: true}, stage: verify.StageAssetMissing},
		{name: "a stable alias that is other bytes than the versioned file", spec: Spec{SumsExtra: strings.Repeat("cd", 32) + "  coral-linux-arm64.tar.gz\n" + strings.Repeat("ef", 32) + "  coral-linux-arm64-1.4.131.tar.gz\n"}, stage: verify.StageSumsFormat},
		{name: "a sums line that is not a sums line", spec: Spec{SumsExtra: "not a line\n"}, stage: verify.StageSumsFormat},
		{name: "a sums entry with a path in its name", spec: Spec{SumsExtra: strings.Repeat("cd", 32) + "  ../escape\n"}, stage: verify.StageSumsFormat},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			c.spec.Version = "1.4.131"
			c.spec.Now = now
			r.publish(c.spec)
			r.gh.Mutate = c.mut
			res := r.check()
			assert.Equal(t, stage.ResultFailed, res.Result, "%+v", res)
			assert.Equal(t, c.stage, res.FailedStage, "%+v", res)
			assert.NotEmpty(t, res.NextAttempt, "a refusal backs off")
			r.nothingStaged()
			// and the next check, at once, is held off, not retried
			assert.Equal(t, stage.ResultBackoff, r.check().Result, "a failed release is not retried at once")
		})
	}
}

// A replayed promotion: an old manifest (a lower sequence) for a newer version is refused once a higher sequence has been accepted; so is a version that
// an earlier manifest revoked, and a downgrade is never offered.
func TestReplayedRevokedAndDowngradedPromotionsAreRefused(t *testing.T) {
	r := newRig(t)
	r.publish(Spec{Version: "1.4.131", Sequence: 5, Revoked: []string{"1.4.133"}})
	require.Equal(t, stage.ResultStaged, r.check().Result)
	// the sequence floor moves when a release is installed and healthy (State.Accept), never at staging (a rollback must not leave it ahead of what runs):
	// so this is what the apply does once 1.4.131 runs
	staged, err := stage.LoadStaged(r.cfg.DataDir)
	require.NoError(t, err)
	dir := filepath.Join(r.cfg.DataDir, stage.UpdatesDir)
	st, err := verify.LoadState(dir)
	require.NoError(t, err)
	require.NoError(t, st.Accept(staged.Record))
	require.NoError(t, verify.SaveState(dir, st))

	r.cfg.Installed = "1.4.131"
	r.publish(Spec{Version: "1.4.132", Sequence: 3}) // an old promotion served again for a newer version
	res := r.check()
	assert.Equal(t, stage.ResultUpToDate, res.Result, "%+v: the highest sequence listed (5, for 1.4.131) outranks the replay (3), and that is the installed one", res)
	r.gh.ListBody = []byte(`[{"tag_name":"` + PromotionTag("v1.4.132", 3) + `","draft":false,"prerelease":true,"assets":[{"name":"promote.json"},{"name":"promote.json.sigstore.json"}]}]`)
	res = r.check()
	assert.Equal(t, stage.ResultFailed, res.Result, "%+v", res)
	assert.Equal(t, verify.StageManifestRules, res.FailedStage, "a lower sequence than the last accepted, alone, is a replay and is refused")

	r2 := newRig(t)
	r2.publish(Spec{Version: "1.4.131", Sequence: 5, Revoked: []string{"1.4.133"}})
	require.Equal(t, stage.ResultStaged, r2.check().Result)
	r2.cfg.Installed = "1.4.131"
	r2.publish(Spec{Version: "1.4.133", Sequence: 6})
	res = r2.check()
	assert.Equal(t, stage.ResultFailed, res.Result, "%+v", res)
	assert.Equal(t, verify.StageManifestRules, res.FailedStage, "a version an earlier manifest revoked")

	r3 := newRig(t)
	r3.cfg.Installed = "1.4.140"
	r3.publish(Spec{Version: "1.4.131"})
	assert.Equal(t, stage.ResultUpToDate, r3.check().Result, "an older release is never offered")
	r3.nothingStaged()
}

// The network's own ways to be wrong: a redirect to a host that is not allowed, to another scheme, too many redirects, a server error, a body larger than the
// limit, and a package bigger than the updater will take.
func TestTheNetworksWaysToBeWrongAreRefusedAndStageNothing(t *testing.T) {
	cases := []struct {
		name string
		set  func(r *rig, rel *Release)
		want string
		fail string
	}{
		{"the manifest redirects to a host that is not allowed", func(r *rig, rel *Release) {
			r.gh.RedirectTo[PromotionTag("v1.4.131", 1)+"/"+stage.ManifestFile] = "http://localhost:" + portOf(r.gh.URL()) + "/" + FakeRepo + "/releases/download/v1.4.131/" + stage.ManifestFile
		}, stage.ResultFailed, "manifest"},
		{"the manifest redirects to another scheme", func(r *rig, rel *Release) {
			r.gh.RedirectTo[PromotionTag("v1.4.131", 1)+"/"+stage.ManifestFile] = "https://127.0.0.1:1/x"
		}, stage.ResultFailed, "manifest"},
		{"the manifest answers with a server error", func(r *rig, rel *Release) { r.gh.Status[PromotionTag("v1.4.131", 1)+"/"+stage.ManifestFile] = 503 }, stage.ResultFailed, "manifest"},
		{"the list of releases answers with a server error", func(r *rig, rel *Release) { r.gh.Status["list"] = 503 }, stage.ResultFailed, "manifest"},
		{"the list of releases is larger than the limit", func(r *rig, rel *Release) { r.gh.ListBody = bytes.Repeat([]byte(" "), 1<<20+10) }, stage.ResultFailed, "manifest"},
		{"the list of releases is not a list", func(r *rig, rel *Release) { r.gh.ListBody = []byte(`{"message":"API rate limit exceeded"}`) }, stage.ResultFailed, "manifest"},
		{"a bundle answers with a server error", func(r *rig, rel *Release) { r.gh.Status["v1.4.131/"+stage.SumsBundleFile] = 500 }, stage.ResultFailed, stage.SumsBundleFile},
		{"the sums bundle is larger than the limit", func(r *rig, rel *Release) {
			r.gh.Mutate = func(p string, b []byte) []byte {
				if strings.HasSuffix(p, stage.SumsBundleFile) {
					return bytes.Repeat([]byte("x"), verify.MaxBundleBytes+10)
				}
				return b
			}
		}, stage.ResultFailed, stage.SumsBundleFile},
		{"the package is bigger than the updater takes", func(r *rig, rel *Release) { r.cfg.MaxPackage = int64(len(rel.Package)) - 1 }, stage.ResultFailed, "download"}, // (a limit hit while copying is reported as a download failure, not as the package stage)
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			rel := r.publish(Spec{Version: "1.4.131"})
			c.set(r, rel)
			res := r.check()
			assert.Equal(t, c.want, res.Result, "%+v", res)
			assert.Equal(t, c.fail, res.FailedStage, "%+v", res)
			r.nothingStaged()
		})
	}
}

func portOf(u string) string { return u[strings.LastIndex(u, ":")+1:] }
