package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/cdknorow/coral/internal/secretbox"
)

// Remote server status values.
const (
	RemoteStatusUnknown         = "unknown"
	RemoteStatusOnline          = "online"
	RemoteStatusUnreachable     = "unreachable"
	RemoteStatusUnauthorized    = "unauthorized"
	RemoteStatusKeyUnreadable   = "key_unreadable"
	RemoteStatusVersionMismatch = "version_mismatch"
)

// LocalServerID is reserved for the hub itself.
const LocalServerID = "local"

var (
	// ErrRemoteNotFound is returned when a server id is not registered.
	ErrRemoteNotFound = errors.New("remote server not found")
	// ErrRemoteExists is returned when adding a duplicate id.
	ErrRemoteExists = errors.New("remote server already exists")
	// ErrKeyUnreadable wraps any failure to obtain or use the master key or to
	// decrypt a stored key. Callers map it to status key_unreadable.
	ErrKeyUnreadable = errors.New("stored remote key is unreadable")
	// ErrInvalidRemoteID is returned for ids that are not valid slugs.
	ErrInvalidRemoteID = errors.New("id must match [a-z0-9-]{1,32} and not be \"local\"")

	remoteIDRe = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
)

// ValidateRemoteID checks the slug rules.
func ValidateRemoteID(id string) error {
	if id == LocalServerID || !remoteIDRe.MatchString(id) {
		return ErrInvalidRemoteID
	}
	return nil
}

// RemoteServer is the safe view of a registered server. It never carries the
// API key, plaintext or encrypted.
type RemoteServer struct {
	ID           string  `json:"id" db:"id"`
	Label        string  `json:"label" db:"label"`
	URL          string  `json:"url" db:"url"`
	AllowPrivate bool    `json:"allow_private" db:"allow_private"`
	Status       string  `json:"status" db:"status"`
	CreatedAt    string  `json:"created_at" db:"created_at"`
	LastSeen     *string `json:"last_seen" db:"last_seen"`
	LastError    *string `json:"last_error" db:"last_error"`
}

// RemoteServerStore manages the remote_servers table. API keys are encrypted
// with a master key that lives outside the database (see internal/secretbox).
type RemoteServerStore struct {
	db  *DB
	dir string // Coral data dir holding .remote_secret_key

	mu  sync.Mutex
	box *secretbox.Box

	// rotateFailAfter, when > 0, makes RotateMasterKey fail after that many
	// row updates (test hook for mid-rotation failure).
	rotateFailAfter int
}

// NewRemoteServerStore creates a store. coralDir is the Coral data directory
// where the master key file lives. No key is read or created until needed.
func NewRemoteServerStore(db *DB, coralDir string) *RemoteServerStore {
	return &RemoteServerStore{db: db, dir: coralDir}
}

func unreadable(err error) error {
	return fmt.Errorf("%w: %w", ErrKeyUnreadable, err)
}

func (s *RemoteServerStore) rowCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.GetContext(ctx, &n, "SELECT COUNT(*) FROM remote_servers")
	return n, err
}

// getBox returns the cached box or loads the key. When no key exists and
// allowCreate is true and no encrypted rows exist, a new key file is created.
// A missing key with existing rows never creates a key unless forceCreate.
func (s *RemoteServerStore) getBox(ctx context.Context, allowCreate, forceCreate bool) (*secretbox.Box, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.box != nil {
		return s.box, nil
	}
	box, err := secretbox.LoadKey(s.dir)
	if err == nil {
		s.box = box
		return box, nil
	}
	if !errors.Is(err, secretbox.ErrKeyMissing) || !allowCreate {
		return nil, unreadable(err)
	}
	n, cerr := s.rowCount(ctx)
	if cerr != nil {
		return nil, cerr
	}
	if n > 0 && !forceCreate {
		return nil, unreadable(err)
	}
	box, err = secretbox.CreateKeyFile(s.dir)
	if err != nil {
		return nil, unreadable(err)
	}
	s.box = box
	return box, nil
}

