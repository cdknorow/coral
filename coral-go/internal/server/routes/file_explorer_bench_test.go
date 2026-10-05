package routes

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// explorerFixture builds a synthetic directory with n entries: one directory
// per ten files, mixed-case names so the case-insensitive ordering is exercised.
func explorerFixture(tb testing.TB, n int) string {
	tb.Helper()
	root := tb.TempDir()
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("Entry-%06d.Dat", (i*7919)%n)
		if i%3 == 0 {
			name = fmt.Sprintf("entry-%06d.dat", (i*7919)%n)
		}
		p := filepath.Join(root, name)
		if i%10 == 0 {
			if err := os.Mkdir(p, 0o755); err != nil {
				tb.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			tb.Fatal(err)
		}
	}
	return root
}

func BenchmarkExplorerDirectory(b *testing.B) {
	for _, n := range []int{5000, 50000} {
		root := explorerFixture(b, n)
		b.Run(fmt.Sprintf("entries=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				w := httptest.NewRecorder()
				serveExplorerDirectory(w, httptest.NewRequest("GET", "/?offset=0", nil), root, ".")
				if w.Code != 200 {
					b.Fatal(w.Code)
				}
			}
		})
	}
}
