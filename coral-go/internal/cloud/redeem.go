package cloud

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	terms "github.com/subgentic/coral/pkg/license"
)

// Redeeming a one-time credential for this server's identity on the Coral Hub
// (#3149). A launch-anywhere bootstrap credential (chb_…) goes to
// /api/bootstrap/enroll (Hub design 5.7, rev 2.2); a Coder workspace ticket
// (cht_…) to /api/enroll/coder/exchange (contract 4.4). The credential travels
// only in the Authorization header; the body carries the terms version this
// binary shows and, for a bootstrap, the redeem id and the opaque proof.
// Nothing credential-bearing is logged.

const (
	bootstrapEnrollPath = "/api/bootstrap/enroll"
	coderExchangePath   = "/api/enroll/coder/exchange"
)

// Redeem outcomes. ErrCredentialInvalid is the Hub's single generic refusal:
// consumed, expired, revoked, unknown, wrong adapter and a failed proof all look
// the same, by design (no oracle).
var (
	ErrCredentialInvalid = errors.New("credential invalid or already used: ask for a new one")
	ErrTermsReconsent    = errors.New("the Coral Hub needs the terms of this Coral version accepted again: ask for a new credential after accepting them")
)

// RetryAfterError is a refusal that may be retried later (429, 5xx, transport).
type RetryAfterError struct {
	After   time.Duration // 0: the caller's own backoff
	Why     string
	reached bool
}

func (e *RetryAfterError) Error() string { return "cloud: try again later: " + e.Why }

// mayHaveReached reports whether the request may have reached the Hub (and so
// used the credential): a transport failure or a 5xx, unlike a 429.
func (e *RetryAfterError) mayHaveReached() bool { return e.reached }

// Redeemed is the identity the Hub issued.
type Redeemed struct {
	ServerID      string
	Token         string // secret
	TermsAccepted string
}

// credentialRe: a Hub credential is a typed base64url secret: chb_ (bootstrap)
// or cht_ (Coder ticket).
var credentialRe = regexp.MustCompile(`^ch[bt]_[A-Za-z0-9_-]{16,}$`)

// ParseCredential trims a credential read from a file and checks its shape,
// so whatever was read is never echoed or sent if it is not one.
func ParseCredential(raw []byte) (string, error) {
	c := strings.TrimSpace(string(raw))
	if len(c) > maxCredentialFile || !credentialRe.MatchString(c) {
		return "", errors.New("the file does not contain a Coral Hub credential")
	}
	return c, nil
}

// NewRedeemID returns a random 128-bit redeem id (base64url, 22 chars). One id
// is used for a whole attempt series, so a retry after a lost response is
// recognised by the Hub and returns the same identity (design 5.7).
func NewRedeemID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("cloud: no randomness for the redeem id: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// proofFields are the fields each proof kind carries besides "kind" (Hub
// design 5.7). The Hub verifies them; Coral checks only the shape.
// (azure is known to the Hub but blocked for now: refused locally.)
var proofFields = map[string][]string{
	"aws":        {"document", "signature"},
	"gcp":        {"token"},
	"kubernetes": {"token"},
}

// ValidateProof checks a proof file's shape (Hub design 5.7): at most 16 KiB of
// UTF-8, one JSON object whose "kind" is aws, gcp, azure or kubernetes, with
// exactly that kind's fields, each a non-empty string, and no other key. The
// contents are passed through unchanged; the Hub verifies them.
func ValidateProof(raw []byte) error {
	if len(raw) > maxProofFile || !utf8.Valid(raw) {
		return errors.New("proof file: at most 16 KiB of UTF-8")
	}
	var p map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&p); err != nil || p == nil || dec.More() {
		return errors.New("proof file: not one JSON object")
	}
	var kind string
	if err := json.Unmarshal(p["kind"], &kind); err != nil {
		return errors.New(`proof file: "kind" must be aws, gcp or kubernetes`)
	}
	if kind == "azure" {
		return errors.New("proof file: the azure adapter is not available yet")
	}
	fields, ok := proofFields[kind]
	if !ok {
		return errors.New(`proof file: "kind" must be aws, gcp or kubernetes`)
	}
	if len(p) != len(fields)+1 {
		return fmt.Errorf("proof file: a %s proof has exactly the fields kind, %s", kind, strings.Join(fields, ", "))
	}
	for _, f := range fields {
		var v string
		if err := json.Unmarshal(p[f], &v); err != nil || v == "" {
			return fmt.Errorf("proof file: a %s proof needs a non-empty %q", kind, f)
		}
	}
	return nil
}

