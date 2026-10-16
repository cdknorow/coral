package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #4004: a refused or failed upload returns an error and prints no coral:// link, so a caller
// that misses the exit code can't mistake a refusal for success (the bug that posted dead links).
func TestUploadArtifactRefusalIsAnError(t *testing.T) {
	file := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name       string
		status     int
		retryAfter string
		body       string
		wantInErr  []string
	}{
		{"429 with Retry-After and a reason", http.StatusTooManyRequests, "60",
			`{"error":"hourly upload limit reached","code":"artifact_quota","limit":"hourly"}`,
			[]string{"rate limited", "hourly upload limit reached", "Retry-After: 60"}},
		{"429 with no body", http.StatusTooManyRequests, "30", "",
			[]string{"rate limited", "Retry-After: 30"}},
		{"500 with a plain body", http.StatusInternalServerError, "", "something broke",
			[]string{"HTTP 500", "something broke"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.retryAfter != "" {
					w.Header().Set("Retry-After", tc.retryAfter)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			var out, errOut bytes.Buffer
			err := uploadArtifact(srv.URL, file, "note.txt", "text/plain", "sess-1", &out, &errOut)
			if err == nil {
				t.Fatalf("a %d answer must be an error, got nil (out=%q)", tc.status, out.String())
			}
			if out.Len() != 0 {
				t.Errorf("no coral:// link may be printed on a refusal, got stdout=%q", out.String())
			}
			if strings.Contains(out.String(), "coral://") {
				t.Errorf("stdout must not contain a coral:// link: %q", out.String())
			}
			for _, want := range tc.wantInErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q must contain %q", err.Error(), want)
				}
			}
		})
	}
}

// #4004 (b): a 2xx reply that carries no artifact id (a success-shaped refusal) is a failure too,
// so the exit code is the only thing a caller can get wrong. No coral:// link is printed.
func TestUploadArtifactTwoXXWithoutIdIsAFailure(t *testing.T) {
	file := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, body string }{
		{"200 with an error body", `{"error":"hourly upload limit reached"}`},
		{"201 with an empty object", `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			var out, errOut bytes.Buffer
			err := uploadArtifact(srv.URL, file, "note.txt", "text/plain", "sess-1", &out, &errOut)
			if err == nil {
				t.Fatalf("a 2xx with no artifact id must be an error, got nil (out=%q)", out.String())
			}
			if strings.Contains(out.String(), "coral://") || out.Len() != 0 {
				t.Errorf("no link may be printed, got stdout=%q", out.String())
			}
		})
	}
}

// A 2xx upload prints the server's reply (the coral:// link) to stdout and returns no error.
func TestUploadArtifactSuccessPrintsTheLink(t *testing.T) {
	file := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	const reply = `{"uri":"coral://artifacts/abc123","name":"note.txt"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Artifact-Name") != "note.txt" {
			t.Errorf("name header not forwarded: %q", r.Header.Get("X-Artifact-Name"))
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(reply))
	}))
	defer srv.Close()

	var out, errOut bytes.Buffer
	if err := uploadArtifact(srv.URL, file, "note.txt", "text/plain", "sess-1", &out, &errOut); err != nil {
		t.Fatalf("a 2xx upload must succeed: %v (stderr=%q)", err, errOut.String())
	}
	if !strings.Contains(out.String(), "coral://artifacts/abc123") {
		t.Errorf("the coral:// link must be printed to stdout, got %q", out.String())
	}
}
