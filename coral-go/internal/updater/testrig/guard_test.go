package testrig

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func goRoot(t *testing.T) string {
	t.Helper()
	d, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)
	return d
}

// releasedPrograms are the programs a release ships: tests/release/commands.txt and gui_commands.txt, plus coral-update.
func releasedPrograms(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, f := range []string{"commands.txt", "gui_commands.txt"} {
		fh, err := os.Open(filepath.Join(goRoot(t), "..", "tests", "release", f))
		require.NoError(t, err)
		sc := bufio.NewScanner(fh)
		for sc.Scan() {
			if l := strings.TrimSpace(sc.Text()); l != "" && !strings.HasPrefix(l, "#") {
				out = append(out, l)
			}
		}
		fh.Close()
	}
	return append(out, "coral-update")
}

// No program a release ships links the fake Sigstore, the rehearsal's packages, or sigstore-go's test CA, on any platform or build tier: the rig's keys and
// its trust of them exist only in the rehearsal's own program.
func TestNoReleasedProgramLinksTheRehearsalOrAFakeSigstore(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go list over every released program")
	}
	root := goRoot(t)
	banned := []string{"/internal/updater/testrig", "github.com/sigstore/sigstore-go/pkg/testing"}
	checked := 0
	for _, goos := range []string{"linux", "darwin", "windows"} {
		for _, tags := range []string{"fts5", "fts5,dev", "fts5,beta", "fts5,dropboxers"} {
			for _, prog := range releasedPrograms(t) {
				dir := filepath.Join(root, "cmd", prog)
				if _, err := os.Stat(dir); err != nil {
					continue
				}
				cmd := exec.Command("go", "list", "-deps", "-tags", tags, "-f", "{{.ImportPath}}", "./cmd/"+prog)
				cmd.Dir = root
				cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH=amd64", "CGO_ENABLED=0")
				out, err := cmd.CombinedOutput()
				if err != nil {
					continue // a program that does not build without cgo on this platform (the GUI ones) is checked where it does
				}
				checked++
				for _, imp := range strings.Fields(string(out)) {
					for _, b := range banned {
						assert.False(t, strings.Contains(imp, b), "%s (%s, %s) links %s", prog, goos, tags, imp)
					}
				}
			}
		}
	}
	assert.Greater(t, checked, 30, "the census looked at the programs")
	// and the rehearsal's own program is not in the lists of what ships
	for _, p := range releasedPrograms(t) {
		assert.NotContains(t, p, "rehearsal")
	}
}

// cmd/coral-update decides its own trust: the compiled-in pins (verify.DefaultPins), the embedded or TUF-refreshed Sigstore root, the Sigstore verifier over
// that root, and the fetcher with only an HTTP client. Nothing in it builds pins or a verifier of its own, takes a base URL, a scheme or a host list, or reads
// a setting from the environment but the switch that turns updates off. A test (or an operator) cannot make it trust another root.
func TestCoralUpdateTakesOnlyTheCompiledInPinsAndTheRealTrust(t *testing.T) {
	dir := filepath.Join(goRoot(t), "cmd", "coral-update")
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	require.NoError(t, err)
	require.Len(t, pkgs, 1)
	var calls = map[string]int{}
	for _, pkg := range pkgs {
		for name, f := range pkg.Files {
			ast.Inspect(f, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.CompositeLit:
					if sel, ok := x.Type.(*ast.SelectorExpr); ok {
						calls["lit:"+exprName(sel)]++
						if sel.Sel.Name == "Pins" {
							t.Errorf("%s: builds its own pins (%s)", filepath.Base(name), fset.Position(x.Pos()))
						}
						if sel.Sel.Name == "Fetcher" {
							for _, el := range x.Elts {
								if kv, ok := el.(*ast.KeyValueExpr); ok {
									if id, ok := kv.Key.(*ast.Ident); !ok || id.Name != "Client" {
										t.Errorf("%s: the fetcher is set up with more than a client (%s)", filepath.Base(name), fset.Position(kv.Pos()))
									}
								}
							}
						}
					}
				case *ast.KeyValueExpr:
					if id, ok := x.Key.(*ast.Ident); ok {
						switch id.Name {
						case "LatestBase", "DownloadBase", "Scheme", "Allow":
							t.Errorf("%s: sets %s, an address or host rule (%s)", filepath.Base(name), id.Name, fset.Position(x.Pos()))
						}
					}
				case *ast.CallExpr:
					calls["call:"+exprName(x.Fun)]++
					if id := exprName(x.Fun); id == "os.Getenv" || id == "getenv" {
						if len(x.Args) == 1 {
							if lit, ok := x.Args[0].(*ast.BasicLit); !ok || lit.Value != `"CORAL_AUTO_UPDATE"` {
								t.Errorf("%s: reads an environment variable other than CORAL_AUTO_UPDATE (%s)", filepath.Base(name), fset.Position(x.Pos()))
							}
						}
					}
				}
				return true
			})
		}
	}
	assert.Positive(t, calls["call:verify.DefaultPins"], "the pins are the compiled-in ones")
	assert.Positive(t, calls["call:verify.LoadTrust"]+calls["call:verify.EmbeddedTrust"], "the trust is the embedded or TUF-refreshed root")
	assert.Positive(t, calls["lit:verify.Sigstore"], "the verifier is the real one")
}

