package verify

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

// Manifest is the promote manifest (promote.json, schema 1): what the promote workflow approved. Every field is required; an unknown one is refused.
type Manifest struct {
	Schema          int      `json:"schema"`
	Repo            string   `json:"repo"`
	Tag             string   `json:"tag"`
	Version         string   `json:"version"`
	Prerelease      bool     `json:"prerelease"`
	SourceSHA       string   `json:"source_sha"`
	SHA256SumsSHA   string   `json:"sha256sums_sha256"`
	Sequence        int64    `json:"sequence"`
	PromotedAt      string   `json:"promoted_at"`
	ExpiresAt       string   `json:"expires_at"`
	Revoked         []string `json:"revoked"`
	promoted, until time.Time
}

var (
	hex40 = regexp.MustCompile(`^[0-9a-f]{40}$`)
	hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// maxRevoked bounds the list a manifest may carry.
const maxRevoked = 512

// ParseManifest reads promote.json strictly: one JSON object of exactly the schema's fields, nothing after it, every field present and well formed.
// It checks the document in itself; the rules against the installed version and the state are CheckManifest's.
func ParseManifest(data []byte) (*Manifest, error) {
	if len(data) == 0 || len(data) > MaxManifestBytes {
		return nil, fmt.Errorf("manifest size %d is outside 1..%d", len(data), MaxManifestBytes)
	}
	// Presence: every field of the schema must be in the document (a missing one would otherwise read as its zero value).
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	for _, k := range []string{"schema", "repo", "tag", "version", "prerelease", "source_sha", "sha256sums_sha256", "sequence", "promoted_at", "expires_at", "revoked"} {
		if _, ok := keys[k]; !ok {
			return nil, fmt.Errorf("manifest: field %q is missing", k)
		}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("manifest: data after the document")
	}
	if m.Schema != 1 {
		return nil, fmt.Errorf("manifest: schema %d is not 1", m.Schema)
	}
	if _, err := ParseVersion(m.Version); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	if m.Tag != "v"+m.Version {
		return nil, fmt.Errorf("manifest: tag %q is not v%s", m.Tag, m.Version)
	}
	if !hex40.MatchString(m.SourceSHA) {
		return nil, errors.New("manifest: source_sha is not 40 lowercase hex digits")
	}
	if !hex64.MatchString(m.SHA256SumsSHA) {
		return nil, errors.New("manifest: sha256sums_sha256 is not 64 lowercase hex digits")
	}
	if m.Sequence < 1 {
		return nil, errors.New("manifest: sequence is below 1")
	}
	var err error
	if m.promoted, err = parseUTC(m.PromotedAt); err != nil {
		return nil, fmt.Errorf("manifest: promoted_at: %w", err)
	}
	if m.until, err = parseUTC(m.ExpiresAt); err != nil {
		return nil, fmt.Errorf("manifest: expires_at: %w", err)
	}
	if !m.until.After(m.promoted) {
		return nil, errors.New("manifest: expires_at is not after promoted_at")
	}
	if len(m.Revoked) > maxRevoked {
		return nil, errors.New("manifest: too many revoked versions")
	}
	for _, r := range m.Revoked {
		if _, err := ParseVersion(r); err != nil {
			return nil, fmt.Errorf("manifest: revoked: %w", err)
		}
	}
	return &m, nil
}

// parseUTC reads an RFC 3339 time that is written in UTC ("Z"): another zone is refused, so a time means one thing.
func parseUTC(s string) (time.Time, error) {
	if !strings.HasSuffix(s, "Z") {
		return time.Time{}, fmt.Errorf("%q is not an RFC 3339 UTC time", s)
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}

// PromotedAt and ExpiresAt are the parsed times.
func (m *Manifest) PromotedTime() time.Time { return m.promoted }
func (m *Manifest) ExpiresTime() time.Time  { return m.until }
