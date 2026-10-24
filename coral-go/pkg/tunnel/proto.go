// Package tunnel implements the Coral Cloud Hub tunnel protocol, both halves.
//
// A Coral server (the connector) dials the hub over one outbound WebSocket.
// After a small JSON handshake the WebSocket carries a yamux session. The hub
// opens streams toward the connector; the connector never opens streams. Each
// stream starts with a length-prefixed JSON Header, then a kind specific body:
//
//	http: request body chunks hub→connector, then a ResponseHeader frame and
//	      response body chunks connector→hub. Bodies use chunk framing
//	      (uint32 length + data, zero length = clean end) so a truncated body
//	      is distinguishable from a complete one and every Write is delivered
//	      immediately (SSE works).
//	ws:   a ResponseHeader frame (handshake result), then WebSocket messages
//	      both ways as [type u8][len u32][payload], close frames carry a code.
//
// Half-closing a stream from the hub side after the request has been sent
// means "cancel".
//
// WebSockets use github.com/coder/websocket, the maintained successor of nhooyr.io/websocket
// (same API), since #3663: nhooyr answered a close frame it had already sent, so a relayed
// close reached the browser twice.

package tunnel

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Protocol versions. The client announces the newest version it speaks; the
// server accepts min(client, ProtocolVersion) provided that is at least
// MinProtocolVersion, so older connectors keep working across upgrades.
//
// Version 2 (#3489) adds Welcome.Capabilities: the hub may advertise "relay", and only then
// may the connector open relay streams (relay.go). A version 1 peer on either side never
// sees or opens one.
const (
	ProtocolVersion    = 2
	MinProtocolVersion = 1
)

// Handshake error codes carried in Welcome.Error.
const (
	CodeUnauthorized       = "unauthorized"
	CodeUnsupportedVersion = "unsupported_version"
	CodeBadHandshake       = "bad_handshake"
	// CodeRetryLater tells the connector the hub cannot take it right now (rate
	// limit, per-account tunnel cap, database trouble). Unlike unauthorized, it
	// is not permanent: the connector keeps retrying with backoff.
	CodeRetryLater = "retry_later"
)

// Stream rejection codes carried in ResponseHeader.ErrorCode.
const (
	CodeTooManyStreams = "too_many_streams"
	CodeBadRequest     = "bad_request"
	CodePathNotAllowed = "path_not_allowed"
	// CodeLocalMode: the route is valid but the connector machine's own maximum access mode is
	// below it. The message is the local mode ("view" or "interact").
	CodeLocalMode = "local_mode"
	CodeUpstream  = "upstream_error"
)

// Hello is the first message, client to server.
type Hello struct {
	ProtocolVersion int    `json:"protocol_version"`
	ServerToken     string `json:"server_token"`
	CoralVersion    string `json:"coral_version"`
	// LocalMode is the connector's own maximum access mode ("view", "interact" or
	// "launch"). It is informational for the hub (to show "limited by this machine");
	// the connector enforces it whatever the hub does. Absent or unknown means view.
	LocalMode string `json:"local_mode,omitempty"`
	// Capabilities are what this Coral can do beyond the base protocol, each "name/version" (#5508): "cross_team_allowlist/1" means it keeps and checks a board's list of allowed senders
	// by cross-team board id. A hub reads it only from the destination's own Hello (never from a sender's request or message); absent, malformed or another version means unsupported.
	Capabilities []string `json:"capabilities,omitempty"`
	// OwnerKind is what this machine is enrolled as: "personal" or "team" (#7061), echoed to the hub at every connect. Informational for the hub: Coral never trusts what the hub says back about it, it
	// keeps the kind it recorded at enrolment. Absent from an older Coral, which the hub reads as one that does not know team machines.
	OwnerKind string `json:"owner_kind,omitempty"`
	// BootID is random, made once per Coral process, and StartedAt (RFC 3339, UTC) is when that process started (#5855). Both are the same on every connection of one process and
	// change with a restart, so a hub that sent a request on one connection and sees another BootID on the next knows that what Coral keeps in memory (an Idempotency-Key's record,
	// for one) is gone: a retry may start a second one. Informational: nothing in the protocol depends on them, and a Hello without them is as before.
	BootID    string `json:"boot_id,omitempty"`
	StartedAt string `json:"started_at,omitempty"`
}

