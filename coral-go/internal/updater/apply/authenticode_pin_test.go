package apply

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #162 (iv): a signature passes only with the leaf naming Subgentic LLC (O and CN) AND a chain through Microsoft Trusted Signing's PCA and
// root. A certificate that names Subgentic LLC from another issuer is refused; a Trusted Signing certificate of anyone else is refused.
func TestAuthenticodePinNeedsTheSubjectAndTheTrustedSigningChain(t *testing.T) {
	leaf, issuing := "aa"+authenticodePinnedChain[0].SHA256[2:], "bb"+authenticodePinnedChain[1].SHA256[2:]
	trustedChain := []string{leaf, issuing, authenticodePinnedChain[0].SHA256, authenticodePinnedChain[1].SHA256}
	assert.NoError(t, checkSignerPin("Subgentic LLC", "Subgentic LLC", trustedChain), "Subgentic LLC through Trusted Signing")

	otherIssuer := []string{leaf, "cc" + leaf[2:], "dd" + leaf[2:]}
	assert.Error(t, checkSignerPin("Subgentic LLC", "Subgentic LLC", otherIssuer), "the right name from another CA")
	assert.Error(t, checkSignerPin("Subgentic LLC", "Subgentic LLC", []string{leaf, authenticodePinnedChain[1].SHA256}), "the root without the PCA")
	assert.Error(t, checkSignerPin("Someone Else LLC", "Someone Else LLC", trustedChain), "another Trusted Signing customer")
	assert.Error(t, checkSignerPin("Subgentic LLC", "Subgentic", trustedChain), "the CN must be exact")
	assert.Error(t, checkSignerPin("", "Subgentic LLC", trustedChain), "the O must be there")
}

// The pinned hashes are the SHA-256 of the certificates recorded in authenticode_pins/ (with where they came from in its README).
func TestAuthenticodePinsAreTheRecordedCertificates(t *testing.T) {
	files := map[string]string{
		"Microsoft ID Verified Code Signing PCA 2021":                     "Microsoft_ID_Verified_Code_Signing_PCA_2021.crt",
		"Microsoft Identity Verification Root Certificate Authority 2020": "Microsoft_Identity_Verification_Root_Certificate_Authority_2020.crt",
	}
	require.Len(t, authenticodePinnedChain, len(files))
	for _, p := range authenticodePinnedChain {
		b, err := os.ReadFile(filepath.Join("authenticode_pins", files[p.Name]))
		require.NoError(t, err, p.Name)
		s := sha256.Sum256(b)
		assert.Equal(t, hex.EncodeToString(s[:]), p.SHA256, p.Name)
	}
}

// #162 D: the MSI is one file. tools/coral.wxs embeds its cabinet (MediaTemplate EmbedCab="yes"); a package with an external cab beside it
// would let the cab be swapped next to the held MSI, so this fails if that changes: also on any EmbedCab="no" or Cabinet= (a Media element
// naming an external cab), wherever it is (QA N2).
func TestTheWindowsPackageEmbedsItsCabinet(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "tools", "coral.wxs"))
	if err != nil {
		t.Skip("tools/coral.wxs is not in this checkout")
	}
	assert.Contains(t, string(b), `<MediaTemplate EmbedCab="yes" />`)
	assert.NotRegexp(t, `(?i)\bEmbedCab\s*=\s*["']\s*no`, string(b), "no cabinet left outside the MSI")
	assert.NotRegexp(t, `(?i)\bCabinet\s*=`, string(b), "no external cabinet named")
}
