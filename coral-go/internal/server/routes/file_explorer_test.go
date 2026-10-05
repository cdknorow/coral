package routes

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExplorerListsAllFilesAndPages(t *testing.T) {
	root := setupTestDir(t)
	for i := 0; i < 501; i++ {
		require.NoError(t, os.WriteFile(filepath.Join(root, fmt.Sprintf("page-%03d.dat", i)), nil, 0600))
	}
	var names []string
	for _, offset := range []int{0, 500} {
		w := httptest.NewRecorder()
		serveExplorerDirectory(w, httptest.NewRequest("GET", fmt.Sprintf("/?offset=%d", offset), nil), root, ".")
		require.Equal(t, 200, w.Code)
		var response struct {
			dirResponse
			HasMore bool   `json:"has_more"`
			Root    string `json:"root"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
		require.Equal(t, root, response.Root)
		require.Equal(t, offset == 0, response.HasMore)
		if offset == 0 {
			require.Len(t, response.Entries, 500)
			require.Equal(t, "dir", response.Entries[0].Type)
		}
		for _, e := range response.Entries {
			names = append(names, e.Name)
		}
	}
	for _, name := range []string{".hidden", ".env", "image.png", "archive.zip", "large.go", "page-500.dat"} {
		require.Contains(t, names, name)
	}
}

func TestExplorerBoundaryAndErrors(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "outside")))
	for _, tc := range []struct {
		dir, query string
		status     int
	}{
		{"..", "", 400}, {outside, "", 400}, {"outside", "", 400},
		{"missing", "", 404}, {".", "?offset=-1", 400}, {".", "?offset=1000001", 400}, {".", "", 200},
	} {
		t.Run(tc.dir+tc.query, func(t *testing.T) {
			w := httptest.NewRecorder()
			serveExplorerDirectory(w, httptest.NewRequest("GET", "/"+tc.query, nil), root, tc.dir)
			require.Equal(t, tc.status, w.Code)
		})
	}
}

// TestExplorerOrderingMatchesReference pins the documented order: directories
// first, then case-insensitive name, ties broken by exact name, and page
// boundaries that neither repeat nor drop entries.
func TestExplorerOrderingMatchesReference(t *testing.T) {
	root := t.TempDir()
	names := []string{"b", "B", "a", "A", "_x", ".hidden", "Éclair", "éclair", "zeta", "Zeta", "10", "9", "résumé.md", "Résumé.md"}
	for i := 0; i < 700; i++ {
		names = append(names, fmt.Sprintf("n%03d", i), fmt.Sprintf("N%03d", i))
	}
	// Names that differ only in case collide on a case-insensitive file system
	// (the macOS default); on those, keep the first of each pair.
	require.NoError(t, os.WriteFile(filepath.Join(root, "probe"), nil, 0o600))
	_, statErr := os.Stat(filepath.Join(root, "PROBE"))
	foldedFS := statErr == nil
	require.NoError(t, os.Remove(filepath.Join(root, "probe")))
	seen := map[string]bool{}
	isDir := map[string]bool{}
	for i, n := range names {
		if foldedFS {
			if f := strings.ToLower(n); seen[f] {
				continue
			} else {
				seen[f] = true
			}
		}
		if i%4 == 0 {
			isDir[n] = true
			require.NoError(t, os.Mkdir(filepath.Join(root, n), 0o755))
		} else {
			require.NoError(t, os.WriteFile(filepath.Join(root, n), nil, 0o600))
		}
	}
	listing, err := os.ReadDir(root)
	require.NoError(t, err)
	want := make([]string, 0, len(listing))
	for _, e := range listing {
		want = append(want, e.Name())
	}
	sort.SliceStable(want, func(i, j int) bool {
		di, dj := isDir[want[i]], isDir[want[j]]
		if di != dj {
			return di
		}
		a, b := strings.ToLower(want[i]), strings.ToLower(want[j])
		if a != b {
			return a < b
		}
		return want[i] < want[j]
	})
	var got []string
	for offset := 0; ; offset += 500 {
		w := httptest.NewRecorder()
		serveExplorerDirectory(w, httptest.NewRequest("GET", fmt.Sprintf("/?offset=%d", offset), nil), root, ".")
		require.Equal(t, 200, w.Code)
		var resp struct {
			Entries []struct{ Name, Path, Type string }
			HasMore bool `json:"has_more"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		for _, e := range resp.Entries {
			require.Equal(t, e.Name, e.Path)
			require.Equal(t, isDir[e.Name], e.Type == "dir")
			got = append(got, e.Name)
		}
		if !resp.HasMore {
			break
		}
	}
	require.Equal(t, want, got)
}
