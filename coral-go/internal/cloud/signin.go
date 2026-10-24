package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/subgentic/coral/internal/safego"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/pkg/tunnel"
)

// Sign in with Coral Hub, the Coral half of the wire (#3966, coral-hub PR #529; design note
// r2 section 2). pkg/tunnel carries the four operations; this file owns their bodies, the
// owner pin and the list of return origins the hub was told. Everything the hub sends is
// parsed strictly (one JSON object, no unknown or repeated key) and anything else is a
// refusal: the caller fails closed.

// SigninEvent is something the local side must act on (internal/auth ends Hub sessions and
// writes the Operator-actions log).
type SigninEvent struct {
	Kind string // SigninOwnerPinned or SigninOwnerMismatch
	How  string // for SigninOwnerPinned: "enrollment" or "origins"
}

const (
	SigninOwnerPinned   = "owner_pinned"
	SigninOwnerMismatch = "owner_mismatch" // the hub named another account: Hub sign-in is off until re-linking
)

var (
	// ErrSigninRefused: the hub answered no (or an answer that isn't a well-formed yes). A
	// session it was asked about ends at once.
	ErrSigninRefused = errors.New("cloud: Coral Hub refused the sign-in")
	// ErrSigninDisabled: begin's one distinct answer, the hub's per-machine switch is off.
	ErrSigninDisabled = errors.New("cloud: Sign in with Coral Hub is turned off for this computer in Coral Hub")
	// ErrSigninUnanswered: no answer came (no tunnel, a timeout, the tunnel dropped). A
	// session may wait for the next check, up to the 10-minute grace.
	ErrSigninUnanswered = errors.New("cloud: Coral Hub didn't answer")
	// ErrSigninBusy: a check answered 429, Coral Hub's busy answer (its body is the same fixed
	// {"ok":false} as a refusal, so only the status tells them apart). It is transient like no
	// answer: it wraps ErrSigninUnanswered, so the grace applies, and it never counts as a good
	// check (#3966, coral-hub Security's finding).
	ErrSigninBusy = fmt.Errorf("%w: busy", ErrSigninUnanswered)
	// ErrSigninNoOwner: no owner is pinned (an older hub never named one): Hub sign-in is off.
	ErrSigninNoOwner = errors.New("cloud: this computer's Coral Hub owner isn't known")
)

const signinVersion = 1

// MaxSigninOrigins caps the return-origin list (it must fit in one 2 KiB body).
const MaxSigninOrigins = 16

var (
	flowRe   = regexp.MustCompile(`^[A-Za-z0-9_-]{26,32}$`)
	b64urlRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

// jsonFieldNames is the exact JSON names of the fields of the struct v points to (nil when v is not
// such a pointer). encoding/json matches a key to a field ignoring case, so {"ok":false,"OK":true}
// is two different keys to a byte comparison and one field to the decoder, where the last one wins:
// strictJSON checks keys against these names exactly (#4147, found by FuzzStrictJSON).
func jsonFieldNames(v any) map[string]bool {
	t := reflect.TypeOf(v)
	if t == nil || t.Kind() != reflect.Pointer || t.Elem().Kind() != reflect.Struct {
		return nil
	}
	names := map[string]bool{}
	for i := 0; i < t.Elem().NumField(); i++ {
		f := t.Elem().Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		switch name {
		case "-":
			continue
		case "":
			name = f.Name
		}
		names[name] = true
	}
	return names
}

// strictJSON decodes exactly one JSON object into v: an unknown or repeated key (compared exactly,
// not ignoring case), a second value or trailing bytes is an error.
func strictJSON(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return errors.New("not a JSON object")
	}
	names := jsonFieldNames(v)
	seen := map[string]bool{}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return err
		}
		k, _ := t.(string)
		if seen[k] {
			return fmt.Errorf("repeated key %q", k)
		}
		if names != nil && !names[k] {
			return fmt.Errorf("unknown key %q", k) // including a different case of a known one
		}
		seen[k] = true
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return err
		}
	}
	strict := json.NewDecoder(bytes.NewReader(b))
	strict.DisallowUnknownFields()
	if err := strict.Decode(v); err != nil {
		return err
	}
	if _, err := strict.Token(); err != io.EOF {
		return errors.New("trailing data")
	}
	return nil
}

