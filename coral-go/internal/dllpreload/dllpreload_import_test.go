package dllpreload

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The program that shows the webview imports this package (for its init). Taking the import out would silently bring the bare-name lookups back; this runs on every OS, since
// coral-app itself builds only where its toolchain is.
func TestCoralAppImportsThisPackage(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "cmd", "coral-app", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), `_ "github.com/subgentic/coral/internal/dllpreload"`) {
		t.Fatal("cmd/coral-app/main.go no longer imports internal/dllpreload for its side effect")
	}
}