// Add registers a server, encrypting apiKey. Creates the master key on first
// use only if no encrypted rows exist.
func (s *RemoteServerStore) Add(ctx context.Context, id, label, url, apiKey string, allowPrivate bool) (*RemoteServer, error) {
	if err := ValidateRemoteID(id); err != nil {
		return nil, err
	}
	box, err := s.getBox(ctx, true, false)
	if err != nil {
		return nil, err
	}
	env, err := box.Seal(id, []byte(apiKey))
	if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO remote_servers (id, label, url, api_key, allow_private, status, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, label, strings.TrimRight(url, "/"), env, allowPrivate, RemoteStatusUnknown, nowUTC())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") || strings.Contains(err.Error(), "PRIMARY KEY") {
			return nil, ErrRemoteExists
		}
		return nil, err
	}
	return s.Get(ctx, id)
}

// RemoteServerUpdate holds optional edits; nil fields are left unchanged.
// APIKey is write-only: setting it re-encrypts it, which also recovers a
// server whose stored key was unreadable.
type RemoteServerUpdate struct {
	Label        *string
	URL          *string
	APIKey       *string
	AllowPrivate *bool
}

// Update applies edits to a server.
func (s *RemoteServerStore) Update(ctx context.Context, id string, u RemoteServerUpdate) (*RemoteServer, error) {
	if _, err := s.Get(ctx, id); err != nil {
		return nil, err
	}
	if u.Label != nil {
		if _, err := s.db.ExecContext(ctx, "UPDATE remote_servers SET label = ? WHERE id = ?", *u.Label, id); err != nil {
			return nil, err
		}
	}
	if u.URL != nil {
		if _, err := s.db.ExecContext(ctx, "UPDATE remote_servers SET url = ?, status = ?, last_error = NULL WHERE id = ?",
			strings.TrimRight(*u.URL, "/"), RemoteStatusUnknown, id); err != nil {
			return nil, err
		}
	}
	if u.AllowPrivate != nil {
		if _, err := s.db.ExecContext(ctx, "UPDATE remote_servers SET allow_private = ? WHERE id = ?", *u.AllowPrivate, id); err != nil {
			return nil, err
		}
	}
	if u.APIKey != nil {
		// Re-entering a key is the explicit recovery path, so a lost key
		// file may be replaced here (other rows were already unreadable).
		box, err := s.getBox(ctx, true, true)
		if err != nil {
			return nil, err
		}
		env, err := box.Seal(id, []byte(*u.APIKey))
		if err != nil {
			return nil, err
		}
		if _, err := s.db.ExecContext(ctx, "UPDATE remote_servers SET api_key = ?, status = ?, last_error = NULL WHERE id = ?",
			env, RemoteStatusUnknown, id); err != nil {
			return nil, err
		}
	}
	return s.Get(ctx, id)
}

// Delete removes a server. Returns ErrRemoteNotFound if absent.
func (s *RemoteServerStore) Delete(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM remote_servers WHERE id = ?", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrRemoteNotFound
	}
	return nil
}

const remoteCols = "id, label, url, allow_private, status, created_at, last_seen, last_error"

// Get returns one server (without its key). Status is key_unreadable when the
// stored key cannot be decrypted.
func (s *RemoteServerStore) Get(ctx context.Context, id string) (*RemoteServer, error) {
	var rs RemoteServer
	err := s.db.GetContext(ctx, &rs, "SELECT "+remoteCols+" FROM remote_servers WHERE id = ?", id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRemoteNotFound
	}
	if err != nil {
		return nil, err
	}
	if _, kerr := s.DecryptedKey(ctx, id); kerr != nil && errors.Is(kerr, ErrKeyUnreadable) {
		rs.Status = RemoteStatusKeyUnreadable
	}
	return &rs, nil
}

// List returns all servers ordered by creation, without keys. Servers whose
// key cannot be decrypted get status key_unreadable; others are unaffected.
func (s *RemoteServerStore) List(ctx context.Context) ([]RemoteServer, error) {
	var out []RemoteServer
	if err := s.db.SelectContext(ctx, &out, "SELECT "+remoteCols+" FROM remote_servers ORDER BY created_at, id"); err != nil {
		return nil, err
	}
	if out == nil {
		out = []RemoteServer{}
	}
	for i := range out {
		if _, err := s.DecryptedKey(ctx, out[i].ID); err != nil && errors.Is(err, ErrKeyUnreadable) {
			out[i].Status = RemoteStatusKeyUnreadable
		}
	}
	return out, nil
}

