// The paths a connector forwards: validation and the allow rules (moved out of local.go, #5091; no behaviour change).

package tunnel

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"
)

// ErrPath is returned by ValidatePath.
var ErrPath = errors.New("tunnel: path not allowed")

// compatDotSlash are the compatibility characters that Unicode NFKC folds to
// "." "/" or "\" (one/two-dot leaders, fullwidth and small forms, fraction and
// division slashes). Go routers do not normalise, but a future one might, so
// they are refused outright.
const compatDotSlash = "\u2024\u2025\u2026\ufe52\uff0e\uff0f\u2215\u2044\uff3c\ufe68\u29f8\u2216"

// ValidatePath is the connector's allowlist for hub-supplied paths. It takes
// the escaped path as it will be sent to the local API and accepts it only if:
//
//   - it starts with /api/ or /ws/;
//   - it contains no percent-encoded ".", "/" or "\" at all (%2e, %2f, %5c);
//   - it has no empty segment ("//"), no "." or ".." segment, also when
//     percent-encoded, and no double encoding (%252e);
//   - no segment contains a backslash, NUL, control character, ";" (path
//     parameters), an encoded "/" (%2f) or invalid UTF-8 (overlong forms);
//   - it is not an absolute URL or scheme-relative target, carries no host
//     part, and contains no "?", "#", whitespace, CR or LF.
//
// Anything else is rejected, not normalised.
func ValidatePath(p string) error {
	if len(p) > 4096 {
		return ErrPath
	}
	if !strings.HasPrefix(p, "/api/") && !strings.HasPrefix(p, "/ws/") {
		return ErrPath
	}
	for i := 0; i < len(p); i++ {
		c := p[i]
		if c <= 0x20 || c >= 0x7f || c == '\\' || c == '?' || c == '#' || c == ';' {
			return ErrPath
		}
	}
	// "." "/" and "\\" are never legitimately percent-encoded by a client; an
	// encoded one is an evasion attempt, wherever it sits in the segment.
	if lp := strings.ToLower(p); strings.Contains(lp, "%2e") || strings.Contains(lp, "%2f") || strings.Contains(lp, "%5c") {
		return ErrPath
	}
	segs := strings.Split(p[1:], "/")
	for i, raw := range segs {
		if raw == "" && i != len(segs)-1 {
			return ErrPath
		}
		if raw == "." || raw == ".." {
			return ErrPath
		}
		d, err := url.PathUnescape(raw)
		if err != nil || !utf8.ValidString(d) {
			return ErrPath
		}
		if d == "." || d == ".." || strings.ContainsAny(d, "/\\;%") || strings.ContainsAny(d, compatDotSlash) {
			return ErrPath
		}
		for j := 0; j < len(d); j++ {
			if d[j] < 0x20 || d[j] == 0x7f {
				return ErrPath
			}
		}
	}
	return nil
}

func validQuery(q string) bool {
	if len(q) > 8192 || !utf8.ValidString(q) {
		return false
	}
	for i := 0; i < len(q); i++ {
		if q[i] <= 0x20 || q[i] >= 0x7f || q[i] == '#' {
			return false
		}
	}
	return true
}

// PathRule allows requests whose decoded path starts with Prefix (or equals it,
// when Exact is set) for the listed methods; no Methods means GET and HEAD only.
type PathRule struct {
	Prefix  string
	Exact   bool
	Methods []string
}

// PathRules is an allowlist of routes. Rules only ever add permissions; a
// request matching none is denied. Use rules.Allow as LocalHandler.AllowPath.
// An optional Gate can veto a request that a rule accepted (for example the
// per-server "allow launching from the web" switch).
type PathRules struct {
	Rules []PathRule
	Gate  func(method, decodedPath string) bool
}

// NewPathRules builds PathRules and validates them: a prefix rule must end in "/" so
// that "/api/session/" can never match "/api/sessions-admin", and every rule must
// start with "/". Exact rules are not constrained.
func NewPathRules(rules []PathRule, gate func(method, decodedPath string) bool) (PathRules, error) {
	for _, r := range rules {
		if !strings.HasPrefix(r.Prefix, "/") {
			return PathRules{}, fmt.Errorf("tunnel: path rule %q must start with /", r.Prefix)
		}
		if !r.Exact && !strings.HasSuffix(r.Prefix, "/") {
			return PathRules{}, fmt.Errorf("tunnel: prefix rule %q must end in / (use Exact for a single route)", r.Prefix)
		}
	}
	return PathRules{Rules: rules, Gate: gate}, nil
}

// Allow reports whether the request may reach the local API. A prefix rule that does
// not end in "/" matches nothing (fail closed), even if it was not built with NewPathRules.
func (p PathRules) Allow(method, decodedPath string) bool {
	if !allowedMethods[method] {
		return false
	}
	ok := false
	for _, r := range p.Rules {
		if r.Exact {
			if decodedPath != r.Prefix {
				continue
			}
		} else if !strings.HasSuffix(r.Prefix, "/") || !strings.HasPrefix(decodedPath, r.Prefix) {
			continue
		}
		methods := r.Methods
		if len(methods) == 0 {
			methods = []string{http.MethodGet, http.MethodHead}
		}
		for _, m := range methods {
			if m == method {
				ok = true
			}
		}
	}
	return ok && (p.Gate == nil || p.Gate(method, decodedPath))
}
