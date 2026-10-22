package install

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Codex Test's survey of the Homebrew cask (#5280 step 3 B): the cask is recognised by the Caskroom link or by its recorded metadata, in a default or a named prefix, and a Caskroom folder
// or a receipt alone is not enough.

type brewRig struct {
	t       *testing.T
	root    string // stands for / : prefixes and appdirs live under it
	prefix  string
	appdir  string
	app     string
	options Options
}

func newBrewRig(t *testing.T) *brewRig {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	r := &brewRig{t: t, root: root, prefix: filepath.Join(root, "opt", "homebrew"), appdir: filepath.Join(root, "Applications")}
	require.NoError(t, os.MkdirAll(r.prefix, 0o755))
	require.NoError(t, os.MkdirAll(r.appdir, 0o755))
	r.app = filepath.Join(r.appdir, "Coral.app")
	require.NoError(t, os.MkdirAll(filepath.Join(r.app, "Contents", "MacOS"), 0o755))
	r.options = Options{Prefixes: []string{r.prefix}, Appdirs: []string{r.appdir}}
	return r
}

func (r *brewRig) link(version string) {
	r.t.Helper()
	dir := filepath.Join(r.prefix, "Caskroom", "coral", version)
	require.NoError(r.t, os.MkdirAll(dir, 0o755))
	require.NoError(r.t, os.Symlink(r.app, filepath.Join(dir, "Coral.app")))
}

func (r *brewRig) metadata(version, file, body string) {
	r.t.Helper()
	dir := filepath.Join(r.prefix, "Caskroom", "coral", ".metadata", version, "20261020120000.000", "Casks")
	require.NoError(r.t, os.MkdirAll(dir, 0o755))
	require.NoError(r.t, os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644))
}

func TestNothingIsManagedWithoutEvidence(t *testing.T) {
	r := newBrewRig(t)
	assert.Empty(t, MacManaged(r.app, r.options), "a dragged copy")
	require.NoError(t, os.MkdirAll(filepath.Join(r.prefix, "Caskroom", "coral", "1.4.100"), 0o755))
	assert.Empty(t, MacManaged(r.app, r.options), "a token folder alone proves nothing")
	require.NoError(t, os.WriteFile(filepath.Join(r.prefix, "Caskroom", "coral", "1.4.100", "INSTALL_RECEIPT.json"), []byte("{}"), 0o644))
	assert.Empty(t, MacManaged(r.app, r.options), "a receipt alone proves nothing")
	assert.Empty(t, MacManaged(filepath.Join(r.root, "missing", "Coral.app"), r.options), "an app that cannot be read is not called managed")
}

func TestTheCaskroomLinkToThisAppMeansHomebrew(t *testing.T) {
	r := newBrewRig(t)
	r.link("1.4.100")
	assert.Equal(t, MacHomebrew, MacManaged(r.app, r.options))

	// a link to ANOTHER copy of Coral does not make this one managed
	other := newBrewRig(t)
	require.NoError(t, os.MkdirAll(filepath.Join(other.root, "Elsewhere", "Coral.app"), 0o755))
	dir := filepath.Join(other.prefix, "Caskroom", "coral", "1.4.100")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.Symlink(filepath.Join(other.root, "Elsewhere", "Coral.app"), filepath.Join(dir, "Coral.app")))
	assert.Empty(t, MacManaged(other.app, other.options))
}

func TestTheCaskRecordedAsInstalledMetadataMeansHomebrewWhateverTheFileIsCalled(t *testing.T) {
	for _, tc := range []struct{ file, body string }{
		{"coral.json", `{"token":"coral","artifacts":[{"app":["Coral.app"]}]}`},
		{"coral.internal.json", `{"token":"coral","artifacts":[["app",["Coral.app"],{}]]}`},
		{"coral.rb", "cask \"coral\" do\n  version \"1.4.100\"\n  app \"Coral.app\"\nend\n"},
	} {
		r := newBrewRig(t)
		r.metadata("1.4.100", tc.file, tc.body)
		assert.Equal(t, MacHomebrew, MacManaged(r.app, r.options), tc.file+": no link, the receipt missing, still managed")
	}
}

func TestTheMetadataHasToNameThisAppAndThisAppHasToSitInAnAppdir(t *testing.T) {
	r := newBrewRig(t)
	r.metadata("1.4.100", "coral.json", `{"artifacts":[{"app":["Other.app"]}]}`)
	assert.Empty(t, MacManaged(r.app, r.options), "metadata for another bundle name")

	r = newBrewRig(t)
	r.metadata("1.4.100", "coral.json", `{"artifacts":[{"app":["Coral.app"]}]}`)
	elsewhere := filepath.Join(r.root, "Users", "me", "Downloads", "Coral.app")
	require.NoError(t, os.MkdirAll(elsewhere, 0o755))
	assert.Empty(t, MacManaged(elsewhere, r.options), "a copy that is not in an appdir is not where Homebrew puts it")

	big := make([]byte, maxCaskBytes+1)
	r = newBrewRig(t)
	r.metadata("1.4.100", "coral.json", string(big))
	assert.Empty(t, MacManaged(r.app, r.options), "an oversized file is not read")
}

func TestAPrefixTheEnvironmentNamesIsLookedInAndTheDefaultsAreAlways(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the macOS classifier's paths (/opt/homebrew, /Applications) are not absolute paths on Windows, where it never runs")
	}
	o := MacOptions("/home/someone", "")
	assert.Equal(t, []string{"/opt/homebrew", "/usr/local"}, o.Prefixes, "a Finder-launched Coral has no variable: the defaults are looked in anyway")
	assert.Equal(t, []string{"/Applications", "/home/someone/Applications"}, o.Appdirs)

	o = MacOptions("/home/someone", "/custom/brew")
	assert.Equal(t, []string{"/opt/homebrew", "/usr/local", "/custom/brew"}, o.Prefixes)
	assert.Equal(t, []string{"/opt/homebrew", "/usr/local"}, MacOptions("", "relative/prefix").Prefixes, "a relative prefix is not a place")

	// and a cask under such a prefix is found
	r := newBrewRig(t)
	custom := filepath.Join(r.root, "custom", "brew")
	r.prefix = custom
	require.NoError(t, os.MkdirAll(custom, 0o755))
	r.link("1.4.100")
	assert.Equal(t, MacHomebrew, MacManaged(r.app, Options{Prefixes: []string{custom}, Appdirs: []string{r.appdir}}))
}
