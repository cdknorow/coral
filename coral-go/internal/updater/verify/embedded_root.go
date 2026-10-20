package verify

import (
	_ "embed"
	"fmt"

	"github.com/sigstore/sigstore-go/pkg/root"
)

// embeddedRoot is a snapshot of the public Sigstore trusted root (the same file the tests verify the real v1.4.120 release against). It is what
// coral-update trusts until the TUF refresh lands: a release signed under keys the snapshot lacks is refused (fail closed), never skipped.
//
//go:embed trusted_root.json
var embeddedRoot []byte

// EmbeddedTrust is the embedded snapshot as trusted material.
func EmbeddedTrust() (root.TrustedMaterial, error) {
	tr, err := root.NewTrustedRootFromJSON(embeddedRoot)
	if err != nil {
		return nil, fmt.Errorf("the embedded trusted root: %w", err)
	}
	return tr, nil
}
