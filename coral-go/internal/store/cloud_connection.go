package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/subgentic/coral/pkg/secretbox"
	"github.com/subgentic/coral/pkg/tunnel"
)

// Cloud connection states.
const (
	CloudStateActive  = "active"  // token usable; the connector may dial
	CloudStateRevoked = "revoked" // hub rejected the token; never retried until re-enrolled
)

// ErrNoCloudConnection is returned when this server is not enrolled.
var ErrNoCloudConnection = errors.New("not enrolled with a Coral Hub")

// CloudConnection is the safe view of the enrollment. It never carries the token.
type CloudConnection struct {
	HubURL    string  `json:"hub_url" db:"hub_url"`
	ServerID  string  `json:"server_id" db:"server_id"`
	Label     string  `json:"label" db:"label"`
	State     string  `json:"state" db:"state"`
	CreatedAt string  `json:"created_at" db:"created_at"`
	LastError *string `json:"last_error" db:"last_error"`
}

// CloudStore keeps this server's single Cloud Hub enrollment. The server token is
// sealed with the same master key as remote API keys (internal secretbox,
// AES-256-GCM, key outside the database) and bound to the server id as AAD.
type CloudStore struct {
	db *DB
	rs *RemoteServerStore // owns the master key loading/creation policy
}

// NewCloudStore creates the store. rs supplies the shared master key.
func NewCloudStore(db *DB, rs *RemoteServerStore) *CloudStore { return &CloudStore{db: db, rs: rs} }

func cloudAAD(serverID string) string { return "cloud-connector:" + serverID }

// CheckSecrets proves the token can be stored here before anything is enrolled (#3693): the
// master key loads (or is created, when nothing is stored yet) and seals and opens a value.
// Enrolling first and failing to store the token afterwards left an enrollment in Coral Hub
// with no working connection. The error never holds key material.
func (s *CloudStore) CheckSecrets(ctx context.Context) error {
	box, err := s.rs.getBox(ctx, true, false)
	if err != nil {
		return err
	}
	const aad = "cloud-connector:check"
	env, err := box.Seal(aad, []byte("check"))
	if err != nil {
		return unreadable(err)
	}
	pt, err := box.Open(aad, env)
	if err != nil || string(pt) != "check" {
		return unreadable(fmt.Errorf("the master key can't open what it sealed: %v", err))
	}
	return nil
}

// Save replaces any existing enrollment with a new, active one. The token is
// sealed before it touches the database.
func (s *CloudStore) Save(ctx context.Context, hubURL, serverID, label, token string) error {
	return s.save(ctx, hubURL, serverID, label, token, nil)
}

// SaveTeam is Save for an enrollment of a Coral Hub organisation's machine (#7061): org (a canonical lower-case UUID) is recorded with the enrollment in the same statement, so there is no moment
// when a team's machine is stored as a personal one. A malformed org is refused.
func (s *CloudStore) SaveTeam(ctx context.Context, hubURL, serverID, label, token, org string) error {
	if !tunnel.ValidHubOrg(org) {
		return errors.New("a team enrollment needs the organisation's id")
	}
	return s.save(ctx, hubURL, serverID, label, token, &org)
}

