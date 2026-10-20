// Package verify decides whether a downloaded Coral release may be installed (#5231, #5245): a promote manifest signed by the promote workflow,
// then the release's SHA256SUMS.txt and its Sigstore bundle, then the package's hash. It is linked only into cmd/coral-update, never into coral
// (a test holds that). Nothing here fetches a decision from the network or from a setting: the identities it trusts are the constants below.
package verify

import "time"

// The identities and limits the client trusts, compiled in. They are changed only by a release that carries the change, promoted while the old
// pins still hold (design note section 12, invariant 3); a test pins them to the release workflow in this repository.
const (
	// Repo is the repository whose releases are installed.
	Repo = "cdknorow/coral"
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
	Repo   string
	Issuer string
	// PromoteRepo, PromoteWorkflow and PromoteRef name the promote workflow: its certificate SAN must be exactly
	// https://github.com/<PromoteRepo>/<PromoteWorkflow>@<PromoteRef>, and the certificate's source repository and ref are checked too.
	PromoteRepo     string
	PromoteWorkflow string
	PromoteRef      string
	// MaxManifestLifetime bounds expires_at - promoted_at, so a manifest cannot approve forever; MaxSkew is the clock skew allowed on promoted_at.
	MaxManifestLifetime time.Duration
	MaxSkew             time.Duration
}

// DefaultPins is the compiled-in set. The promote workflow's place is decided with the approval model (note section 3: a second locked-down
// repository, option A, is assumed here); until that workflow exists no manifest verifies, so nothing installs.
func DefaultPins() Pins {
	return Pins{
		Repo:                Repo,
		Issuer:              Issuer,
		PromoteRepo:         "cdknorow/coral-promote",
		PromoteWorkflow:     ".github/workflows/promote.yml",
		PromoteRef:          "refs/heads/main",
		MaxManifestLifetime: 60 * 24 * time.Hour,
		MaxSkew:             5 * time.Minute,
	}
}

// Size caps for what is fetched (section 5): the manifest and its bundle, the sums file and its bundle.
const (
	MaxManifestBytes = 64 << 10
	MaxBundleBytes   = 256 << 10
	MaxSumsBytes     = 64 << 10
)
