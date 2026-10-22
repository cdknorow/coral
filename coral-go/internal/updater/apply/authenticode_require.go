package apply

import (
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// N4 (Security's note on #162): a signature on the Windows package, required, and the signer pinned down to the whole certificate.
//
// Until Windows releases are signed with Azure Trusted Signing (release.yml's signing path, still off), an unsigned package is accepted on its
// checksums and the Sigstore bundle alone, and the update's history says "unsigned_package". Once they are signed, that way in must close: the Sigstore
// identity and the signing certificate are two keys, and an update should need both. This is the switch, one reviewed line, like apply_switch.go's:
// it stays false until the signing certificate exists and the two pins below are filled in from the first signed MSI. A test fails when it is
// true and either pin is empty, so it cannot be turned on without them.
const RequireSignedWindowsPackage = false

// authenticodeLeafSubjectDER is the leaf certificate's whole subject, pinned as the lower-case hex of its DER encoding (x509.Certificate.RawSubject): every
// attribute, in order, with the string type each is encoded in, so an extra attribute, another order or another encoding of the same text is a different pin. (Not
// a display form such as pkix.Name.String(), which two different subjects can share.) The first signed release prints it: set CORAL_SIGNED_MSI to the signed MSI
// and run "go test -v -run TestSignedMSIMatchesTheUpdatersPins ./internal/updater/apply" on Windows (release.yml runs it after signing, so a mismatch fails the
// release before it is published). Empty means "not pinned yet" and is allowed only while RequireSignedWindowsPackage is false.
var authenticodeLeafSubjectDER = ""

// authenticodePolicyOIDs are the certificate-policy OIDs Azure Trusted Signing puts in the leaf's certificatePolicies extension (dotted form): the leaf
// must carry at least one of them. The same command prints the ones the signed MSI has. Empty means "not pinned yet", under the same rule.
var authenticodePolicyOIDs []string

// errPackageUnsigned is the refusal of a package with no signature once one is required.
var errPackageUnsigned = errors.New("the package is not signed, and Coral's Windows packages are signed")

// checkLeaf judges a signature's leaf certificate by the pins: its whole subject, byte for byte (the hex of its DER), and at least one of the policy OIDs. An empty
// pin is not checked (see the variables above). WinVerifyTrust has already accepted the signature and the chain, and checkSignerPin has checked the chain and the
// O and CN.
func checkLeaf(c *x509.Certificate, subjectDER string, policyOIDs []string) error {
	if subjectDER != "" {
		if hex.EncodeToString(c.RawSubject) != strings.ToLower(subjectDER) {
			// the text is for the person reading the log: c.Subject.String() can look the same for a subject that differs in an attribute or an encoding
			return fmt.Errorf("the signer's certificate subject (%s) is not the pinned one", c.Subject.String())
		}
	}
	if len(policyOIDs) > 0 {
		have := map[string]bool{}
		for _, p := range c.Policies {
			have[p.String()] = true
		}
		for _, p := range c.PolicyIdentifiers { // the same list for the OIDs that fit an asn1.ObjectIdentifier: both are read, so neither setting of x509usepolicies hides one
			have[p.String()] = true
		}
		for _, want := range policyOIDs {
			if have[want] {
				return nil
			}
		}
		return errors.New("the signer's certificate carries none of the pinned certificate policies")
	}
	return nil
}
