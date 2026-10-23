package verify

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	assert.Equal(t, "https://github.com/subgentic/coral/.github/workflows/promote.yml@refs/heads/promote", promoteIdentity(p).SAN)
	assert.Equal(t, "https://token.actions.githubusercontent.com", p.Issuer)
}

// A workflow that can ask for an OIDC token can get a Sigstore certificate for its own path, so which workflows may is a list the verifier's pins are only as good as (#6274). The two the updater trusts, release.yml
// and promote.yml, are the signers; nothing else may request an id-token with write access unless it is named here with the reason, so a new one fails this test until it is read and added on purpose.
// (A certificate from any other workflow is refused by the verifier all the same: its SAN is another path. This keeps the list of such workflows short and known.)
func TestOnlyNamedWorkflowsRequestAnIDTokenWithWriteAccess(t *testing.T) {
	signers := map[string]string{
		"release.yml": "signs SHA256SUMS.txt for the tag it runs on (the certificate the updater trusts for a release)",
		"promote.yml": "signs the promote manifest, on the protected promote branch, run by hand (the certificate the updater trusts for a promotion)",
	}
	others := map[string]string{
		"docs.yml":                "deploys GitHub Pages: the Pages action asks for the token to authenticate the deployment",
		"release-cache-warm.yml":  "the build provenance attestation of the cache payload manifest",
		"sigstore-cert-probe.yml": "manual, read-only, signs a dummy file to check that certificates are issued as expected",
	}
	dir := filepath.Join("..", "..", "..", "..", ".github", "workflows")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	idToken := regexp.MustCompile(`(?m)^\s*id-token:\s*write\b`)
	var found []string
	for _, e := range entries {
		if e.IsDir() || !(strings.HasSuffix(e.Name(), ".yml") || strings.HasSuffix(e.Name(), ".yaml")) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		require.NoError(t, err)
		if !idToken.Match(b) {
			continue
		}
		found = append(found, e.Name())
		_, isSigner := signers[e.Name()]
		_, isOther := others[e.Name()]
		assert.True(t, isSigner || isOther, "%s requests id-token: write and is not named in this test: a workflow that can mint certificates is a signer; read it, and add it here with its reason only on purpose", e.Name())
	}
	assert.Contains(t, found, "release.yml", "the release workflow signs: the census finds it")
	// the signers are the paths the pins name, and only those
	assert.Equal(t, filepath.Base(ReleaseWorkflow), "release.yml")
	assert.Equal(t, filepath.Base(DefaultPins().PromoteWorkflow), "promote.yml")
	for name := range signers {
		assert.Contains(t, []string{filepath.Base(ReleaseWorkflow), filepath.Base(DefaultPins().PromoteWorkflow)}, name)
	}
	// if promote.yml is present with write access it is the real one on the promote branch; the stub on main has none (it only exists so a dispatch can find the file)
	if b, err := os.ReadFile(filepath.Join(dir, "promote.yml")); err == nil && idToken.Match(b) {
		assert.Contains(t, string(b), "workflow_dispatch", "promote.yml runs by hand only")
		assert.NotRegexp(t, `(?m)^\s*(push|pull_request|schedule|pull_request_target):`, string(b), "promote.yml has no other trigger")
	}
}