// ErrHubRequest is the Hub's 400: a request Coral should never have sent.
var ErrHubRequest = errors.New("the Coral Hub rejected the request as malformed (a Coral bug: please report it)")

// Redeem exchanges credential for this server's identity. hubURL is the
// configured origin; the identity is refused unless the Hub names exactly that
// origin, mode view, and accepted exactly this binary's terms version.
// redeemID is the attempt series' id (bootstrap credentials).
func Redeem(ctx context.Context, hc *http.Client, hubURL, credential string, proof []byte, hostname, redeemID string) (*Redeemed, error) {
	body := map[string]any{"eula_version": terms.EULAVersion}
	if h := cleanInfo(hostname, 64); h != "" {
		body["hostname"] = h
	}
	path := coderExchangePath
	if strings.HasPrefix(credential, "chb_") {
		path = bootstrapEnrollPath
		body["redeem_id"] = redeemID
		if len(proof) > 0 {
			body["proof"] = json.RawMessage(proof) // unchanged, never logged
		}
	} else if len(proof) > 0 {
		return nil, errors.New("a proof file goes only with a bootstrap credential (chb_)")
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hubURL+path, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+credential)
	resp, err := hc.Do(req)
	if err != nil {
		return nil, &RetryAfterError{Why: "the Coral Hub is unreachable", reached: true} // no URL or error text: it may echo the request
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxHubResponse+1))
	if err != nil || len(raw) > maxHubResponse {
		return nil, &RetryAfterError{Why: "the Coral Hub's answer was cut short", reached: true}
	}
	var r struct {
		ServerID      string `json:"server_id"`
		Token         string `json:"server_token"`
		HubURL        string `json:"hub_url"`
		Mode          string `json:"mode"`
		TermsAccepted string `json:"terms_accepted"`
		Error         string `json:"error"`
	}
	_ = json.Unmarshal(raw, &r)
	switch {
	case resp.StatusCode == http.StatusOK:
		if !strings.HasPrefix(r.Token, tokenPrefix) || len(r.Token) > 256 || !serverIDRe.MatchString(r.ServerID) {
			return nil, ErrHubResponse
		}
		if strings.TrimRight(r.HubURL, "/") != hubURL {
			return nil, fmt.Errorf("%w: the identity is for another hub", ErrHubResponse)
		}
		if r.TermsAccepted != terms.EULAVersion {
			return nil, fmt.Errorf("%w: terms accepted for another version", ErrHubResponse)
		}
		if r.Mode != "view" {
			return nil, fmt.Errorf("%w: an identity must start in view mode", ErrHubResponse)
		}
		return &Redeemed{ServerID: r.ServerID, Token: r.Token, TermsAccepted: r.TermsAccepted}, nil
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, ErrCredentialInvalid
	case resp.StatusCode == http.StatusConflict && r.Error == "terms_reconsent_required":
		return nil, ErrTermsReconsent
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, &RetryAfterError{After: retryAfter(resp.Header.Get("Retry-After")), Why: "the Coral Hub is busy"}
	case resp.StatusCode >= 500:
		return nil, &RetryAfterError{Why: "the Coral Hub had an error", reached: true}
	case resp.StatusCode == http.StatusBadRequest:
		return nil, ErrHubRequest
	}
	return nil, fmt.Errorf("%w: HTTP %d", ErrHubResponse, resp.StatusCode)
}

// retryAfter reads a Retry-After header in seconds, capped at a minute.
func retryAfter(v string) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 {
		return 0
	}
	return min(time.Duration(n)*time.Second, time.Minute)
}
