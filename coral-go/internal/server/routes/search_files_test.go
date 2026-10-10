package routes

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupTestDir creates a temporary directory populated with test files and dirs.
func setupTestDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	// Directories
	os.MkdirAll(filepath.Join(root, "src", "components"), 0755)
	os.MkdirAll(filepath.Join(root, "docs"), 0755)
	os.MkdirAll(filepath.Join(root, ".hidden"), 0755)
	os.MkdirAll(filepath.Join(root, "empty"), 0755)

	// Text files at root
	os.WriteFile(filepath.Join(root, "main.go"), []byte("package main"), 0644)
	os.WriteFile(filepath.Join(root, "README.md"), []byte("# readme"), 0644)
	os.WriteFile(filepath.Join(root, "Makefile"), []byte("all: build"), 0644)
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test"), 0644)

	// Nested text files
	os.WriteFile(filepath.Join(root, "src", "app.go"), []byte("package src"), 0644)
	os.WriteFile(filepath.Join(root, "src", "components", "button.tsx"), []byte("export default"), 0644)
	os.WriteFile(filepath.Join(root, "docs", "guide.md"), []byte("# guide"), 0644)

	// Binary files (should be filtered by whitelist)
	os.WriteFile(filepath.Join(root, "coral"), []byte("\x00binary"), 0755) // extensionless binary
	os.WriteFile(filepath.Join(root, "image.png"), []byte("fakepng"), 0644)
	os.WriteFile(filepath.Join(root, "archive.zip"), []byte("fakezip"), 0644)

	// Hidden files
	os.WriteFile(filepath.Join(root, ".gitignore"), []byte("*.log"), 0644)
	os.WriteFile(filepath.Join(root, ".env"), []byte("SECRET=x"), 0644)

	// Large file (>1MB)
	largeData := make([]byte, 1<<20+1) // 1MB + 1 byte
	os.WriteFile(filepath.Join(root, "large.go"), largeData, 0644)

	return root
}

// parseDirResponse parses the JSON response from searchFilesDir.
type dirResponse struct {
	Entries []struct {
		Name string `json:"name"`
		Path string `json:"path"`
		Type string `json:"type"`
	} `json:"entries"`
	Dir string `json:"dir"`
}

func callSearchFilesDir(t *testing.T, handler *SessionsHandler, workdir, dir, filter string) dirResponse {
	t.Helper()
	w := httptest.NewRecorder()
	handler.searchFilesDir(w, context.Background(), workdir, dir, filter, 10000, false)
	require.Equal(t, 200, w.Code)
	var resp dirResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	return resp
}

func entryNames(resp dirResponse) []string {
	names := make([]string, len(resp.Entries))
	for i, e := range resp.Entries {
		names[i] = e.Name
	}
	return names
}

func entryTypes(resp dirResponse) map[string]string {
	types := make(map[string]string)
	for _, e := range resp.Entries {
		types[e.Name] = e.Type
	}
	return types
}

// ── Directory Browsing Tests ────────────────────────────────────────

func TestSearchFilesDir_RootListing(t *testing.T) {
	root := setupTestDir(t)
	handler := &SessionsHandler{}

	resp := callSearchFilesDir(t, handler, root, ".", "")

	assert.Equal(t, ".", resp.Dir)
	names := entryNames(resp)

	// Directories should appear (with trailing /)
	assert.Contains(t, names, "docs/")
	assert.Contains(t, names, "empty/")
	assert.Contains(t, names, "src/")

	// Text files should appear
	assert.Contains(t, names, "main.go")
	assert.Contains(t, names, "README.md")
	assert.Contains(t, names, "Makefile")
	assert.Contains(t, names, "go.mod")

	// Binary files should NOT appear (whitelist filter)
	assert.NotContains(t, names, "image.png")
	assert.NotContains(t, names, "archive.zip")
	assert.NotContains(t, names, "coral") // extensionless, not in whitelist

	// Large files should NOT appear (>1MB)
	assert.NotContains(t, names, "large.go")

	// Hidden files should NOT appear (unless filter starts with .)
	assert.NotContains(t, names, ".gitignore")
	assert.NotContains(t, names, ".env")
	assert.NotContains(t, names, ".hidden/")
}

func TestSearchFilesDir_DirsBeforeFiles(t *testing.T) {
	root := setupTestDir(t)
	handler := &SessionsHandler{}

	resp := callSearchFilesDir(t, handler, root, ".", "")

	types := entryTypes(resp)
	// Find first file and last dir
	lastDirIdx := -1
	firstFileIdx := len(resp.Entries)
	for i, e := range resp.Entries {
		if types[e.Name] == "dir" {
			lastDirIdx = i
		}
		if types[e.Name] == "file" && i < firstFileIdx {
			firstFileIdx = i
		}
	}
	assert.Less(t, lastDirIdx, firstFileIdx, "directories should sort before files")
}