func exprName(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.SelectorExpr:
		return exprName(x.X) + "." + x.Sel.Name
	case *ast.Ident:
		return x.Name
	}
	return ""
}

// The Windows rehearsal's program builds only with the windowsrehearsal tag. Outside the program's own source, only the rehearsal's script and workflow may name the
// tag: a release path (release.yml, release-cache-warm.yml, tools/build_windows.ps1, installers/build-*.sh, the windows-tests package job, a dry run) that passes it
// would build the rig's program, with its fake trust, into something that ships.
var windowsRehearsalTagAllowed = map[string]bool{
	"tests/release/update_rehearsal_windows.ps1":       true,
	"tests/release/update_rehearsal_windows_route.ps1": true,
	"tests/release/rehearsal_build.ps1":                true,
	".github/workflows/update-rehearsal-windows.yml":   true,
}

// windowsRehearsalTagUses returns the files under repo (paths with forward slashes) that name the tag and are not allowed to.
func windowsRehearsalTagUses(t *testing.T, repo string) []string {
	t.Helper()
	var bad []string
	for _, top := range []string{".github", "installers", "tools", "tests", "coral-go/Makefile", "Casks"} {
		_ = filepath.WalkDir(filepath.Join(repo, filepath.FromSlash(top)), func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				if d != nil && d.IsDir() && d.Name() == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if fi, e := d.Info(); e != nil || fi.Size() > 4<<20 {
				return nil
			}
			b, e := os.ReadFile(p)
			if e != nil || !strings.Contains(string(b), "windowsrehearsal") {
				return nil
			}
			rel, _ := filepath.Rel(repo, p)
			rel = filepath.ToSlash(rel)
			if !windowsRehearsalTagAllowed[rel] {
				bad = append(bad, rel)
			}
			return nil
		})
	}
	return bad
}

func TestNoReleasePathPassesTheWindowsRehearsalTag(t *testing.T) {
	repo := filepath.Join(goRoot(t), "..")
	assert.Empty(t, windowsRehearsalTagUses(t, repo), "only the rehearsal's own script and workflow may name windowsrehearsal")
	for p := range windowsRehearsalTagAllowed {
		_, err := os.Stat(filepath.Join(repo, filepath.FromSlash(p)))
		assert.NoError(t, err, "%s is allowed to name the tag, so it must exist (a stale entry is a hole)", p)
	}
	// the census sees a release path that passes the tag (a mutant)
	tmp := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "tools"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "tools", "build_windows.ps1"), []byte("go build -tags fts5,windowsrehearsal ./cmd/coral\n"), 0o644))
	assert.Equal(t, []string{"tools/build_windows.ps1"}, windowsRehearsalTagUses(t, tmp))
}
