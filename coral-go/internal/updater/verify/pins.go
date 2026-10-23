// Package verify decides whether a downloaded Coral release may be installed (#5231, #5245): a promote manifest signed by the promote workflow,
// then the release's SHA256SUMS.txt and its Sigstore bundle, then the package's hash. It is linked only into cmd/coral-update, never into coral
// (a test holds that). Nothing here fetches a decision from the network or from a setting: the identities it trusts are the constants below.
package verify

import "time"

// The identities and limits the client trusts, compiled in. They are changed only by a release that carries the change; a test pins them to the release workflow in this repository.
// The promote workflow lives in the same repository as the release workflow, on a protected branch of its own (PromoteRef): the two are told apart by the workflow path and ref in the
// certificate's SAN and by the trigger (a pushed tag against a dispatch), never by the repository.
const (
	// Repo is the repository whose releases are installed: the organization's, since the repository moved from cdknorow/coral (#5299). New homes only: a release must come from
	// the home that is pinned here, and the versions an updater installs are always newer than the one running, so the releases cut in the old home are never candidates.
	Repo = "subgentic/coral"
	// RepoID is that repository's numeric GitHub id (certificate extension 1.3.6.1.4.1.57264.1.15, "source repository identifier"). It survives a transfer and is never reissued, so a
	// repository re-created under the old or the new NAME has a different id and fails (#5536): the name alone is only a label. Required: with none, nothing verifies.
	RepoID = "1160696364"
	// Issuer is the OIDC issuer of every signing certificate: GitHub Actions, exactly.
	Issuer = "https://token.actions.githubusercontent.com"
	// RunnerEnvironment: both workflows run on GitHub-hosted runners.
	RunnerEnvironment = "github-hosted"
	// ReleaseWorkflow is the path of the release workflow in Repo; its SAN is built for one exact tag.
	ReleaseWorkflow = ".github/workflows/release.yml"
	// ReleaseTrigger is how release.yml runs (a pushed tag).
	ReleaseTrigger = "push"
	// PromoteTrigger is how the promote workflow runs (by hand, for one tag).
	PromoteTrigger = "workflow_dispatch"
)

// Pins is what a verification trusts. DefaultPins is the compiled-in set; a test may build another.
type Pins struct {
	Repo string
	// RepoID is the id the release certificate must carry for Repo (see RepoID above); empty fails closed.
	RepoID string
	Issuer string
	// PromoteRepo, PromoteWorkflow and PromoteRef name the promote workflow: its certificate SAN must be exactly
	// https://github.com/<PromoteRepo>/<PromoteWorkflow>@<PromoteRef>, and the certificate's source repository and ref are checked too.
	PromoteRepo string
	// PromoteRepoID is the promote repository's id, with the same meaning and the same rule: empty fails closed.
	PromoteRepoID   string
	PromoteWorkflow string
	PromoteRef      string
	// MaxManifestLifetime bounds expires_at - promoted_at, so a manifest cannot approve forever; MaxSkew is the clock skew allowed on promoted_at.
	MaxManifestLifetime time.Duration
	MaxSkew             time.Duration
	// MacTeamIDs are the Apple Developer ID team identifiers a macOS update may be signed by (a set, so a rotation is approved under the old pin
	// and takes effect in the release after it). With none, the macOS apply installs nothing.
	MacTeamIDs []string
}

// DefaultPins is the compiled-in set. The promote workflow is .github/workflows/promote.yml at refs/heads/promote of Repo itself (#6274): a branch nobody works on, covered by its own
// ruleset, so the file a certificate names is the reviewed one; its certificate carries Repo's id, and a release certificate (release.yml, a tag, a push) is told apart from it by path, ref and trigger.
func DefaultPins() Pins {
	return Pins{
		Repo:                Repo,
		RepoID:              RepoID,
		Issuer:              Issuer,
		PromoteRepo:         Repo,
		PromoteRepoID:       RepoID, // the same repository: a renamed or recreated one does not verify
		PromoteWorkflow:     ".github/workflows/promote.yml",
		PromoteRef:          "refs/heads/promote",
		MaxManifestLifetime: 60 * 24 * time.Hour,
		MaxSkew:             5 * time.Minute,
		MacTeamIDs:          []string{"33UR27T84L"}, // the Developer ID team that signs Coral.app (the certificate of 2026-10-21, used since v1.4.143)
	}
}

// Size caps for what is fetched (section 5): the manifest and its bundle, the sums file and its bundle.
const (
	MaxManifestBytes = 64 << 10
	MaxBundleBytes   = 256 << 10
	MaxSumsBytes     = 64 << 10
)