// CapCrossTeamAllowlist is this Coral's list-of-senders capability, version 1 (see Hello.Capabilities).
const CapCrossTeamAllowlist = "cross_team_allowlist/1"

// CapCrossTeamLinks is the cross-team link capability, version 1, "cross_team_links/1" (#5572), in BOTH directions. A Coral says it in Hello.Capabilities: it takes the hub's link snapshots, so a hub
// sends them only to a Coral that says so. A hub says it in Welcome.Capabilities: it keeps the link table and sends it.
// It is per connection. The connector stamps what it learned from THIS connection on the requests that carry a relayed message or a snapshot (HubLinksCapHeader), never what the hub says on a
// request.
const CapCrossTeamLinks = "cross_team_links/1"

// capabilityShape is a well-formed capability string: a lower-case name, a slash and a whole version.
var capabilityShape = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}/[0-9]{1,4}$`)

// maxCapabilities bounds how many a Hello may carry.
const maxCapabilities = 16

// HasCapability reports whether a Hello's capabilities name want, exactly: a list that is too long, or holds any malformed string, supports nothing (never a partial read).
func HasCapability(caps []string, want string) bool {
	if len(caps) > maxCapabilities {
		return false
	}
	have := false
	for _, c := range caps {
		if !capabilityShape.MatchString(c) {
			return false
		}
		if c == want {
			have = true
		}
	}
	return have
}

// wellFormedCapabilities is caps when the list is within the bound and every entry has the shape, else nil.
func wellFormedCapabilities(caps []string) []string {
	if len(caps) > maxCapabilities {
		return nil
	}
	for _, c := range caps {
		if !capabilityShape.MatchString(c) {
			return nil
		}
	}
	return append([]string(nil), caps...)
}

// Welcome is the server's reply to Hello.
type Welcome struct {
	AcceptedVersion int    `json:"accepted_version,omitempty"`
	Error           string `json:"error,omitempty"`
	MaxStreams      int    `json:"max_streams,omitempty"`
	// Capabilities (version 2) the hub turns on for this tunnel, e.g. "relay".
	Capabilities []string `json:"capabilities,omitempty"`
	// RouteKey (optional, #3683, coral-hub's account affinity) is an opaque routing hint: the
	// connector sends it back as the RouteHeader on its later dials to the same hub, so the hub
	// can place the tunnel near the account's other sessions. Exactly 32 lowercase hex
	// characters; anything else is ignored. Old hubs never send it (no version change).
	RouteKey string `json:"route_key,omitempty"`
	// RetryAfter (optional, #6318, coral-hub's ask) is how many whole seconds a hub busy with a reconnect storm asks the connector to wait before it dials again, sent with a
	// retry_later refusal. The connector uses it as the floor of its next delay, never longer than MaxRetryAfter whatever the hub says. Old hubs never send it (no version change), and an
	// old connector ignores it.
	RetryAfter int `json:"retry_after,omitempty"`
}

// MaxRetryAfter is the longest wait a hub's retry_after can ask for: a hub (or a hostile peer) cannot park a connector for longer.
const MaxRetryAfter = 300 * time.Second

// retryLaterError is ErrRetryLater with the wait the hub asked for.
type retryLaterError struct{ after time.Duration }

func (e retryLaterError) Error() string        { return ErrRetryLater.Error() }
func (e retryLaterError) Is(target error) bool { return target == ErrRetryLater }

// RetryLater is what a hub's AuthFunc returns to refuse a connection for now and ask for a wait of at least after (whole seconds, at most MaxRetryAfter). It is an ErrRetryLater.
// With no wait (or none that is a whole second) it is ErrRetryLater itself, as an old hub's refusal is.
func RetryLater(after time.Duration) error {
	if after = clampRetryAfter(after); after == 0 {
		return ErrRetryLater
	}
	return retryLaterError{after: after}
}

// RetryAfterOf is the wait a refusal asked for: zero for an error that is not a retry_later with one, and never more than MaxRetryAfter.
func RetryAfterOf(err error) time.Duration {
	var r retryLaterError
	if errors.As(err, &r) {
		return r.after
	}
	return 0
}

func clampRetryAfter(d time.Duration) time.Duration {
	switch {
	case d <= 0:
		return 0
	case d > MaxRetryAfter:
		return MaxRetryAfter
	}
	return d.Truncate(time.Second)
}

// RouteHeader carries a hub's last route key on a tunnel dial to that same hub (#3683).
const RouteHeader = "X-Coral-Route"

// ValidRouteKey reports whether k is a well-formed route key: exactly 32 lowercase hex
// characters. A route key is never logged.
func ValidRouteKey(k string) bool {
	if len(k) != 32 {
		return false
	}
	for i := 0; i < len(k); i++ {
		if c := k[i]; !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

var (
	ErrUnauthorized       = errors.New("tunnel: unauthorized")
	ErrUnsupportedVersion = errors.New("tunnel: unsupported protocol version")
	ErrBadHandshake       = errors.New("tunnel: bad handshake")
	ErrRetryLater         = errors.New("tunnel: hub busy, retry later")
	// ErrAuthUnavailable is what an AuthFunc returns when it cannot decide (database
	// outage, overload) as opposed to rejecting the token. The connector sees a
	// temporary refusal and retries with backoff; only ErrUnauthorized is permanent.
	ErrAuthUnavailable = ErrRetryLater
	ErrSessionClosed   = errors.New("tunnel: session closed")
	ErrTooManyStreams  = errors.New("tunnel: too many concurrent streams")
	ErrHeaderTooLarge  = errors.New("tunnel: header too large")
	ErrBadHeader       = errors.New("tunnel: malformed stream header")
)

// StreamError is returned when the connector rejected or failed a stream.
type StreamError struct {
	Code       string
	Message    string
	Status     int    // upstream status for failed WebSocket handshakes, else 0
	RetryAfter string // the upstream's Retry-After for a 429, whole seconds as the connector cleaned them, else ""
}

func (e *StreamError) Error() string {
	if e.Message == "" {
		return "tunnel: stream rejected: " + e.Code
	}
	return "tunnel: stream rejected: " + e.Code + ": " + e.Message
}

// Kind is the stream type.
type Kind string

const (
	KindHTTP Kind = "http"
	KindWS   Kind = "ws"
)

// Header opens a stream (hub to connector).
type Header struct {
	Kind          Kind        `json:"kind"`
	Method        string      `json:"method,omitempty"`
	Path          string      `json:"path"`            // escaped path, as sent upstream
	Query         string      `json:"query,omitempty"` // raw query
	Headers       http.Header `json:"headers,omitempty"`
	Protocols     []string    `json:"protocols,omitempty"` // ws subprotocols
	ContentLength int64       `json:"content_length,omitempty"`
	// HubUser is the Coral Hub user the stream is for (hub-user-<8 hex>), for the connector's
	// per-user limits only (#3642). It is a field of the stream, not an HTTP header, so it never
	// reaches the local API; an old hub doesn't send it.
	HubUser string `json:"hub_user,omitempty"`
	// HubTier is the viewer's effective tier ("view", "interact" or "launch"), set from the
	// hub's context (WithHubTier); it decides only whether answers are redacted (hubtier.go).
	// Missing or anything else is View.
	HubTier string `json:"hub_tier,omitempty"`
	// HubRole is "owner" when the viewer is the hub account this machine is enrolled to, set by the hub on the two update routes only
	// (hubrole.go, #5280). Missing or anything else is no role.
	HubRole string `json:"hub_role,omitempty"`
	// HubOrg, HubTeamRole and HubUserID say who a request to a team-owned machine is for (#7061, hubteam.go): the organisation (a canonical lower-case UUID), the person's role in it (owner, admin or
	// member) and their stable id (hub-uid-<32 hex>). They are fields of the stream like HubRole, set from the hub's context only; a machine that is not a team's ignores them.
	HubOrg      string `json:"hub_org,omitempty"`
	HubTeamRole string `json:"hub_team_role,omitempty"`
	HubUserID   string `json:"hub_user_id,omitempty"`
}

// ResponseHeader answers a Header (connector to hub).
type ResponseHeader struct {
	Status      int         `json:"status,omitempty"`
	Headers     http.Header `json:"headers,omitempty"`
	Subprotocol string      `json:"subprotocol,omitempty"`
	Error       string      `json:"error,omitempty"`
	ErrorCode   string      `json:"error_code,omitempty"`
}

// Limits bound what a peer may send. Zero fields take the defaults.
type Limits struct {
	MaxHeaderBytes int // encoded header frame, default 64 KiB
	MaxHeaders     int // header names, default 128
	MaxHeaderValue int // single name or value, default 8 KiB
	MaxPath        int // default 4 KiB
	MaxQuery       int // default 8 KiB
}

func (l Limits) withDefaults() Limits {
	if l.MaxHeaderBytes <= 0 {
		l.MaxHeaderBytes = 64 << 10
	}
	if l.MaxHeaders <= 0 {
		l.MaxHeaders = 128
	}
	if l.MaxHeaderValue <= 0 {
		l.MaxHeaderValue = 8 << 10
	}
	if l.MaxPath <= 0 {
		l.MaxPath = 4 << 10
	}
	if l.MaxQuery <= 0 {
		l.MaxQuery = 8 << 10
	}
	return l
}

// DecodeHeader parses and validates an encoded stream Header with default
// limits. It never panics on malformed input.
func DecodeHeader(b []byte) (Header, error) { return decodeHeader(b, Limits{}) }

func decodeHeader(b []byte, l Limits) (Header, error) {
	l = l.withDefaults()
	var h Header
	if len(b) > l.MaxHeaderBytes {
		return h, ErrHeaderTooLarge
	}
	if err := json.Unmarshal(b, &h); err != nil {
		return Header{}, fmt.Errorf("%w: %v", ErrBadHeader, err)
	}
	if err := h.validate(l); err != nil {
		return Header{}, err
	}
	return h, nil
}

func cleanToken(s string, max int, allowEmpty bool) bool {
	if (!allowEmpty && s == "") || len(s) > max || !utf8.ValidString(s) {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7f {
			return false
		}
	}
	return true
}

func (h *Header) validate(l Limits) error {
	switch h.Kind {
	case KindHTTP:
		if h.Method == "" || len(h.Method) > 16 || strings.ContainsAny(h.Method, " \t\r\n\x00") || !cleanToken(h.Method, 16, false) {
			return fmt.Errorf("%w: method", ErrBadHeader)
		}
	case KindWS:
		if h.Method != "" && h.Method != http.MethodGet {
			return fmt.Errorf("%w: method", ErrBadHeader)
		}
	default:
		return fmt.Errorf("%w: kind", ErrBadHeader)
	}
	if !cleanToken(h.Path, l.MaxPath, false) || !cleanToken(h.Query, l.MaxQuery, true) {
		return fmt.Errorf("%w: path or query", ErrBadHeader)
	}
	if h.ContentLength < -1 {
		return fmt.Errorf("%w: content length", ErrBadHeader)
	}
	if h.HubUser != "" && !ValidHubUser(h.HubUser) {
		return fmt.Errorf("%w: hub user", ErrBadHeader)
	}
	if h.HubRole != "" && h.HubRole != HubRoleOwner {
		return fmt.Errorf("%w: hub role", ErrBadHeader)
	}
	if h.HubOrg != "" && !ValidHubOrg(h.HubOrg) {
		return fmt.Errorf("%w: hub org", ErrBadHeader)
	}
	if h.HubTeamRole != "" && !ValidHubTeamRole(h.HubTeamRole) {
		return fmt.Errorf("%w: hub team role", ErrBadHeader)
	}
	if h.HubUserID != "" && !ValidHubUserID(h.HubUserID) {
		return fmt.Errorf("%w: hub user id", ErrBadHeader)
	}
	if h.HubTeamRole != "" && h.HubOrg == "" {
		return fmt.Errorf("%w: hub team role without an org", ErrBadHeader)
	}
	if len(h.Protocols) > 16 {
		return fmt.Errorf("%w: protocols", ErrBadHeader)
	}
	for _, p := range h.Protocols {
		if !cleanToken(p, 256, false) {
			return fmt.Errorf("%w: protocol", ErrBadHeader)
		}
	}
	return validHeaders(h.Headers, l)
}

// CanonicalHeader returns h with every name in canonical form, the values of names
// that differ only in case merged (in map order). Nil stays nil.
func CanonicalHeader(h http.Header) http.Header {
	if h == nil {
		return nil
	}
	out := make(http.Header, len(h))
	for k, vs := range h {
		c := http.CanonicalHeaderKey(k)
		out[c] = append(out[c], vs...)
	}
	return out
}

func validHeaders(hd http.Header, l Limits) error {
	if len(hd) > l.MaxHeaders {
		return fmt.Errorf("%w: too many headers", ErrBadHeader)
	}
	n := 0
	for k, vs := range hd {
		if !cleanToken(k, l.MaxHeaderValue, false) || strings.ContainsAny(k, " \t:") {
			return fmt.Errorf("%w: header name", ErrBadHeader)
		}
		n += len(vs)
		if n > 4*l.MaxHeaders {
			return fmt.Errorf("%w: too many header values", ErrBadHeader)
		}
		for _, v := range vs {
			if !cleanToken(v, l.MaxHeaderValue, true) {
				return fmt.Errorf("%w: header value", ErrBadHeader)
			}
		}
	}
	return nil
}

// ── Framing ─────────────────────────────────────────────────────────

func writeFrame(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	buf := make([]byte, 4+len(b))
	binary.BigEndian.PutUint32(buf, uint32(len(b)))
	copy(buf[4:], b)
	_, err = w.Write(buf) // single Write: one yamux data frame
	return err
}

// readFrame reads one length-prefixed frame, refusing frames above max before
// allocating.
func readFrame(r io.Reader, max int) ([]byte, error) {
	var lb [4]byte
	if _, err := io.ReadFull(r, lb[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(lb[:])
	if uint64(n) > uint64(max) {
		return nil, ErrHeaderTooLarge
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	return b, nil
}

// ── Chunked bodies ──────────────────────────────────────────────────

const maxChunk = 32 << 10

type chunkWriter struct {
	w    io.Writer
	done bool
}

func (c *chunkWriter) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		n := len(p)
		if n > maxChunk {
			n = maxChunk
		}
		buf := make([]byte, 4+n)
		binary.BigEndian.PutUint32(buf, uint32(n))
		copy(buf[4:], p[:n])
		if _, err := c.w.Write(buf); err != nil {
			return written, err
		}
		written += n
		p = p[n:]
	}
	return written, nil
}

// Close writes the terminating zero-length chunk.
func (c *chunkWriter) Close() error {
	if c.done {
		return nil
	}
	c.done = true
	_, err := c.w.Write([]byte{0, 0, 0, 0})
	return err
}

// chunkReader reads chunked data. A stream that ends before the terminator
// yields io.ErrUnexpectedEOF. limit>0 caps total bytes.
type chunkReader struct {
	r     io.Reader
	left  int
	eof   bool
	total int64
	limit int64
}

var errBodyTooLarge = errors.New("tunnel: body too large")

func (c *chunkReader) Read(p []byte) (int, error) {
	if c.eof {
		return 0, io.EOF
	}
	if c.left == 0 {
		var lb [4]byte
		if _, err := io.ReadFull(c.r, lb[:]); err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return 0, err
		}
		n := binary.BigEndian.Uint32(lb[:])
		if n == 0 {
			c.eof = true
			return 0, io.EOF
		}
		if n > maxChunk {
			return 0, ErrBadHeader
		}
		c.left = int(n)
	}
	if len(p) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= n
	c.total += int64(n)
	if c.limit > 0 && c.total > c.limit {
		return n, errBodyTooLarge
	}
	if err == io.EOF {
		if c.left > 0 {
			err = io.ErrUnexpectedEOF
		} else {
			err = nil
		}
	}
	return n, err
}
