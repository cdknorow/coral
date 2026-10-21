package testrig

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/updater/verify"
)

func idOf(c CertID) verify.Identity {
	return verify.Identity{SAN: c.SAN, Issuer: c.Issuer, SourceRepositoryURI: c.SourceRepositoryURI, SourceRepositoryRef: c.SourceRepositoryRef,
		RunnerEnvironment: c.RunnerEnvironment, BuildTrigger: c.BuildTrigger, SourceRepositoryID: c.SourceRepositoryID}
}

// The real verifier (verify.Sigstore, the one coral-update links, with the same options: an SCT, a transparency log entry and an observer timestamp
// are each required) accepts a bundle from the fake Sigstore, and refuses each of the ways a bundle can be wrong.
func TestTheRealVerifierAcceptsTheFakeSigstoreAndRefusesWhatIsMissingOrWrong(t *testing.T) {
	s, err := NewSigner()
	require.NoError(t, err)
	v := verify.Sigstore{Trust: s.Trust()}
	art := []byte("SHA256SUMS of a release")
	cert := ReleaseCert("v1.4.131")
	good, err := s.Bundle(art, cert, Omit{})
	require.NoError(t, err)

	got, err := v.VerifyBundle(good, art, idOf(cert))
	require.NoError(t, err, "a bundle signed as the release workflow, under a root the verifier trusts")
	assert.Equal(t, cert.SAN, got.SAN)
	assert.Equal(t, FakeCommit, got.SourceRepositoryDigest)

	// each required part of a bundle: leaving it out is a refusal
	for name, omit := range map[string]Omit{
		"no signed certificate timestamp":                     {SCT: true},
		"no transparency log entry":                           {TransparencyLog: true},
		"no promise from the log":                             {InclusionPromise: true},
		"the only time is outside the certificate's validity": {TimestampServer: true, IntegratedAt: time.Now().Add(2 * time.Hour)},
	} {
		b, err := s.Bundle(art, cert, omit)
		require.NoError(t, err, name)
		_, err = v.VerifyBundle(b, art, idOf(cert))
		assert.Error(t, err, name)
	}
	// the log's signed promise carries its time, so a timestamp server is not also needed
	b, err := s.Bundle(art, cert, Omit{TimestampServer: true})
	require.NoError(t, err)
	_, err = v.VerifyBundle(b, art, idOf(cert))
	assert.NoError(t, err, "no timestamp server: the log's promise is the time")

	// each pin: another signer is refused
	wrong := map[string]func(c *CertID){
		"another tag":                func(c *CertID) { *c = ReleaseCert("v1.4.99") },
		"another workflow":           func(c *CertID) { c.SAN = strings.Replace(c.SAN, "release.yml", "other.yml", 1) },
		"another repository":         func(c *CertID) { c.SourceRepositoryURI = "https://github.com/other.invalid/coral" },
		"another ref":                func(c *CertID) { c.SourceRepositoryRef = "refs/heads/main" },
		"a hosted-by-someone runner": func(c *CertID) { c.RunnerEnvironment = "self-hosted" },
		"another trigger":            func(c *CertID) { c.BuildTrigger = "workflow_dispatch" },
		"another repository id":      func(c *CertID) { c.SourceRepositoryID = "9000099" }, // the re-created name (#5536)
		"another OIDC issuer":        func(c *CertID) { c.Issuer = "https://oidc.other.invalid" },
	}
	for name, mod := range wrong {
		c := cert
		mod(&c)
		b, err := s.Bundle(art, c, Omit{})
		require.NoError(t, err, name)
		_, err = v.VerifyBundle(b, art, idOf(cert))
		assert.Error(t, err, name)
	}
	// another artifact, and another Sigstore (its keys are its own: nothing it signs is trusted here)
	_, err = v.VerifyBundle(good, []byte("something else"), idOf(cert))
	assert.Error(t, err, "a bundle for another artifact")
	other, err := NewSigner()
	require.NoError(t, err)
	b2, err := other.Bundle(art, cert, Omit{})
	require.NoError(t, err)
	_, err = v.VerifyBundle(b2, art, idOf(cert))
	assert.Error(t, err, "a bundle from a Sigstore this verifier does not trust")
}
