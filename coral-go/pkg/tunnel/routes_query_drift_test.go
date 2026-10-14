package tunnel

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The per-route query table is generated from the handlers (tools/gen_route_queries.py).
// A handler that starts reading a new parameter without a regeneration, or a hand edit of
// the table, makes the tunnel refuse (or accept) parameters the handler does not expect.
// This runs the generator on a copy of its inputs and fails if its output differs from the
// committed table (#3279). It needs python3 and gofmt; it is skipped without them.
func TestRouteQueriesTableMatchesTheGenerator(t *testing.T) {
	repo, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	gen := filepath.Join(repo, "tools", "gen_route_queries.py")
	if _, err := os.Stat(gen); err != nil {
		t.Skip("tools/gen_route_queries.py not found (not a full checkout)")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not found")
	}
	if _, err := exec.LookPath("gofmt"); err != nil {
		t.Skip("gofmt not found")
	}

	// The generator reads coral-go/internal/server and writes coral-go/pkg/tunnel/routes_query.go,
	// relative to the repository root: give it a scratch root with just those.
	tmp := t.TempDir()
	copyTree(t, filepath.Join(repo, "coral-go", "internal", "server"), filepath.Join(tmp, "coral-go", "internal", "server"))
	copyFile(t, gen, filepath.Join(tmp, "tools", "gen_route_queries.py"))
	if err := os.MkdirAll(filepath.Join(tmp, "coral-go", "pkg", "tunnel"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(python, "-I", filepath.Join("tools", "gen_route_queries.py"))
	cmd.Dir = tmp
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generator failed: %v\n%s", err, out)
	}

	want, err := os.ReadFile(filepath.Join(tmp, "coral-go", "pkg", "tunnel", "routes_query.go"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("routes_query.go")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("pkg/tunnel/routes_query.go differs from what the handlers need.\n"+
			"Run from the repository root: python3 tools/gen_route_queries.py, then review the diff (it is a security allowlist).\n%s",
			lineDiff(string(got), string(want)))
	}
}

func copyTree(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(to, rel), 0o755)
		}
		if strings.HasSuffix(p, ".go") {
			copyFile(t, p, filepath.Join(to, rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	b, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// lineDiff lists the lines only in the committed table (-) and only in the generated one (+).
func lineDiff(committed, generated string) string {
	in := func(s string) map[string]bool {
		m := map[string]bool{}
		for _, l := range strings.Split(s, "\n") {
			m[l] = true
		}
		return m
	}
	c, g := in(committed), in(generated)
	var b strings.Builder
	for _, l := range strings.Split(committed, "\n") {
		if !g[l] {
			b.WriteString("- " + l + "\n")
		}
	}
	for _, l := range strings.Split(generated, "\n") {
		if !c[l] {
			b.WriteString("+ " + l + "\n")
		}
	}
	return b.String()
}