func TestSearchFilesDir_NestedDirectory(t *testing.T) {
	root := setupTestDir(t)
	handler := &SessionsHandler{}

	resp := callSearchFilesDir(t, handler, root, "src", "")

	assert.Equal(t, "src", resp.Dir)
	names := entryNames(resp)
	assert.Contains(t, names, "components/")
	assert.Contains(t, names, "app.go")

	// Paths should include the directory prefix
	for _, e := range resp.Entries {
		if e.Name == "app.go" {
			assert.Equal(t, "src/app.go", e.Path)
		}
		if e.Name == "components/" {
			assert.Equal(t, "src/components", e.Path)
		}
	}
}

func TestSearchFilesDir_DeepNested(t *testing.T) {
	root := setupTestDir(t)
	handler := &SessionsHandler{}

	resp := callSearchFilesDir(t, handler, root, "src/components", "")

	assert.Equal(t, "src/components", resp.Dir)
	names := entryNames(resp)
	assert.Contains(t, names, "button.tsx")

	for _, e := range resp.Entries {
		if e.Name == "button.tsx" {
			assert.Equal(t, "src/components/button.tsx", e.Path)
		}
	}
}

func TestSearchFilesDir_FilterNarrowsResults(t *testing.T) {
	root := setupTestDir(t)
	handler := &SessionsHandler{}

	resp := callSearchFilesDir(t, handler, root, ".", "main")

	names := entryNames(resp)
	assert.Contains(t, names, "main.go")
	// Other files without "main" in name should not appear
	assert.NotContains(t, names, "README.md")
	assert.NotContains(t, names, "Makefile")
}

func TestSearchFilesDir_HiddenFilesWithDotFilter(t *testing.T) {
	root := setupTestDir(t)
	handler := &SessionsHandler{}

	resp := callSearchFilesDir(t, handler, root, ".", ".git")

	names := entryNames(resp)
	assert.Contains(t, names, ".gitignore")
}

func TestSearchFilesDir_PathTraversalBlocked(t *testing.T) {
	root := setupTestDir(t)
	handler := &SessionsHandler{}

	resp := callSearchFilesDir(t, handler, root, "../../../etc", "")

	assert.Empty(t, resp.Entries, "path traversal should return empty entries")
}

func TestSearchFilesDir_EmptyDirectory(t *testing.T) {
	root := setupTestDir(t)
	handler := &SessionsHandler{}

	resp := callSearchFilesDir(t, handler, root, "empty", "")

	assert.Equal(t, "empty", resp.Dir)
	assert.Empty(t, resp.Entries, "empty directory should return no entries")
}

func TestSearchFilesDir_NonexistentDirectory(t *testing.T) {
	root := setupTestDir(t)
	handler := &SessionsHandler{}

	resp := callSearchFilesDir(t, handler, root, "nonexistent", "")

	assert.Empty(t, resp.Entries)
}

func TestSearchFilesDir_EmptyStringDir(t *testing.T) {
	root := setupTestDir(t)
	handler := &SessionsHandler{}

	// Empty dir should be treated as root
	resp := callSearchFilesDir(t, handler, root, "", "")

	assert.Equal(t, ".", resp.Dir)
	assert.NotEmpty(t, resp.Entries)
}

func TestSearchFilesDir_TypeField(t *testing.T) {
	root := setupTestDir(t)
	handler := &SessionsHandler{}

	resp := callSearchFilesDir(t, handler, root, ".", "")

	types := entryTypes(resp)
	assert.Equal(t, "dir", types["docs/"])
	assert.Equal(t, "dir", types["src/"])
	assert.Equal(t, "file", types["main.go"])
	assert.Equal(t, "file", types["README.md"])
}

// ── isTextFile Tests ────────────────────────────────────────────────

func TestIsTextFile(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		// Text files
		{"main.go", true},
		{"app.js", true},
		{"style.css", true},
		{"config.yaml", true},
		{"README.md", true},
		{"Makefile", true},
		{"Dockerfile", true},
		{".gitignore", true},

		// Binary files
		{"image.png", false},
		{"archive.zip", false},
		{"font.woff2", false},
		{"data.db", false},
		{"binary.exe", false},

		// Extensionless unknown
		{"coral", false},
		{"unknown", false},

		// Edge cases
		{"", false},
		{".hidden", false}, // dot-only file without known extension
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isTextFile(tt.name))
		})
	}
}
