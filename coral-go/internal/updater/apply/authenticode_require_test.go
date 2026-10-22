package apply

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/hex"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// N4: a signature on the Windows package, required once releases are signed, and the signer pinned down to the whole certificate.

// While nothing is signed the switch is off and the pins are empty; turning it on needs both pins (a reviewed change of its own).
func TestTheSignatureSwitchIsOffAndCannotBeOnWithoutThePins(t *testing.T) {
	assert.False(t, RequireSignedWindowsPackage, "off until Windows releases are signed with the signing certificate")
	if RequireSignedWindowsPackage {
		assert.NotEmpty(t, authenticodeLeafSubjectDER, "the whole subject must be pinned before a signature is required")
		assert.NotEmpty(t, authenticodePolicyOIDs, "the Trusted Signing policy must be pinned before a signature is required")
	}
}

// certWith makes a certificate with the subject and policies, the way a signer's leaf has them.
func certWith(t *testing.T, name pkix.Name, policies ...string) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: name, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(72 * time.Hour),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning}}
	for _, p := range policies {
		oid, err := x509.ParseOID(p)
		require.NoError(t, err)
		tmpl.Policies = append(tmpl.Policies, oid)
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	c, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return c
}

func subgentic() pkix.Name {
	return pkix.Name{CommonName: "Subgentic LLC", Organization: []string{"Subgentic LLC"}, Locality: []string{"Seattle"}, Province: []string{"Washington"}, Country: []string{"US"}}
}

// The leaf's whole subject and one of the pinned policies: each way to differ is refused, and an empty pin is not checked (not pinned yet).
func TestCheckLeafPinsTheWholeSubjectAndThePolicy(t *testing.T) {
	const policy, other = "1.3.6.1.4.1.311.97.1.0", "1.3.6.1.4.1.311.97.2.0"
	good := certWith(t, subgentic(), policy)
	subject := hex.EncodeToString(good.RawSubject)
	assert.Equal(t, "CN=Subgentic LLC,O=Subgentic LLC,L=Seattle,ST=Washington,C=US", good.Subject.String(), "(the text is for the log; the pin is the DER)")
	assert.NoError(t, checkLeaf(good, subject, []string{policy}), "the pinned subject and policy")
	assert.NoError(t, checkLeaf(good, subject, []string{other, policy}), "one of several pinned policies")
	assert.NoError(t, checkLeaf(good, "", nil), "not pinned yet: nothing more is checked")

	wrongCity := subgentic()
	wrongCity.Locality = []string{"Portland"}
	assert.Error(t, checkLeaf(certWith(t, wrongCity, policy), subject, []string{policy}), "another city: the whole subject is pinned, not O and CN")
	extra := subgentic()
	extra.OrganizationalUnit = []string{"Build"}
	assert.Error(t, checkLeaf(certWith(t, extra, policy), subject, []string{policy}), "an extra RDN")
	lookalike := subgentic()
	lookalike.CommonName = "Subgentic LLC "
	assert.Error(t, checkLeaf(certWith(t, lookalike, policy), subject, []string{policy}), "a lookalike CN")
	assert.Error(t, checkLeaf(certWith(t, subgentic()), subject, []string{policy}), "no policy at all")
	assert.Error(t, checkLeaf(certWith(t, subgentic(), other), subject, []string{policy}), "another policy")
	assert.Error(t, checkLeaf(good, hex.EncodeToString(certWith(t, pkix.Name{CommonName: "Someone Else"}, policy).RawSubject), []string{policy}), "another subject, the right policy")
	assert.NoError(t, checkLeaf(good, strings.ToUpper(subject), []string{policy}), "the pin may be written in capitals")
	assert.Error(t, checkLeaf(good, subject, []string{other}), "the right subject, another policy")
	assert.NoError(t, checkLeaf(certWith(t, subgentic()), subject, nil), "a subject pin alone")
	assert.Error(t, checkLeaf(certWith(t, subgentic(), policy), "", []string{other}), "a policy pin alone")
}

