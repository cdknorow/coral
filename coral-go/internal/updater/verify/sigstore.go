package verify

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	"github.com/sigstore/sigstore-go/pkg/root"
	sgverify "github.com/sigstore/sigstore-go/pkg/verify"
)

// Identity is the signer a bundle must have: an exact SAN and issuer, and the certificate extensions that must equal these when non-empty. It is
// never a pattern: the release signer is built for one exact tag (releaseIdentity).
type Identity struct {
	SAN                 string
	Issuer              string
	SourceRepositoryURI string
	SourceRepositoryRef string
	RunnerEnvironment   string
	BuildTrigger        string
}

// Cert is what a verified certificate says, for the checks that need more than the identity (the release certificate's commit).
type Cert struct {
	SAN                    string
	SourceRepositoryURI    string
	SourceRepositoryRef    string
	SourceRepositoryDigest string
}

// BundleVerifier verifies a Sigstore bundle over an artifact against an identity. Sigstore is the real one; tests stand in for it to make the
// cases a real signer cannot (a forged or expired manifest).
type BundleVerifier interface {
	VerifyBundle(bundleJSON, artifact []byte, id Identity) (Cert, error)
}

// Sigstore verifies with sigstore-go: the signature, the certificate chain to the trusted root, a signed certificate timestamp, the transparency
// log's inclusion proof carried in the bundle (no network), the observer timestamp inside the certificate's validity, then the identity.
type Sigstore struct {
	Trust root.TrustedMaterial
}

// VerifyBundle implements BundleVerifier.
func (s Sigstore) VerifyBundle(bundleJSON, artifact []byte, id Identity) (Cert, error) {
	if s.Trust == nil {
		return Cert{}, errors.New("no trusted root")
	}
	if id.SAN == "" || id.Issuer == "" {
		return Cert{}, errors.New("an identity needs a SAN and an issuer")
	}
	if len(bundleJSON) == 0 || len(bundleJSON) > MaxBundleBytes {
		return Cert{}, fmt.Errorf("bundle size %d is outside 1..%d", len(bundleJSON), MaxBundleBytes)
	}
	var b bundle.Bundle
	if err := b.UnmarshalJSON(bundleJSON); err != nil {
		return Cert{}, fmt.Errorf("bundle: %w", err)
	}
	v, err := sgverify.NewVerifier(s.Trust,
		sgverify.WithSignedCertificateTimestamps(1),
		sgverify.WithTransparencyLog(1),
		sgverify.WithObserverTimestamps(1),
	)
	if err != nil {
		return Cert{}, err
	}
	san, err := sgverify.NewSANMatcher(id.SAN, "")
	if err != nil {
		return Cert{}, err
	}
	iss, err := sgverify.NewIssuerMatcher(id.Issuer, "")
	if err != nil {
		return Cert{}, err
	}
	ci, err := sgverify.NewCertificateIdentity(san, iss, certificate.Extensions{
		SourceRepositoryURI: id.SourceRepositoryURI,
		SourceRepositoryRef: id.SourceRepositoryRef,
		RunnerEnvironment:   id.RunnerEnvironment,
		BuildTrigger:        id.BuildTrigger,
	})
	if err != nil {
		return Cert{}, err
	}
	res, err := v.Verify(&b, sgverify.NewPolicy(sgverify.WithArtifact(bytes.NewReader(artifact)), sgverify.WithCertificateIdentity(ci)))
	if err != nil {
		return Cert{}, err
	}
	if res.Signature == nil || res.Signature.Certificate == nil {
		return Cert{}, errors.New("the bundle has no certificate")
	}
	c := res.Signature.Certificate
	out := Cert{SAN: c.SubjectAlternativeName, SourceRepositoryURI: c.SourceRepositoryURI, SourceRepositoryRef: c.SourceRepositoryRef, SourceRepositoryDigest: c.SourceRepositoryDigest}
	// The library compared them; they are compared again here, so a change in how it treats an empty or partial match cannot loosen a pin.
	if out.SAN != id.SAN || c.Issuer != id.Issuer {
		return Cert{}, fmt.Errorf("identity %q (issuer %q) is not %q (issuer %q)", out.SAN, c.Issuer, id.SAN, id.Issuer)
	}
	for _, p := range [][3]string{
		{"source repository", id.SourceRepositoryURI, c.SourceRepositoryURI},
		{"source ref", id.SourceRepositoryRef, c.SourceRepositoryRef},
		{"runner environment", id.RunnerEnvironment, c.RunnerEnvironment},
		{"trigger", id.BuildTrigger, c.BuildTrigger},
	} {
		if p[1] != "" && p[1] != p[2] {
			return Cert{}, fmt.Errorf("%s %q is not %q", p[0], p[2], p[1])
		}
	}
	return out, nil
}
