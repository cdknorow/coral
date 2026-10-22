package apply

import (
	"errors"
	"fmt"
	"strings"
)

// The pin for the Windows package's Authenticode signature (#162, the Security Reviewer's and the Orchestrator's point iv). Coral's Windows
// releases are signed with Azure Trusted Signing (release.yml's signing path). Its leaf certificates live about three days and their keys
// change with them, so the pin is the issuing chain Microsoft runs for it and the leaf's subject, never a leaf key:
//
//   - the chain must contain Microsoft ID Verified Code Signing PCA 2021 and Microsoft Identity Verification Root Certificate Authority 2020,
//     by the SHA-256 of their DER encoding (the issuing CAs below the PCA, "Microsoft ID Verified CS EOC/AOC CA 0x", rotate and are not pinned);
//   - the leaf's subject must say O=Subgentic LLC and CN=Subgentic LLC.
//
// The certificates are in authenticode_pins/ with where they were fetched from. A certificate that names Subgentic LLC from any other
// issuer is refused, and so is a valid Trusted Signing certificate of anyone else.

var authenticodePinnedChain = []pinnedCert{
	{Name: "Microsoft ID Verified Code Signing PCA 2021", SHA256: "3d29798cc5d3f0644a7e0dc9cb1cade523ea5ec83b335109b605bfeaa7d5f5c1"},
	{Name: "Microsoft Identity Verification Root Certificate Authority 2020", SHA256: "5367f20c7ade0e2bca790915056d086b720c33c1fa2a2661acf787e3292e1270"},
}

// authenticodeSubject is the publisher the leaf must name, as its O and CN.
const authenticodeSubject = "Subgentic LLC"

type pinnedCert struct{ Name, SHA256 string }

// errUnsigned is a package with no Authenticode signature: it goes by the sums and the bundle alone.
var errUnsigned = errors.New("the package has no Authenticode signature")

// checkSignerPin applies the pin to a signature WinVerifyTrust already accepted: the leaf's organization and common name, and the SHA-256 of
// every certificate of its chain, leaf first.
func checkSignerPin(org, cn string, chainSHA256 []string) error {
	if org != authenticodeSubject || cn != authenticodeSubject {
		return fmt.Errorf("signed by %q (O=%q), not %s", cn, org, authenticodeSubject)
	}
	have := map[string]bool{}
	for _, s := range chainSHA256 {
		have[strings.ToLower(s)] = true
	}
	for _, p := range authenticodePinnedChain {
		if !have[p.SHA256] {
			return fmt.Errorf("the signature's chain doesn't go through %s", p.Name)
		}
	}
	return nil
}