// With a signature required, a package with none is refused before Windows is asked anything; signed ones go on; and the switch off is the old way.
func TestAnUnsignedPackageIsRefusedWhenASignatureIsRequired(t *testing.T) {
	r := newMSIRig(t)
	r.msi.signed = false
	r.cfg.RequireSigned = true
	res := r.run()
	assert.Equal(t, ResultRefused, res.Result)
	assert.Equal(t, "authenticode", res.FailedStage)
	assert.NotContains(t, strings.Join(r.msi.calls, " "), "msiexec")
	assert.Contains(t, r.state().LastFailure.Reason, "not signed")

	r = newMSIRig(t) // signed, required: on
	r.cfg.RequireSigned = true
	assert.Equal(t, ResultRelaunched, r.run().Result)

	r = newMSIRig(t) // unsigned, not required (today): the sums and the bundle, said in the history
	r.msi.signed = false
	assert.Equal(t, ResultRelaunched, r.run().Result)
	assert.Equal(t, "unsigned_package", r.lastHistory().Reason)
}

// The rollback package is the version already installed: it may be one of the unsigned releases, and putting it back is what a failed first signed
// update needs, so a required signature is not asked of it (it still goes through the hold, the checksum and the pin when signed).
func TestTheRollbackPackageNeedNotBeSignedWhenASignatureIsRequired(t *testing.T) {
	r := newMSIRig(t)
	r.cfg.RequireSigned = true
	r.msi.unsignedAt = r.rbPath
	r.msi.healthErr = []error{errors.New("not up")}
	res := r.run()
	assert.Equal(t, ResultRolledBack, res.Result, res.FailedStage)
	assert.Contains(t, r.msi.calls, "msiexec /x")

	r = newMSIRig(t) // a rollback package signed by someone else is still refused
	r.cfg.RequireSigned = true
	r.msi.healthErr = []error{errors.New("not up")}
	r.msi.holdHook = func(path string) {
		if path == r.rbPath {
			r.msi.authErr = errors.New("signed by another publisher")
		}
	}
	res = r.run()
	assert.Equal(t, "rollback_authenticode", res.FailedStage)
}

// attrOf is one subject attribute with its string type: PrintableString is what Go (and most CAs) write for plain ASCII, UTF8String is the other common one.
func attrOf(oid []int, tag int, value string) pkix.AttributeTypeAndValue {
	return pkix.AttributeTypeAndValue{Type: oid, Value: asn1.RawValue{Class: asn1.ClassUniversal, Tag: tag, Bytes: []byte(value)}}
}

// The pin is the subject's DER, not its display text: a subject whose text is identical but whose encoding differs (the CN as a UTF8String instead of a
// PrintableString), or that carries one more attribute, is not the pinned subject (Security's review of N4).
func TestCheckLeafRefusesASubjectThatOnlyLooksTheSame(t *testing.T) {
	const policy = "1.3.6.1.4.1.311.97.1.0"
	good := certWith(t, subgentic(), policy)
	pin := hex.EncodeToString(good.RawSubject)

	// the same attributes in the same order, built from ExtraNames, with the CN as a UTF8String
	asUTF8 := certWith(t, pkix.Name{ExtraNames: []pkix.AttributeTypeAndValue{
		attrOf([]int{2, 5, 4, 6}, asn1.TagPrintableString, "US"),
		attrOf([]int{2, 5, 4, 8}, asn1.TagPrintableString, "Washington"),
		attrOf([]int{2, 5, 4, 7}, asn1.TagPrintableString, "Seattle"),
		attrOf([]int{2, 5, 4, 10}, asn1.TagPrintableString, "Subgentic LLC"),
		attrOf([]int{2, 5, 4, 3}, asn1.TagUTF8String, "Subgentic LLC"),
	}}, policy)
	require.Equal(t, good.Subject.String(), asUTF8.Subject.String(), "the premise: the two subjects read exactly alike")
	require.NotEqual(t, good.RawSubject, asUTF8.RawSubject, "and are encoded differently")
	assert.Error(t, checkLeaf(asUTF8, pin, []string{policy}), "another encoding of the same text")

	// one attribute more, whose text is empty of anything visible in the display form being compared
	withMore := subgentic()
	withMore.ExtraNames = []pkix.AttributeTypeAndValue{attrOf([]int{2, 5, 4, 5}, asn1.TagPrintableString, "1234")} // a serial number attribute
	assert.Error(t, checkLeaf(certWith(t, withMore, policy), pin, []string{policy}), "an extra attribute")
	assert.NoError(t, checkLeaf(good, pin, []string{policy}))
}
