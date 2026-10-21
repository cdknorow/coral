package license

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	terms "github.com/subgentic/coral/pkg/license"
)

// markerAt points the acceptance marker at a temporary file for one test.
func markerAt(t *testing.T) string {
	t.Helper()
	marker := filepath.Join(t.TempDir(), ".eula-accepted")
	old := eulaMarkerPath
	eulaMarkerPath = func() string { return marker }
	t.Cleanup(func() { eulaMarkerPath = old })
	t.Setenv("CORAL_DATA_DIR", t.TempDir())
	return marker
}

func writeMarker(t *testing.T, path, content string) {
	t.Helper()
	require(t, os.WriteFile(path, []byte(content), 0o644))
}

func require(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// The marker records the version AND the digest of the terms accepted, and
// how. Acceptance means both match this build's terms.
func TestEULAMarkerRecordsVersionAndDigest(t *testing.T) {
	marker := markerAt(t)
	if EULAText != terms.EULAText || EULAVersion != terms.EULAVersion {
		t.Fatal("internal/license must show pkg/license's terms")
	}
	if IsEULAAccepted() {
		t.Fatal("no marker: not accepted")
	}
	require(t, WriteEULAMarker())
	data, _ := os.ReadFile(marker)
	want := "accepted\nversion=" + EULAVersion + "\nsha256=" + terms.EULADigest() + "\nsource=dialog\n"
	if string(data) != want {
		t.Fatalf("marker = %q, want %q", data, want)
	}
	if !IsEULAAccepted() {
		t.Fatal("accepted")
	}

	// Other terms are not acceptance of these: another version, or this
	// version with another text's digest.
	writeMarker(t, marker, "accepted\nversion=2025-01-01\nsha256="+terms.EULADigest()+"\n")
	if IsEULAAccepted() {
		t.Fatal("another version is not acceptance")
	}
	writeMarker(t, marker, "accepted\nversion="+EULAVersion+"\nsha256=0000\n")
	if IsEULAAccepted() {
		t.Fatal("another text's digest is not acceptance")
	}
	writeMarker(t, marker, "accepted\nversion="+EULAVersion+"\n")
	if IsEULAAccepted() {
		t.Fatal("a version without a digest is not acceptance")
	}
}

// A legacy marker ("accepted", from before versions; also the Windows
// installer's "accepted \r\n") is rewritten at the next launch as acceptance of
// the current version, with its digest, source=legacy and the rewrite time,
// without a prompt. It is rewritten once.
func TestLegacyEULAMarkerIsRewritten(t *testing.T) {
	when := time.Date(2026, 10, 14, 9, 30, 0, 0, time.UTC)
	old := eulaNow
	eulaNow = func() time.Time { return when }
	t.Cleanup(func() { eulaNow = old })
	for _, legacy := range []string{"accepted\n", "accepted \r\n", ""} {
		marker := markerAt(t)
		writeMarker(t, marker, legacy)
		prompted := false
		if !CheckAndPromptEULA(func(string) bool { prompted = true; return false }) || prompted {
			t.Fatalf("%q: accepted without a prompt", legacy)
		}
		data, _ := os.ReadFile(marker)
		want := "accepted\nversion=" + EULAVersion + "\nsha256=" + terms.EULADigest() + "\nsource=legacy\nrewritten_at=2026-10-14T09:30:00Z\n"
		if string(data) != want {
			t.Fatalf("%q rewritten as %q, want %q", legacy, data, want)
		}
		eulaNow = func() time.Time { return when.Add(time.Hour) }
		MigrateLegacyEULAMarker()
		again, _ := os.ReadFile(marker)
		if !bytes.Equal(again, data) {
			t.Fatal("a rewritten marker is not rewritten again")
		}
		eulaNow = func() time.Time { return when }
	}
}

// A legacy marker that cannot be rewritten (read-only) keeps working as it
// did, and that is logged: a launch never blocks on it.
func TestLegacyEULAMarkerReadOnlyStillAccepted(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	marker := markerAt(t)
	writeMarker(t, marker, "accepted\n")
	require(t, os.Chmod(marker, 0o444))
	require(t, os.Chmod(filepath.Dir(marker), 0o555))
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(marker), 0o755) })
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	if !CheckAndPromptEULA(func(string) bool { t.Fatal("no prompt"); return false }) {
		t.Fatal("still accepted")
	}
	if data, _ := os.ReadFile(marker); string(data) != "accepted\n" {
		t.Fatalf("marker = %q", data)
	}
	if !strings.Contains(logs.String(), "legacy acceptance marker kept as is") {
		t.Fatalf("logged: %s", logs.String())
	}
}

// The Hub's terms acceptance is recorded only for exactly this binary's
// version; a local marker never stands in for it.
func TestRecordHubTermsAcceptance(t *testing.T) {
	marker := markerAt(t)
	if err := RecordHubTermsAcceptance("2025-01-01"); err == nil {
		t.Fatal("another version is refused")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("nothing written for a mismatch")
	}
	require(t, RecordHubTermsAcceptance(EULAVersion))
	data, _ := os.ReadFile(marker)
	if !strings.Contains(string(data), "source=hub\n") || !IsEULAAccepted() {
		t.Fatalf("marker = %q", data)
	}
}

// The Windows installer shows tools/license.rtf, then writes the marker: its
// text must be EULAText (after RTF markup and whitespace), or the marker would
// claim terms the user never saw.
func TestInstallerTermsMatchEULAText(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "tools", "license.rtf"))
	if err != nil {
		t.Fatalf("the installer's terms (tools/license.rtf): %v", err)
	}
	got, want := normalizeTerms(rtfPlainText(string(raw))), normalizeTerms(EULAText)
	if got != want {
		t.Fatalf("tools/license.rtf differs from EULAText: update both together (and EULAVersion)\nrtf:  %q\neula: %q", got, want)
	}
}

var (
	rtfGroupRe   = regexp.MustCompile(`\{\\\*[^{}]*\}|\{\\fonttbl\{[^}]*\}\}`)
	rtfPardRe    = regexp.MustCompile(`\\pard\b`)
	rtfControlRe = regexp.MustCompile(`\\[a-z]+-?\d*`)
	spaceRe      = regexp.MustCompile(`\s+`)
)

// rtfPlainText strips the WiX license RTF to its text.
func rtfPlainText(s string) string {
	s = rtfGroupRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, `\emdash`, "—")
	s = rtfPardRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, `\par`, "\n")
	s = rtfControlRe.ReplaceAllString(s, "")
	return strings.NewReplacer("{", "", "}", "").Replace(s)
}

// normalizeTerms compares texts by their non-empty lines with whitespace collapsed.
func normalizeTerms(s string) string {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(spaceRe.ReplaceAllString(l, " ")); l != "" {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, "\n")
}
