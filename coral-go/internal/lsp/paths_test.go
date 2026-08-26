package lsp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRegistrySelection(t *testing.T) {
	def, ok := DefaultRegistry().ForFile("hello.GO")
	if !ok || def.ID != "gopls" {
		t.Fatalf("got %#v, %v", def, ok)
	}
	if _, ok := DefaultRegistry().ForFile("hello.py"); ok {
		t.Fatal("unexpected server for unsupported file")
	}
}

func TestResolveWorkspacePrecedenceAndNearest(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "module", "pkg")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{filepath.Join(root, "go.work"), filepath.Join(root, "module", "go.mod")} {
		if err := os.WriteFile(name, nil, 0644); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(nested, "x.go")
	if err := os.WriteFile(file, nil, 0644); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveWorkspace(root, file, []string{"go.work", "go.mod"})
	canonicalRoot, _ := Canonical(root)
	if err != nil || got != canonicalRoot {
		t.Fatalf("go.work precedence: got %q, %v", got, err)
	}
	got, err = ResolveWorkspace(root, file, []string{"go.mod"})
	canonicalModule, _ := Canonical(filepath.Join(root, "module"))
	if err != nil || got != canonicalModule {
		t.Fatalf("nearest go.mod: got %q, %v", got, err)
	}
}

func TestURIWorkspaceBoundary(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "space ü.go")
	if err := os.WriteFile(inside, nil, 0644); err != nil {
		t.Fatal(err)
	}
	uri, err := PathToURI(inside)
	if err != nil {
		t.Fatal(err)
	}
	got, err := URIToPath(root, uri)
	canonicalInside, _ := Canonical(inside)
	if err != nil || got != canonicalInside {
		t.Fatalf("roundtrip got %q, %v", got, err)
	}
	outside := filepath.Join(t.TempDir(), "x.go")
	if err := os.WriteFile(outside, nil, 0644); err != nil {
		t.Fatal(err)
	}
	outsideURI, _ := PathToURI(outside)
	if _, err := URIToPath(root, outsideURI); err == nil {
		t.Fatal("accepted outside path")
	}
}

func TestResolveFileRejectsWorkspaceRootAndDirectory(t *testing.T) {
	root := t.TempDir()
	if _, err := ResolveFile(root, "."); err == nil {
		t.Fatal("accepted workspace root as a document")
	}
	if err := os.Mkdir(filepath.Join(root, "dir"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveFile(root, "dir"); err == nil {
		t.Fatal("accepted directory as a document")
	}
}