// signin sends one operation over the live tunnel and returns the hub's status and body. A
// transport failure is ErrSigninUnanswered; a hub that doesn't (or no longer) offer sign-in
// is ErrSigninRefused.
func (m *Manager) signin(ctx context.Context, op string, req any) (int, []byte, error) {
	cs := m.session.Load()
	if cs == nil {
		return 0, nil, fmt.Errorf("%w: not connected", ErrSigninUnanswered)
	}
	body, err := json.Marshal(req)
	if err != nil {
		return 0, nil, err
	}
	select {
	case m.signinSlots <- struct{}{}: // sign-in's own budget beside the relay's
		defer func() { <-m.signinSlots }()
	case <-ctx.Done():
		return 0, nil, fmt.Errorf("%w: %v", ErrSigninUnanswered, ctx.Err())
	}
	r, err := cs.Signin(ctx, op, body)
	switch {
	case errors.Is(err, tunnel.ErrSigninUnsupported):
		return 0, nil, ErrSigninRefused
	case err != nil:
		return 0, nil, fmt.Errorf("%w: %v", ErrSigninUnanswered, err)
	}
	return r.Status, r.Body, nil
}

// SigninBegin is begin's answer.
type SigninBegin struct {
	Flow string
	URL  string // https on the configured hub's host
	TTL  time.Duration
}

// StartSignin starts a flow (design note 1.1 step 2). returnOrigin must be on the list the hub
// accepted.
func (m *Manager) StartSignin(ctx context.Context, challenge, state, returnOrigin string) (SigninBegin, error) {
	if !m.originAccepted(returnOrigin) {
		return SigninBegin{}, ErrSigninRefused
	}
	status, b, err := m.signin(ctx, tunnel.SigninBegin, map[string]any{"v": signinVersion, "challenge": challenge, "state": state, "return": returnOrigin})
	if err != nil {
		return SigninBegin{}, err
	}
	var r struct {
		OK     bool   `json:"ok"`
		Status string `json:"status"`
		Flow   string `json:"flow"`
		URL    string `json:"url"`
		TTL    int    `json:"ttl"`
	}
	if strictJSON(b, &r) != nil {
		return SigninBegin{}, ErrSigninRefused
	}
	if !r.OK {
		if r.Status == "disabled" {
			return SigninBegin{}, ErrSigninDisabled
		}
		return SigninBegin{}, ErrSigninRefused
	}
	if status != http.StatusOK || r.Status != "" || !flowRe.MatchString(r.Flow) || r.TTL < 1 || r.TTL > 300 || !m.hubPageURL(r.URL) {
		return SigninBegin{}, ErrSigninRefused
	}
	return SigninBegin{Flow: r.Flow, URL: r.URL, TTL: time.Duration(r.TTL) * time.Second}, nil
}

// hubPageURL reports whether u is an https URL on the configured hub's host, with no
// userinfo or fragment: the only place a start may send the browser.
func (m *Manager) hubPageURL(u string) bool {
	p, err := url.Parse(u)
	if err != nil || p.Scheme != "https" || p.User != nil || p.Fragment != "" || p.Host == "" {
		return false
	}
	hub, err := url.Parse(m.o.HubURL)
	return err == nil && strings.EqualFold(p.Host, hub.Host)
}

// SigninGrant is redeem's answer.
type SigninGrant struct {
	Grant   string
	Account string
	Display string // sanitised: no control characters or line breaks, at most 64 runes
	TTL     time.Duration
}

