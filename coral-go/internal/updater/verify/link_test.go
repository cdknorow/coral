package verify

import (
	"os"
	"os/exec"
	"path/filepath"
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
			assert.NotEqual(t, "github.com/cdknorow/coral/internal/updater/verify", d, "%s links the update verifier", pkg)
		}
		checked++
	}
	assert.Greater(t, checked, 5, "the programs under cmd/ were found")
	// And nothing else in the module imports the verifier except the updater's own program.
	const self = "github.com/cdknorow/coral/internal/updater/verify"
	for _, line := range list("-f", "{{.ImportPath}}|{{join .Imports \" \"}}", "./...") {
		pkg, imports, _ := strings.Cut(line, "|")
		if pkg == self || strings.HasSuffix(pkg, "/cmd/coral-update") || strings.HasPrefix(pkg, self+"/") {
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
	assert.Equal(t, "https://github.com/cdknorow/coral/.github/workflows/release.yml@refs/tags/v1.4.120", releaseSAN(Repo, "v1.4.120"))
	p := DefaultPins()
	assert.Equal(t, "https://github.com/cdknorow/coral-promote/.github/workflows/promote.yml@refs/heads/main", promoteIdentity(p).SAN)
	assert.Equal(t, "https://token.actions.githubusercontent.com", p.Issuer)
}
