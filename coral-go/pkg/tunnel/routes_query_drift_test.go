package tunnel

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The per-route query table is generated from the handlers (tools/gen_route_queries.py).
// A handler that starts reading a new parameter without a regeneration, or a hand edit of
// the table, makes the tunnel refuse (or accept) parameters the handler does not expect.
// This runs the generator, writing to a scratch file, and fails if its output differs from
// the committed table (#3279). It needs python3, gofmt and go (the generator asks the Go
// route table which routes the tunnel allows, #3942); it is skipped without them.
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
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not found (the generator asks the Go route table, #3942)")
	}

	// The generator reads coral-go/internal/server and asks the route table through
	// coral-go/internal/tools/routeshapes, from the repository root; it writes to a scratch file.
	out := filepath.Join(t.TempDir(), "routes_query.go")
	cmd := exec.Command(python, "-I", gen, "--out", out)
	cmd.Dir = repo
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generator failed: %v\n%s", err, b)
	}

	want, err := os.ReadFile(out)
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
