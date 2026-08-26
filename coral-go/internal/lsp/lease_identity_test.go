package lsp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLeaseIsKeyedByDocumentIdentityNotURISpelling guards the acceptance
// criterion "concurrent conflicting edits to one URI are detected rather than
// silently merged".
//
// Manager.Lease validates the URI through URIToPath but stores the lease under
// the raw browser-supplied URI string. Several distinct URI spellings resolve
// to the same file on disk, so two clients can each believe they hold the
// exclusive lease for that document and then push conflicting versioned
// didChange notifications to the same server-side document.
func TestLeaseIsKeyedByDocumentIdentityNotURISpelling(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "x.go")
	if err := os.WriteFile(file, nil, 0644); err != nil {
		t.Fatal(err)
	}
	canonicalURI, err := PathToURI(file)
	if err != nil {
		t.Fatal(err)
	}
	// t.TempDir() may itself sit behind a symlink (/var -> /private/var on macOS).
	canonicalFile, err := Canonical(file)
	if err != nil {
		t.Fatal(err)
	}

	// Every alias below is accepted by URIToPath and resolves to the same file.
	aliases := map[string]string{
		"localhost authority":  strings.Replace(canonicalURI, "file://", "file://localhost", 1),
		"percent-encoded path": strings.Replace(canonicalURI, "x.go", "x%2Ego", 1),
		"dot segment":          strings.Replace(canonicalURI, "/x.go", "/./x.go", 1),
	}

	for name, alias := range aliases {
		t.Run(name, func(t *testing.T) {
			resolved, err := URIToPath(root, alias)
			if err != nil {
				t.Skipf("alias not accepted by URIToPath: %v", err)
			}
			if resolved != canonicalFile {
				t.Skipf("alias resolves to %s, not the same document", resolved)
			}

			instance := &Instance{Workspace: root, Definition: ServerDefinition{ID: "fake"}}
			manager := &Manager{servers: map[string]*managed{
				key(root, "fake"): {instance: instance, clients: 2, leases: map[string]string{}},
			}}

			if err := manager.Lease(instance, canonicalURI, "one"); err != nil {
				t.Fatalf("first client could not take the lease: %v", err)
			}
			if err := manager.Lease(instance, alias, "two"); err == nil {
				t.Fatalf("second client acquired a concurrent lease on %s using an aliased URI (%s); "+
					"conflicting edits to this document would be silently merged", file, alias)
			}
		})
	}
}
