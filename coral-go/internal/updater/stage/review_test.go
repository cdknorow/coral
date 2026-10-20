package stage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/updater/verify"
)

// #5301: a verified manifest's revocations count at once, not only after the install: the staged release is not installed yet, and a replayed older manifest that does not
// revoke what a later one did must not make that version installable again.
func TestAVerifiedManifestsRevocationsAreKeptAtOnce(t *testing.T) {
	e := newEnv(t, "1.4.125")
	version := "1.4.130"
	pkg := []byte("the package bytes of " + version)
	sums := []byte(sum(pkg) + "  " + asset(version) + "\n")
	m := verify.Manifest{Schema: 1, Repo: verify.Repo, Tag: "v" + version, Version: version, SourceSHA: testCommit, SHA256SumsSHA: sum(sums), Sequence: 7,
		PromotedAt: "2026-10-20T10:00:00Z", ExpiresAt: "2026-11-20T10:00:00Z", Revoked: []string{"1.4.99", "1.4.126"}}
	manifest, _ := json.Marshal(m)
	e.rel.mu.Lock()
	e.rel.version, e.rel.pkg = version, pkg
	e.rel.files = map[string][]byte{"promote.json": manifest, "promote.json.sigstore.json": bundleOf(manifest, ""), "SHA256SUMS.txt": sums,
		"SHA256SUMS.txt.sigstore.json": bundleOf(sums, testCommit), asset(version): pkg}
	e.rel.mu.Unlock()
	require.Equal(t, ResultStaged, e.check().Result)
	st := e.state()
	assert.Equal(t, []string{"1.4.126", "1.4.99"}, st.Revoked, "kept although nothing is installed")
	assert.Empty(t, st.Version, "the installed high-water mark is still the apply's")
	// The next manifest (even a replayed older one) cannot approve a revoked version.
	e.rel.publish("1.4.126", []byte("an older, revoked release"))
	res := e.check()
	assert.Equal(t, ResultFailed, res.Result, "%+v", res)
	assert.Equal(t, verify.StageManifestRules, res.FailedStage)
}

// The staged record sits in a user-writable folder: an asset name in it that is a path never leaves the folder.
func TestLoadStagedRefusesARecordWhoseAssetIsAPath(t *testing.T) {
	e := newEnv(t, "1.4.125")
	require.Equal(t, ResultStaged, e.check().Result)
	s, err := LoadStaged(e.data)
	require.NoError(t, err)
	recPath := filepath.Join(s.Dir, RecordFile)
	for _, asset := range []string{"../1.4.130/x", "../../state.json", "/etc/passwd", `..\x`, "a/b", "..", ".hidden", "bad\x00name"} {
		rec := *s.Record
		rec.Asset = asset
		b, _ := json.Marshal(rec)
		require.NoError(t, os.WriteFile(recPath, b, 0o600))
		_, err := LoadStaged(e.data)
		assert.ErrorIs(t, err, ErrNoStaged, "%q", asset)
	}
	// And a check with such a record stages afresh instead of opening the path.
	rec := *s.Record
	rec.Asset = "../../" + strings.Repeat("x", 3)
	b, _ := json.Marshal(rec)
	require.NoError(t, os.WriteFile(recPath, b, 0o600))
	assert.Equal(t, ResultStaged, e.check().Result)
}
