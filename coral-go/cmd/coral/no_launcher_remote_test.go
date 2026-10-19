package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// #5124: remote access is off unless the person turns it on. Nothing that starts Coral for them (the tray, the app, launch-coral, the installers,
// the service and cask definitions, the release workflow) passes --remote or sets the server's RemoteAccess option: only cmd/coral takes it from its own
// flag, which a person types.
func TestNoLauncherTurnsRemoteAccessOn(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "coral-go", "go.mod")); err != nil {
		t.Skip("not in the repository checkout")
	}
	flagRe := regexp.MustCompile(`(^|[\s"'\x60,\[])--remote([\s"'\x60=,\]]|$)`)
	optRe := regexp.MustCompile(`\bRemoteAccess\s*:|\.RemoteAccess\s*=`)
	skipDir := map[string]bool{".git": true, "node_modules": true, "specs": true, "agent_docs": true, "site_docs": true, "docs": true, "dist": true}
	scanned := 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			if skipDir[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		ext := filepath.Ext(path)
		switch ext {
		case ".go", ".sh", ".yml", ".yaml", ".wxs", ".rb", ".plist", ".service", ".ps1", ".bat", ".cmd", ".json", ".toml", ".desktop":
		default:
			if d.Name() != "Makefile" {
				return nil
			}
		}
		if strings.HasSuffix(path, "_test.go") || rel == filepath.Join("coral-go", "cmd", "coral", "main.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		scanned++
		s := string(b)
		// In Go, a launcher's argument is the literal "--remote"; the flag in a comment or a log line is prose.
		if (ext == ".go" && strings.Contains(s, `"--remote"`)) || (ext != ".go" && flagRe.MatchString(s)) {
			t.Errorf("%s passes --remote: a launcher must not turn remote access on", rel)
		}
		if ext == ".go" && optRe.MatchString(s) {
			t.Errorf("%s sets the server's RemoteAccess option: only cmd/coral's --remote flag may", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 50 {
		t.Fatalf("scanned only %d files: the walk is not reaching the tree", scanned)
	}
}
