package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subgentic/coral/internal/auth"
)

// #3949: --stdin reads one line (one trailing newline dropped); an empty one is refused.
func TestReadNewPasswordFromStdin(t *testing.T) {
	pw, err := readNewPassword(strings.NewReader("s3cret with spaces\n"), true, &bytes.Buffer{})
	if err != nil || pw != "s3cret with spaces" {
		t.Fatalf("got %q, %v", pw, err)
	}
	if _, err := readNewPassword(strings.NewReader("\n"), true, &bytes.Buffer{}); err == nil {
		t.Error("an empty password was accepted")
	}
}

// With Coral stopped, reset-password writes the data folder and marks every session for ending.
func TestResetPasswordOffline(t *testing.T) {
	dir := t.TempDir()
	if _, err := auth.NewKeyStore(dir); err != nil { // makes api_key: a real data folder
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := runResetPassword([]string{"--stdin", "--home", dir, "--url", "http://127.0.0.1:1"}, strings.NewReader("new-one\n"), &out, &errOut); code != 0 {
		t.Fatalf("code %d: %s", code, errOut.String())
	}
	phc, err := auth.ReadPasswordHash(dir)
	if err != nil || !auth.VerifyPassword(phc, "new-one") {
		t.Fatalf("password not set: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, auth.SessionsRevokedFile)); err != nil {
		t.Errorf("no revoke marker: %v", err)
	}
	if code := runResetPassword([]string{"--clear", "--home", dir, "--url", "http://127.0.0.1:1"}, nil, &out, &errOut); code != 0 {
		t.Fatalf("clear: code %d: %s", code, errOut.String())
	}
	if _, err := auth.ReadPasswordHash(dir); err == nil {
		t.Error("--clear left a password")
	}
	// Not a data folder: refused.
	if code := runResetPassword([]string{"--clear", "--home", t.TempDir(), "--url", "http://127.0.0.1:1"}, nil, &out, &errOut); code != 2 {
		t.Errorf("an empty folder: code %d", code)
	}
}