// RedeemSignin trades the code and the verifier for a grant (1.1 step 4). The grant must name the
// pinned owner.
func (m *Manager) RedeemSignin(ctx context.Context, code, verifier string) (SigninGrant, error) {
	owner, err := m.owner(ctx)
	if err != nil {
		return SigninGrant{}, err
	}
	if len(code) != 43 || !b64urlRe.MatchString(code) || len(verifier) < 43 || len(verifier) > 128 || !b64urlRe.MatchString(verifier) {
		return SigninGrant{}, ErrSigninRefused
	}
	status, b, err := m.signin(ctx, tunnel.SigninRedeem, map[string]any{"v": signinVersion, "code": code, "verifier": verifier})
	if err != nil {
		return SigninGrant{}, err
	}
	var r struct {
		OK      bool   `json:"ok"`
		Status  string `json:"status"`
		Grant   string `json:"grant"`
		Account string `json:"account"`
		Display string `json:"display"`
		Tier    string `json:"tier"` // older drafts send it; a Hub sign-in isn't capped by tier
		TTL     int    `json:"ttl"`
	}
	if strictJSON(b, &r) != nil || !r.OK || status != http.StatusOK || !ValidHubAccount(r.Grant) || !ValidHubAccount(r.Account) || r.TTL < 1 || r.TTL > 8*3600 {
		return SigninGrant{}, ErrSigninRefused
	}
	if r.Account != owner {
		m.ownerMismatch(ctx)
		return SigninGrant{}, ErrSigninRefused
	}
	return SigninGrant{Grant: r.Grant, Account: r.Account, Display: CleanDisplay(r.Display), TTL: time.Duration(r.TTL) * time.Second}, nil
}

// CheckSignin asks whether grant still holds. nil only for {"ok":true,"status":"ok"} naming the
// pinned owner; ErrSigninUnanswered when no answer came or the hub was busy (429,
// ErrSigninBusy); anything else, any other status included, ends the session.
func (m *Manager) CheckSignin(ctx context.Context, grant string) error {
	owner, err := m.owner(ctx)
	if err != nil {
		return err
	}
	status, b, err := m.signin(ctx, tunnel.SigninCheck, map[string]any{"v": signinVersion, "grant": grant})
	if err != nil {
		return err
	}
	if status == http.StatusTooManyRequests {
		return ErrSigninBusy // check only: begin and redeem take a 429 as a plain failure
	}
	var r struct {
		OK      bool   `json:"ok"`
		Status  string `json:"status"`
		Account string `json:"account"`
		Tier    string `json:"tier"`
	}
	if strictJSON(b, &r) != nil || !r.OK || status != http.StatusOK || r.Status != "ok" || !ValidHubAccount(r.Account) {
		return ErrSigninRefused
	}
	if r.Account != owner {
		m.ownerMismatch(ctx)
		return ErrSigninRefused
	}
	return nil
}

// owner is the pinned owner: ErrSigninNoOwner when none is, ErrSigninRefused once blocked
// (the hub named another account; until re-linking).
func (m *Manager) owner(ctx context.Context) (string, error) {
	// A team's machine has no person owner to sign in as (#7061), and a record that cannot be read is not a person's: both refuse.
	if kind, _, rerr := m.o.Store.OwnerRecord(ctx); rerr != nil || kind != store.OwnerKindPersonal {
		return "", ErrSigninNoOwner
	}
	o, err := m.o.Store.Owner(ctx)
	switch {
	case err != nil || o == "":
		return "", ErrSigninNoOwner
	case !ValidHubAccount(o):
		return "", ErrSigninRefused
	}
	return o, nil
}

// ownerMismatch blocks the pin (it survives a restart; a new enrollment clears it) and, the
// first time, tells the local side so it ends every Hub session.
func (m *Manager) ownerMismatch(ctx context.Context) {
	conn, err := m.o.Store.Get(ctx)
	if err != nil {
		return
	}
	if o, _ := m.o.Store.Owner(ctx); o == store.OwnerBlocked {
		return
	}
	if m.o.Store.BlockOwner(ctx, conn.ServerID) != nil {
		return
	}
	m.o.Logger.Warn("Coral Hub named another account than this computer's owner: Hub sign-in is off until the computer is linked again")
	m.signinEvent(ctx, SigninEvent{Kind: SigninOwnerMismatch})
}

func (m *Manager) signinEvent(ctx context.Context, ev SigninEvent) {
	if m.o.OnSigninEvent != nil {
		m.o.OnSigninEvent(ctx, ev)
	}
}

