package testrig

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"fmt"
	"time"

	ct "github.com/google/certificate-transparency-go"
	"github.com/google/certificate-transparency-go/tls"
	ctx509 "github.com/google/certificate-transparency-go/x509"
)

// oidSCTList is the X.509 extension that carries a certificate's embedded signed certificate timestamps (RFC 6962 section 3.3).
var oidSCTList = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 11129, 2, 4, 2}

// sctExtension signs a precertificate timestamp with the fake CT log's key and returns it as the extension to embed in the final certificate. precert is the
// certificate as issued WITHOUT the extension (its TBS is what the log signs, as a real Fulcio does with a precertificate), issuer its issuing CA. A verifier
// that rebuilds the TBS from the final certificate, minus this extension, gets the same bytes. Real Fulcio certificates carry exactly this; the sigstore-go
// test CA this package was copied from does not, so the updater's verifier (which requires an SCT) refused its certificates.
func sctExtension(ctKey *ecdsa.PrivateKey, precert, issuer *x509.Certificate, at time.Time) (pkix.Extension, error) {
	keyDER, err := x509.MarshalPKIXPublicKey(&ctKey.PublicKey)
	if err != nil {
		return pkix.Extension{}, err
	}
	ts := uint64(at.UnixMilli())
	sct := ct.SignedCertificateTimestamp{SCTVersion: ct.V1, LogID: ct.LogID{KeyID: sha256.Sum256(keyDER)}, Timestamp: ts}
	entry := ct.LogEntry{Leaf: ct.MerkleTreeLeaf{
		Version:  ct.V1,
		LeafType: ct.TimestampedEntryLeafType,
		TimestampedEntry: &ct.TimestampedEntry{
			Timestamp: ts,
			EntryType: ct.PrecertLogEntryType,
			PrecertEntry: &ct.PreCert{
				IssuerKeyHash:  sha256.Sum256(issuer.RawSubjectPublicKeyInfo),
				TBSCertificate: precert.RawTBSCertificate,
			},
		},
	}}
	input, err := ct.SerializeSCTSignatureInput(sct, entry)
	if err != nil {
		return pkix.Extension{}, fmt.Errorf("sct input: %w", err)
	}
	digest := sha256.Sum256(input)
	sig, err := ecdsa.SignASN1(rand.Reader, ctKey, digest[:])
	if err != nil {
		return pkix.Extension{}, err
	}
	sct.Signature = ct.DigitallySigned{Algorithm: tls.SignatureAndHashAlgorithm{Hash: tls.SHA256, Signature: tls.ECDSA}, Signature: sig}
	one, err := tls.Marshal(sct)
	if err != nil {
		return pkix.Extension{}, err
	}
	list, err := tls.Marshal(ctx509.SignedCertificateTimestampList{SCTList: []ctx509.SerializedSCT{{Val: one}}})
	if err != nil {
		return pkix.Extension{}, err
	}
	val, err := asn1.Marshal(list)
	if err != nil {
		return pkix.Extension{}, err
	}
	return pkix.Extension{Id: oidSCTList, Value: val}, nil
}
