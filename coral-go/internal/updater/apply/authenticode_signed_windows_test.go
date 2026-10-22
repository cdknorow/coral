//go:build windows

package apply

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The signed release against the updater's own pins (N4). release.yml runs it right after the signing step (CORAL_SIGNED_MSI is the signed MSI), so a
// release whose signature the updater would refuse fails before it is published. It also prints the values to pin: the leaf's whole subject and its
// certificate policies, the way the pins are written (authenticode_require.go), so the first signed release says what to put there.
func TestSignedMSIMatchesTheUpdatersPins(t *testing.T) {
	path := os.Getenv("CORAL_SIGNED_MSI")
	if path == "" {
		t.Skip("CORAL_SIGNED_MSI names a signed MSI to check")
	}
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	sum := sha256.Sum256(b)
	w := &WindowsMSI{DataDir: t.TempDir()}
	// Hold wants the file under the updates folder of its data folder: a copy there.
	staged := w.DataDir + `\updates\signed.msi`
	require.NoError(t, os.MkdirAll(w.DataDir+`\updates`, 0o700))
	require.NoError(t, os.WriteFile(staged, b, 0o600))
	h, err := w.Hold(staged, hex.EncodeToString(sum[:]))
	require.NoError(t, err)
	defer h.Close()

	signed, sg, err := authenticodeSigner(h.(*heldFile))
	require.NoError(t, err, "the signature must verify (WinVerifyTrust, with revocation)")
	require.True(t, signed, "the release's MSI is not signed")
	c, perr := x509.ParseCertificate(sg.der)
	require.NoError(t, perr)
	var policies []string
	for _, p := range c.Policies {
		policies = append(policies, p.String())
	}
	t.Logf("leaf subject DER, hex (for authenticodeLeafSubjectDER): %s", hex.EncodeToString(c.RawSubject))
	t.Logf("leaf subject, for reading: %s", c.Subject.String())
	t.Logf("leaf policies (for authenticodePolicyOIDs): %s", strings.Join(policies, ", "))
	t.Logf("leaf O=%q CN=%q; chain SHA-256s: %s", sg.org, sg.cn, strings.Join(sg.chainSHA256, " "))

	assert.NoError(t, judgeSigner(sg), "the updater would refuse this release's signature")
	if authenticodeLeafSubjectDER == "" || len(authenticodePolicyOIDs) == 0 {
		t.Log("the leaf's whole subject and policy are not pinned yet: pin the two values above before a signature is required")
	}
}
