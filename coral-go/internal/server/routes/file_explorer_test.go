package routes

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
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
