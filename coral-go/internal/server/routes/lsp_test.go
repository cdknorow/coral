package routes

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cdknorow/coral/internal/lsp"
)

func TestValidateResultLocationsRejectsOutsideWorkspace(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "inside.go")
	outside := filepath.Join(t.TempDir(), "outside.go")
	for _, path := range []string{inside, outside} {
		if err := os.WriteFile(path, nil, 0644); err != nil {
			t.Fatal(err)
		}
	}
	insideURI, _ := lsp.PathToURI(inside)
	outsideURI, _ := lsp.PathToURI(outside)
	allowed, _ := json.Marshal(map[string]any{"uri": insideURI})
	if err := validateResultLocations(root, allowed); err != nil {
		t.Fatal(err)
	}
	rejected, _ := json.Marshal([]any{map[string]any{"targetUri": outsideURI}})
	if err := validateResultLocations(root, rejected); err == nil {
		t.Fatal("accepted an out-of-workspace navigation target")
	}
}

func TestValidateRequestURI(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "x.go")
	if err := os.WriteFile(file, nil, 0644); err != nil {
		t.Fatal(err)
	}
	uri, _ := lsp.PathToURI(file)
	params, _ := json.Marshal(map[string]any{"textDocument": map[string]any{"uri": uri}})
	if err := validateRequestURI(root, params); err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeDocumentURIAliases(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "x.go")
	if err := os.WriteFile(file, nil, 0644); err != nil {
		t.Fatal(err)
	}
	canonical, _ := lsp.PathToURI(file)
	alias := strings.Replace(canonical, "file://", "file://localhost", 1)
	params, _ := json.Marshal(map[string]any{
		"textDocument": map[string]any{"uri": alias, "version": 1},
	})
	normalized, err := normalizeDocumentURI(root, params)
	if err != nil {
		t.Fatal(err)
	}
	if got := documentURI(normalized); got != canonical {
		t.Fatalf("normalized URI = %q, want %q", got, canonical)
	}
}

func TestLSPDocumentAndNavigationLimits(t *testing.T) {
	params, _ := json.Marshal(map[string]any{
		"textDocument": map[string]any{"text": string(make([]byte, maxOpenDocument+1))},
	})
	if got := documentContentSize(params); got != maxOpenDocument+1 {
		t.Fatalf("document size = %d", got)
	}

	root := t.TempDir()
	file := filepath.Join(root, "x.go")
	if err := os.WriteFile(file, nil, 0644); err != nil {
		t.Fatal(err)
	}
	uri, _ := lsp.PathToURI(file)
	locations := make([]any, maxNavigationResults+1)
	for i := range locations {
		locations[i] = map[string]any{"uri": uri}
	}
	result, _ := json.Marshal(locations)
	if err := validateResultLocations(root, result); err == nil {
		t.Fatal("accepted excessive navigation results")
	}
}

func TestBrowserRequestKeysPreserveType(t *testing.T) {
	if browserRequestKey(12) == browserRequestKey("12") {
		t.Fatal("numeric and string request IDs collided")
	}
	if browserRequestKey(nil) != "" {
		t.Fatal("nil request ID was accepted")
	}
}
