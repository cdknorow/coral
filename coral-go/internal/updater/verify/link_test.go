package verify

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// The verifier is linked only into cmd/coral-update: the server holds the API key and never links the Sigstore code (#5245 section 12), and
// the size of the library (+15 MB) lands only on the updater. Every other program under cmd/ is checked.
func TestSigstoreIsLinkedOnlyIntoTheUpdater(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go tool")
	}
	list := func(args ...string) []string {
		cmd := exec.Command("go", append([]string{"list"}, args...)...)
		cmd.Dir = filepath.Join("..", "..", "..")
		out, err := cmd.Output()
		require.NoError(t, err)
		return strings.Fields(string(out))
	}
	checked := 0
	for _, pkg := range list("-f", "{{.ImportPath}}", "./cmd/...") {
		if strings.HasSuffix(pkg, "/cmd/coral-update") {
			continue
		}
		deps := list("-deps", "-f", "{{.ImportPath}}", pkg)
		for _, d := range deps {
			assert.NotContains(t, d, "sigstore/sigstore-go", "%s links the Sigstore verifier", pkg)
			assert.NotEqual(t, "github.com/subgentic/coral/internal/updater/verify", d, "%s links the update verifier", pkg)
		}
		checked++
	}
	assert.Greater(t, checked, 5, "the programs under cmd/ were found")
	// And nothing else in the module imports the verifier except the updater's own program and the packages under internal/updater (which only
	// cmd/coral-update links: the loop above holds that for every other program).
	const self = "github.com/subgentic/coral/internal/updater/verify"
	for _, line := range list("-f", "{{.ImportPath}}|{{join .Imports \" \"}}", "./...") {
		pkg, imports, _ := strings.Cut(line, "|")
		if pkg == self || strings.HasSuffix(pkg, "/cmd/coral-update") || strings.Contains(pkg, "/internal/updater/") {
			continue
		}
		assert.NotContains(t, strings.Fields(imports), self, "%s imports the update verifier", pkg)
	}
}

// The compiled-in identities name the workflow that exists in this repository: a rename of release.yml fails here, so the pin changes with it
// (and, per the rotation rule, in a release promoted while the old pin still holds).
func TestThePinsNameTheWorkflowsOfThisRepository(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")
	b, err := os.ReadFile(filepath.Join(root, ReleaseWorkflow))
	require.NoError(t, err, "the pinned release workflow exists")
	assert.Contains(t, string(b), "name: Release", "the certificate's workflow name is Release")
	assert.Regexp(t, `(?m)^\s*-\s*['"]?v\*`, string(b), "release.yml runs on v* tags, which is what the pinned SAN's ref names")
	assert.Equal(t, "https://github.com/subgentic/coral/.github/workflows/release.yml@refs/tags/v1.4.120", releaseSAN(Repo, "v1.4.120"))
	p := DefaultPins()
	assert.Equal(t, "https://github.com/subgentic/coral/.github/workflows/promote.yml@refs/heads/main", promoteIdentity(p).SAN)
	assert.Equal(t, "https://token.actions.githubusercontent.com", p.Issuer)
}

// The promote workflow the pins name is this repository's (#326), and it keeps the shape the trust relies on: run by hand only (another trigger
// would sign under an identity the updater refuses, or, for a push or a schedule, without anyone asking), and every job that can sign (an OIDC
// token) or write runs in the environment "promote", whose required reviewer is the operator.
func TestThePromoteWorkflowRunsByHandAndSignsOnlyAfterApproval(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "..", PromoteWorkflow))
	require.NoError(t, err, "the pinned promote workflow exists")
	var wf struct {
		Name        string                `yaml:"name"`
		On          map[string]any        `yaml:"on"`
		Permissions map[string]string     `yaml:"permissions"`
		Jobs        map[string]promoteJob `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(b, &wf))
	assert.Equal(t, "Promote", wf.Name)
	var triggers []string
	for k := range wf.On {
		triggers = append(triggers, k)
	}
	assert.Equal(t, []string{PromoteTrigger}, triggers, "workflow_dispatch and nothing else")
	assert.Empty(t, wf.Permissions, "no permission at the top: each job names its own")
	signers := 0
	for name, j := range wf.Jobs {
		if j.Permissions["id-token"] == "write" || j.Permissions["contents"] == "write" || j.Permissions["packages"] == "write" || j.Permissions["actions"] == "write" {
			signers++
			assert.Equal(t, "promote", j.Environment, "job %s can sign or write: it must wait for the operator's approval in the environment promote", name)
		}
		for perm, v := range j.Permissions {
			if v == "write" {
				assert.Contains(t, []string{"id-token", "contents"}, perm, "job %s writes %s", name, perm)
			}
		}
	}
	assert.Equal(t, 1, signers, "one job signs and publishes")
}

type promoteJob struct {
	Environment string            `yaml:"environment"`
	Permissions map[string]string `yaml:"permissions"`
}

// The two workflows of this repository never stand in for each other (#326, Security): release.yml's identity and promote.yml's differ in the
// workflow file, the ref and the trigger, so a release build can't sign a promotion, a promotion can't sign a release's sums, and promote.yml run
// from a branch or a tag is neither.
func TestTheReleaseAndPromoteIdentitiesNeverMatch(t *testing.T) {
	p := DefaultPins()
	pro, rel := promoteIdentity(p), releaseIdentity(p, "v1.4.158")
	assert.Equal(t, pro.SourceRepositoryID, rel.SourceRepositoryID, "the same repository")
	assert.NotEqual(t, pro.SAN, rel.SAN)
	assert.NotEqual(t, pro.SourceRepositoryRef, rel.SourceRepositoryRef)
	assert.NotEqual(t, pro.BuildTrigger, rel.BuildTrigger)
	assert.Equal(t, "refs/heads/main", pro.SourceRepositoryRef)
	assert.Equal(t, "workflow_dispatch", pro.BuildTrigger)
	assert.Equal(t, "push", rel.BuildTrigger)
}