func (s *CloudStore) save(ctx context.Context, hubURL, serverID, label, token string, teamOrg *string) error {
	box, err := s.rs.getBox(ctx, true, false)
	if err != nil {
		return err
	}
	env, err := box.Seal(cloudAAD(serverID), []byte(token))
	if err != nil {
		return err
	}
	// A new enrollment starts without a route key (#3683) or an owner (#3966): an earlier one's
	// belonged to it.
	// The kind and organisation are the new enrollment's own, or none: a team record is never inherited.
	var kind, org any
	if teamOrg != nil {
		kind, org = OwnerKindTeam, *teamOrg
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO cloud_connection (id, hub_url, server_id, label, token, state, created_at, route_key, hub_owner, hub_owner_user, hub_owner_kind, hub_owner_org)
		VALUES (1, ?, ?, ?, ?, ?, ?, NULL, NULL, NULL, ?, ?)
		ON CONFLICT(id) DO UPDATE SET hub_url=excluded.hub_url, server_id=excluded.server_id, label=excluded.label,
		  token=excluded.token, state=excluded.state, created_at=excluded.created_at, last_error=NULL, route_key=NULL, hub_owner=NULL, hub_owner_user=NULL, hub_owner_adopted_at=NULL,
		  hub_owner_kind=excluded.hub_owner_kind, hub_owner_org=excluded.hub_owner_org`,
		strings.TrimRight(hubURL, "/"), serverID, label, env, CloudStateActive, nowUTC(), kind, org)
	return err
}

// OwnerKindTeam and OwnerKindPersonal are what OwnerRecord reports as the kind.
const (
	OwnerKindPersonal = tunnel.OwnerKindPersonal
	OwnerKindTeam     = tunnel.OwnerKindTeam
)

// OwnerRecord is what the machine is enrolled as (#7061): OwnerKindPersonal and "" for an enrollment made for a person (or before teams existed), OwnerKindTeam and the organisation's id for a team's.
// There is no enrollment: personal, "", and no error (nothing is owned, and nothing here is a team's). Any other stored kind, or a team with no well-formed organisation, is an error, which every
// caller treats as "no owner": a record this code does not understand never falls back to personal.
func (s *CloudStore) OwnerRecord(ctx context.Context) (kind, org string, err error) {
	var r struct {
		Kind sql.NullString `db:"hub_owner_kind"`
		Org  sql.NullString `db:"hub_owner_org"`
	}
	err = s.db.GetContext(ctx, &r, "SELECT hub_owner_kind, hub_owner_org FROM cloud_connection WHERE id = 1")
	if errors.Is(err, sql.ErrNoRows) {
		return OwnerKindPersonal, "", nil
	}
	if err != nil {
		return "", "", err
	}
	switch {
	case !r.Kind.Valid || r.Kind.String == "" || r.Kind.String == OwnerKindPersonal:
		if r.Org.String != "" {
			return "", "", errors.New("a personal enrollment records an organisation")
		}
		return OwnerKindPersonal, "", nil
	case r.Kind.String == OwnerKindTeam && tunnel.ValidHubOrg(r.Org.String):
		return OwnerKindTeam, r.Org.String, nil
	}
	return "", "", errors.New("the enrollment's owner record is not understood")
}

// Get returns the enrollment without its token.
func (s *CloudStore) Get(ctx context.Context) (*CloudConnection, error) {
	var c CloudConnection
	err := s.db.GetContext(ctx, &c, "SELECT hub_url, server_id, label, state, created_at, last_error FROM cloud_connection WHERE id = 1")
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoCloudConnection
	}
	return &c, err
}

// Token returns the decrypted server token. It is for dialing only: keep it in a
// local variable, never on a struct, in a log or in an error. Errors wrapping
// ErrKeyUnreadable mean the master key is missing or wrong.
func (s *CloudStore) Token(ctx context.Context) (string, error) {
	var serverID, env string
	err := s.db.QueryRowxContext(ctx, "SELECT server_id, token FROM cloud_connection WHERE id = 1").Scan(&serverID, &env)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNoCloudConnection
	}
	if err != nil {
		return "", err
	}
	box, err := s.rs.getBox(ctx, false, false)
	if err != nil {
		return "", err
	}
	pt, err := box.Open(cloudAAD(serverID), env)
	if err != nil {
		return "", unreadable(err)
	}
	tok := string(pt)
	secretbox.Zero(pt)
	return tok, nil
}

// SetState records the connection state; msg (truncated, must not hold secrets)
// is shown in the settings page.
func (s *CloudStore) SetState(ctx context.Context, state, msg string) error {
	if len(msg) > 300 {
		msg = msg[:300]
	}
	var m any
	if msg != "" {
		m = msg
	}
	_, err := s.db.ExecContext(ctx, "UPDATE cloud_connection SET state = ?, last_error = ? WHERE id = 1", state, m)
	return err
}

// RouteKey is the route key the hub at hubURL last gave this enrollment, or "" (#3683). It is
// kept only for the hub that gave it: a key stored for another hub URL is never returned.
func (s *CloudStore) RouteKey(ctx context.Context, hubURL string) (string, error) {
	var k sql.NullString
	err := s.db.GetContext(ctx, &k, "SELECT route_key FROM cloud_connection WHERE id = 1 AND hub_url = ?", strings.TrimRight(hubURL, "/"))
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return k.String, err
}

// SetRouteKey keeps key as the route key of the enrollment with hubURL; with another hub's
// enrollment it changes nothing. The caller has checked the key's form. Never logged.
func (s *CloudStore) SetRouteKey(ctx context.Context, hubURL, key string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE cloud_connection SET route_key = ? WHERE id = 1 AND hub_url = ?", key, strings.TrimRight(hubURL, "/"))
	return err
}

// Owner is what is pinned as the Coral Hub account that owns this enrollment (Hub sign-in,
// #3966): "" when none is known yet, an account id, or OwnerBlocked after the hub named
// another account. Only a new enrollment (Save) clears it.
func (s *CloudStore) Owner(ctx context.Context) (string, error) {
	var o sql.NullString
	err := s.db.GetContext(ctx, &o, "SELECT hub_owner FROM cloud_connection WHERE id = 1")
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNoCloudConnection
	}
	return o.String, err
}

// OwnerBlocked is the owner pin after the hub named another account than the pinned one:
// Hub sign-in stays off until this computer is linked again.
const OwnerBlocked = "blocked"

// PinOwner pins account as the owner of the enrollment with serverID when none is pinned,
// and reports whether it did.
func (s *CloudStore) PinOwner(ctx context.Context, serverID, account string) (bool, error) {
	res, err := s.db.ExecContext(ctx, "UPDATE cloud_connection SET hub_owner = ? WHERE id = 1 AND server_id = ? AND hub_owner IS NULL", account, serverID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// OwnerUser is the owner's hub-user-<8 hex> label recorded at enrolment (#5280), or "" when the enrolment named none (an older hub, or an
// enrolment made before this existed). It is never set from a later request.
func (s *CloudStore) OwnerUser(ctx context.Context) (string, error) {
	var o sql.NullString
	err := s.db.GetContext(ctx, &o, "SELECT hub_owner_user FROM cloud_connection WHERE id = 1")
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNoCloudConnection
	}
	return o.String, err
}

// ServerID is the server id of the enrolment, or "" when there is none.
func (s *CloudStore) ServerID(ctx context.Context) (string, error) {
	var id string
	err := s.db.GetContext(ctx, &id, "SELECT server_id FROM cloud_connection WHERE id = 1")
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// AdoptOwnerUser records user as the owner's label of the enrollment with serverID when none is recorded, marking that the person at this computer adopted it (at, RFC 3339) rather than the
// hub's enrolment reply carrying it (#6108). It never replaces a label, and reports whether it recorded one.
func (s *CloudStore) AdoptOwnerUser(ctx context.Context, serverID, user, at string) (bool, error) {
	res, err := s.db.ExecContext(ctx, "UPDATE cloud_connection SET hub_owner_user = ?, hub_owner_adopted_at = ? WHERE id = 1 AND server_id = ? AND (hub_owner_user IS NULL OR hub_owner_user = '')", user, at, serverID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// OwnerAdoptedAt is when the owner label was adopted locally (RFC 3339), or "" when it came with the enrolment or none is recorded.
func (s *CloudStore) OwnerAdoptedAt(ctx context.Context) (string, error) {
	var at sql.NullString
	err := s.db.GetContext(ctx, &at, "SELECT hub_owner_adopted_at FROM cloud_connection WHERE id = 1")
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return at.String, err
}

// PinOwnerUser records user as the owner's label of the enrollment with serverID when none is recorded, and reports whether it did.
func (s *CloudStore) PinOwnerUser(ctx context.Context, serverID, user string) (bool, error) {
	res, err := s.db.ExecContext(ctx, "UPDATE cloud_connection SET hub_owner_user = ? WHERE id = 1 AND server_id = ? AND hub_owner_user IS NULL", user, serverID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// BlockOwner sets the pin of the enrollment with serverID to OwnerBlocked.
func (s *CloudStore) BlockOwner(ctx context.Context, serverID string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE cloud_connection SET hub_owner = ? WHERE id = 1 AND server_id = ?", OwnerBlocked, serverID)
	return err
}

// Delete forgets the enrollment and its sealed token.
func (s *CloudStore) Delete(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM cloud_connection WHERE id = 1")
	return err
}
