package license

import (
	"regexp"
	"testing"
)

// The version names exactly this text: change the terms, and this fails until
// EULAVersion is set to the day of the change and the digest below updated.
func TestEULAVersionMatchesText(t *testing.T) {
	const (
		version = "2026-03-26"
		digest  = "c1997b07a78b0d69b607e66129caeb0738bd73fce178a739d9e3d515b47e13af"
	)
	if got := EULADigest(); got != digest || EULAVersion != version {
		t.Fatalf("the terms changed (sha256 %s, version %s): set EULAVersion to today's date (YYYY-MM-DD) and update this test's version and digest", got, EULAVersion)
	}
	if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`).MatchString(EULAVersion) {
		t.Fatalf("EULAVersion %q is not a date", EULAVersion)
	}
}
