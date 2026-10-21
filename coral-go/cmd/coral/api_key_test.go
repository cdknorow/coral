package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subgentic/coral/internal/auth"
)

// #3936: coral api-key prints the key the server made, and says how to get one when there is
// none; it never makes one itself.
func TestAPIKeyPrintsTheServersKey(t *testing.T) {
	dir := t.TempDir()
	var out, errOut bytes.Buffer
	if code := runAPIKey([]string{"--home", dir}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "no API key yet") {
		t.Fatalf("no key: code %d, %q", code, errOut.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "api_key")); !os.IsNotExist(err) {
		t.Fatalf("coral api-key made a key file: %v", err)
	}
	ks, err := auth.NewKeyStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errOut.Reset()
	if code := runAPIKey([]string{"--home", dir}, &out, &errOut); code != 0 {
		t.Fatalf("code %d: %s", code, errOut.String())
	}
	if got := strings.TrimSpace(out.String()); got != ks.Key() || got == "" {
		t.Errorf("printed %q, want the server's key", got)
	}
	if code := runAPIKey([]string{"extra"}, &out, &errOut); code != 2 {
		t.Errorf("an extra argument: code %d", code)
	}
}