// CleanDisplay makes a Hub display name safe to store and show: control characters, line
// and paragraph breaks and format characters are dropped, and it is cut to 64 runes.
func CleanDisplay(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "")
	}
	var b strings.Builder
	n := 0
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			continue
		}
		if n == 64 {
			break
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

// HubPage is the configured Coral Hub's own address for a link on the sign-in page, only when it
// passes the same check as a page the browser is sent to (https on the configured host); else "".
func (m *Manager) HubPage() string {
	if m.hubPageURL(m.o.HubURL) {
		return m.o.HubURL
	}
	return ""
}

// SigninReady reports whether a sign-in may start for a page reached on origin: a live
// tunnel that offers sign-in, a pinned owner with no mismatch, and origin on the list the
// hub accepted on this tunnel.
func (m *Manager) SigninReady(ctx context.Context, origin string) bool {
	cs := m.session.Load()
	if cs == nil || !cs.CanSignin() || !m.originAccepted(origin) {
		return false
	}
	_, err := m.owner(ctx)
	return err == nil
}

func (m *Manager) originAccepted(origin string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.originsSession != nil && m.originsSession == m.session.Load() && slices.Contains(m.originsAccepted, origin)
}

// SigninOriginsChanged sends the current list again (the setting changed). Until the hub
// accepts it, no flow starts.
func (m *Manager) SigninOriginsChanged() {
	m.mu.Lock()
	m.originsAccepted, m.originsSession = nil, nil
	gen := m.gen
	m.mu.Unlock()
	if cs := m.session.Load(); cs != nil && cs.CanSignin() {
		go func() { defer safego.Recover("cloud/signin:m.pushOrigins"); m.pushOrigins(gen, cs) }()
	}
}

// pushOrigins sends the origin list on tunnel cs until the hub accepts it, the tunnel
// changes or the manager stops; the reply's account pins the owner if none is pinned yet.
func (m *Manager) pushOrigins(gen int, cs *tunnel.ClientSession) {
	if m.o.SigninOrigins == nil {
		return
	}
	wait := 2 * time.Second
	for {
		m.mu.Lock()
		root, stale := m.root, m.stale(gen)
		m.mu.Unlock()
		if stale || root == nil || root.Err() != nil || m.session.Load() != cs {
			return
		}
		origins := m.o.SigninOrigins()
		if len(origins) > MaxSigninOrigins {
			origins = origins[:MaxSigninOrigins]
		}
		ctx, cancel := context.WithTimeout(root, 15*time.Second)
		err := m.sendOrigins(ctx, cs, origins)
		cancel()
		if err == nil {
			m.mu.Lock()
			if !m.stale(gen) && m.session.Load() == cs {
				m.originsAccepted, m.originsSession = slices.Clone(origins), cs
			}
			m.mu.Unlock()
			return
		}
		select {
		case <-root.Done():
			return
		case <-time.After(wait):
		}
		wait = min(wait*2, time.Minute)
	}
}

func (m *Manager) sendOrigins(ctx context.Context, cs *tunnel.ClientSession, origins []string) error {
	if m.session.Load() != cs {
		return ErrSigninUnanswered
	}
	status, b, err := m.signin(ctx, tunnel.SigninOrigins, map[string]any{"v": signinVersion, "origins": origins})
	if err != nil {
		return err
	}
	var r struct {
		OK      bool   `json:"ok"`
		Account string `json:"account"`
	}
	if strictJSON(b, &r) != nil || !r.OK || status != http.StatusOK {
		return ErrSigninRefused
	}
	if !ValidHubAccount(r.Account) {
		return nil // accepted; an older hub names no owner, so nothing is pinned
	}
	conn, err := m.o.Store.Get(ctx)
	if err != nil {
		return err
	}
	pinned, err := m.o.Store.PinOwner(ctx, conn.ServerID, r.Account)
	if err != nil {
		return err
	}
	if pinned {
		m.signinEvent(ctx, SigninEvent{Kind: SigninOwnerPinned, How: "origins"})
		return nil
	}
	if o, _ := m.o.Store.Owner(ctx); o != r.Account {
		m.ownerMismatch(ctx)
	}
	return nil
}
