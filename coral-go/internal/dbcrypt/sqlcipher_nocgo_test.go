//go:build sqlcipher && !cgo

package dbcrypt

import (
	"errors"
	"strings"
	"testing"
)

// The sqlcipher tag cannot work without cgo: the build must report encryption
// unavailable and say why, instead of failing later inside the driver stub.
func TestSqlcipherTagWithoutCgoReportsUnavailable(t *testing.T) {
	if FeatureAvailable() {
		t.Fatal("sqlcipher without cgo must not report encryption available")
	}
	err := RequireAvailable()
	if !errors.Is(err, ErrEncryptionUnavailable) || !strings.Contains(err.Error(), "without cgo") {
		t.Fatalf("unclear error: %v", err)
	}
}
