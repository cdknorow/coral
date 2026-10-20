package testrig

import (
	"crypto/x509/pkix"
	"encoding/asn1"
	"errors"
	"fmt"
	"sync"
	"time"

	protobundle "github.com/sigstore/protobuf-specs/gen/pb-go/bundle/v1"
	protocommon "github.com/sigstore/protobuf-specs/gen/pb-go/common/v1"
	"github.com/sigstore/sigstore-go/pkg/root"
	"google.golang.org/protobuf/encoding/protojson"
)

// Signer is a fake Sigstore for the update rehearsal (#5401): a fake Fulcio CA, CT log, Rekor log and timestamp authority whose keys are generated when it is
// made (every run has its own; none is kept or committed), and the Sigstore bundles it signs. Trust is the trusted material to verify them with.
type Signer struct {
	mu sync.Mutex
	vs *VirtualSigstore
}

// NewSigner makes a fake Sigstore with fresh keys.
func NewSigner() (*Signer, error) {
	vs, err := NewVirtualSigstore()
	if err != nil {
		return nil, err
	}
	return &Signer{vs: vs}, nil
}

// Trust is what a verifier trusts: this fake Sigstore's CA, CT log, Rekor log and timestamp authority, and nothing else.
func (s *Signer) Trust() root.TrustedMaterial { return s.vs }

// CertID is what a signing certificate says, as a Fulcio certificate for a GitHub Actions workflow does. SAN is the workflow's URI.
type CertID struct {
	SAN, Issuer                               string
	SourceRepositoryURI, SourceRepositoryRef  string
	SourceRepositoryDigest, RunnerEnvironment string
	BuildTrigger                              string
}

// Omit says which parts of a real Sigstore bundle to leave out, for the cases a strict verifier must refuse.
type Omit struct {
	SCT              bool      // the certificate carries no signed certificate timestamp
	TransparencyLog  bool      // no transparency log entry in the bundle
	TimestampServer  bool      // no RFC 3161 timestamp
	InclusionPromise bool      // the log entry has not the log's signed promise (its integrated time)
	IntegratedAt     time.Time // when the log says it recorded the signature (zero: now); a time outside the certificate's ten minutes is a case to refuse
}

func utf8Ext(last int, v string) (pkix.Extension, error) {
	if v == "" {
		return pkix.Extension{}, errors.New("empty")
	}
	der, err := asn1.MarshalWithParams(v, "utf8")
	if err != nil {
		return pkix.Extension{}, err
	}
	return pkix.Extension{Id: asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, last}, Value: der}, nil
}

// Bundle signs artifact as id and returns the Sigstore bundle (JSON).
func (s *Signer) Bundle(artifact []byte, id CertID, omit Omit) ([]byte, error) {
	var ext []pkix.Extension
	for _, e := range []struct {
		oid int
		v   string
	}{{11, id.RunnerEnvironment}, {12, id.SourceRepositoryURI}, {13, id.SourceRepositoryDigest}, {14, id.SourceRepositoryRef}, {20, id.BuildTrigger}} {
		if e.v == "" {
			continue
		}
		x, err := utf8Ext(e.oid, e.v)
		if err != nil {
			return nil, err
		}
		ext = append(ext, x)
	}
	s.mu.Lock()
	s.vs.Extra, s.vs.NoSCT = ext, omit.SCT
	var entity *TestEntity
	var err error
	if omit.IntegratedAt.IsZero() {
		entity, err = s.vs.Sign(id.SAN, id.Issuer, artifact)
	} else {
		entity, err = s.vs.SignAtTime(id.SAN, id.Issuer, artifact, omit.IntegratedAt)
	}
	s.vs.Extra, s.vs.NoSCT = nil, false
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	ms := entity.messageSignature
	alg, ok := protocommon.HashAlgorithm_value[ms.DigestAlgorithm()]
	if !ok {
		return nil, fmt.Errorf("digest algorithm %q", ms.DigestAlgorithm())
	}
	pb := &protobundle.Bundle{
		MediaType: "application/vnd.dev.sigstore.bundle+json;version=0.1", // v0.1 takes the log's signed promise; the v0.3 form needs an inclusion proof the test CA does not make for a message signature
		VerificationMaterial: &protobundle.VerificationMaterial{
			Content: &protobundle.VerificationMaterial_X509CertificateChain{X509CertificateChain: &protocommon.X509CertificateChain{Certificates: []*protocommon.X509Certificate{{RawBytes: entity.certChain[0].Raw}}}},
		},
		Content: &protobundle.Bundle_MessageSignature{MessageSignature: &protocommon.MessageSignature{
			MessageDigest: &protocommon.HashOutput{Algorithm: protocommon.HashAlgorithm(alg), Digest: ms.Digest()},
			Signature:     ms.Signature(),
		}},
	}
	if !omit.TransparencyLog {
		for _, en := range entity.tlogEntries {
			te := en.TransparencyLogEntry()
			if omit.InclusionPromise {
				te.InclusionPromise = nil
			}
			pb.VerificationMaterial.TlogEntries = append(pb.VerificationMaterial.TlogEntries, te)
		}
	}
	if !omit.TimestampServer {
		tv := &protobundle.TimestampVerificationData{}
		for _, ts := range entity.timestamps {
			tv.Rfc3161Timestamps = append(tv.Rfc3161Timestamps, &protocommon.RFC3161SignedTimestamp{SignedTimestamp: ts})
		}
		pb.VerificationMaterial.TimestampVerificationData = tv
	}
	return protojson.Marshal(pb)
}