// DecryptedKey returns the remote's API key. It is for dialing only: use it
// into a local variable, never store it on a struct, log it, or put it in an
// error. Errors wrapping ErrKeyUnreadable mean the key cannot be decrypted.
func (s *RemoteServerStore) DecryptedKey(ctx context.Context, id string) (string, error) {
	var env string
	err := s.db.GetContext(ctx, &env, "SELECT api_key FROM remote_servers WHERE id = ?", id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrRemoteNotFound
	}
	if err != nil {
		return "", err
	}
	box, err := s.getBox(ctx, false, false)
	if err != nil {
		return "", err
	}
	pt, err := box.Open(id, env)
	if err != nil {
		return "", unreadable(err)
	}
	key := string(pt)
	secretbox.Zero(pt)
	return key, nil
}

// SetStatus records the outcome of a contact attempt. status online sets
// last_seen and clears last_error; other statuses record errMsg (truncated).
// errMsg must not contain key material.
func (s *RemoteServerStore) SetStatus(ctx context.Context, id, status, errMsg string) error {
	if len(errMsg) > 500 {
		errMsg = errMsg[:500]
	}
	if status == RemoteStatusOnline {
		_, err := s.db.ExecContext(ctx,
			"UPDATE remote_servers SET status = ?, last_seen = ?, last_error = NULL WHERE id = ?", status, nowUTC(), id)
		return err
	}
	var e any
	if errMsg != "" {
		e = errMsg
	}
	_, err := s.db.ExecContext(ctx, "UPDATE remote_servers SET status = ?, last_error = ? WHERE id = ?", status, e, id)
	return err
}

// RotateMasterKey generates a new master key and re-encrypts every row in one
// transaction. On any failure the old key file and rows remain valid. Not
// supported when the key comes from CORAL_SECRET_KEY.
func (s *RemoteServerStore) RotateMasterKey(ctx context.Context) error {
	old, err := s.getBox(ctx, false, false)
	if err != nil {
		return err
	}
	if old.Source() == secretbox.SourceEnv {
		return fmt.Errorf("master key comes from %s; rotate it externally", secretbox.EnvKey)
	}
	next, commitKey, abortKey, err := secretbox.StageRotatedKey(s.dir)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			abortKey()
		}
	}()

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var rows []struct {
		ID     string `db:"id"`
		APIKey string `db:"api_key"`
	}
	if err := tx.SelectContext(ctx, &rows, "SELECT id, api_key FROM remote_servers"); err != nil {
		return err
	}
	in := make([]secretbox.Row, len(rows))
	for i, r := range rows {
		in[i] = secretbox.Row{AAD: r.ID, Envelope: r.APIKey}
	}
	out, err := secretbox.Reencrypt(old, next, in)
	if err != nil {
		return err
	}
	for i, r := range out {
		if s.rotateFailAfter > 0 && i >= s.rotateFailAfter {
			return errors.New("simulated rotation failure")
		}
		if _, err := tx.ExecContext(ctx, "UPDATE remote_servers SET api_key = ? WHERE id = ?", r.Envelope, r.AAD); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	if err := commitKey(); err != nil {
		// Rows are now under the new key but the file still holds the old
		// one. Roll the rows back to the old key.
		abortKey()
		back, rerr := secretbox.Reencrypt(next, old, out)
		if rerr == nil {
			if tx2, terr := s.db.BeginTxx(ctx, nil); terr == nil {
				for _, r := range back {
					tx2.ExecContext(ctx, "UPDATE remote_servers SET api_key = ? WHERE id = ?", r.Envelope, r.AAD)
				}
				tx2.Commit()
			}
		}
		return fmt.Errorf("install rotated key: %w", err)
	}
	s.mu.Lock()
	s.box = next
	s.mu.Unlock()
	return nil
}
