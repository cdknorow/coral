package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/cdknorow/coral/pkg/secretbox"
)

// Cloud connection states.
const (
	CloudStateActive  = "active"  // token usable; the connector may dial
	CloudStateRevoked = "revoked" // hub rejected the token; never retried until re-enrolled
)

// ErrNoCloudConnection is returned when this server is not enrolled.
var ErrNoCloudConnection = errors.New("not enrolled with a cloud hub")

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

// Save replaces any existing enrollment with a new, active one. The token is
// sealed before it touches the database.
func (s *CloudStore) Save(ctx context.Context, hubURL, serverID, label, token string) error {
	box, err := s.rs.getBox(ctx, true, false)
	if err != nil {
		return err
	}
	env, err := box.Seal(cloudAAD(serverID), []byte(token))
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO cloud_connection (id, hub_url, server_id, label, token, state, created_at)
		VALUES (1, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET hub_url=excluded.hub_url, server_id=excluded.server_id, label=excluded.label,
		  token=excluded.token, state=excluded.state, created_at=excluded.created_at, last_error=NULL`,
		strings.TrimRight(hubURL, "/"), serverID, label, env, CloudStateActive, nowUTC())
	return err
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

// Delete forgets the enrollment and its sealed token.
func (s *CloudStore) Delete(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM cloud_connection WHERE id = 1")
	return err
}
