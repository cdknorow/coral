package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"syscall"

	"github.com/cdknorow/coral/internal/secfile"
	"github.com/cdknorow/coral/pkg/secretbox"
)

// masterKeyGen counts master-key replacements in this process (a rotation or a re-key). Every
// RemoteServerStore (the server keeps several) reloads its cached key when it changes, so none
// seals a new secret with a key that was just replaced (#3698).
var masterKeyGen atomic.Int64

// ErrRekeyNotNeeded: the master key loads, so nothing stored is lost to it and a re-key would
// only throw secrets away.
var ErrRekeyNotNeeded = errors.New("the secret key is usable; nothing to re-key")

// ErrKeyNotRefused: the key file couldn't be read (an I/O or access error), which says nothing
// against the key itself: it may read again once that is fixed, so no re-key is offered.
var ErrKeyNotRefused = errors.New("the secret key file can't be read right now; fix that and try again (a re-key is only for a key Coral refuses)")

// keyRefused tells a refusal of the key itself from a failure to read it (Security's SHOULD on
// #3698): exposed or not private (secfile), a wrong mode, owner or file type (a refusal with no
// OS error inside), or a malformed or wrong-size key. An OS error reaching the file (EACCES, an
// I/O error), on any system, is not a refusal.
func keyRefused(err error) bool {
	if errors.Is(err, secfile.ErrExposed) || errors.Is(err, secfile.ErrNotPrivate) || errors.Is(err, secretbox.ErrKeyInvalid) {
		return true
	}
	var pe *os.PathError
	var errno syscall.Errno
	return errors.Is(err, secretbox.ErrKeyPermissions) && !errors.As(err, &pe) && !errors.As(err, &errno)
}

// SecretKeyState says whether the master key that seals the Coral Hub token and the remote
// servers' API keys loads, and what a re-key would discard.
type SecretKeyState struct {
	// State: "ok" (it loads), "missing" (none yet: one is made when needed), "env" (it comes from
	// CORAL_SECRET_KEY and is managed outside Coral), "refused" (Coral refuses the key: a re-key
	// is offered) or "unreadable" (the file couldn't be read: no re-key).
	State   string `json:"state"`
	Exposed bool   `json:"exposed"`           // refused because other accounts could read it
	Message string `json:"message,omitempty"` // why it is refused (no key material)
	// What a re-key discards: the remote servers whose API keys are stored, and the Coral Hub
	// enrollment (its hub URL), if there is one.
	RemoteServerKeys int    `json:"remote_server_keys"`
	HubURL           string `json:"hub_url,omitempty"`
}

// KeyState reports the master key's state and what a re-key would discard.
func (s *RemoteServerStore) KeyState(ctx context.Context) (SecretKeyState, error) {
	var st SecretKeyState
	box, err := secretbox.LoadKey(s.dir)
	switch {
	case err == nil && box.Source() == secretbox.SourceEnv:
		st.State = "env"
	case err == nil:
		st.State = "ok"
	case errors.Is(err, secretbox.ErrKeyMissing):
		st.State = "missing"
	case keyRefused(err):
		st.State, st.Message = "refused", err.Error()
		st.Exposed = errors.Is(err, secfile.ErrExposed)
	default:
		st.State, st.Message = "unreadable", err.Error()
	}
	if err := s.db.GetContext(ctx, &st.RemoteServerKeys, "SELECT COUNT(*) FROM remote_servers WHERE api_key != ''"); err != nil {
		return st, err
	}
	var hub []string
	if err := s.db.SelectContext(ctx, &hub, "SELECT hub_url FROM cloud_connection WHERE id = 1"); err != nil {
		return st, err
	}
	if len(hub) == 1 {
		st.HubURL = hub[0]
	}
	return st, nil
}

// Rekey replaces a master key Coral refuses (keyRefused: exposed, not private, a wrong mode or
// owner, malformed) with a new
// one, and discards every secret sealed with the old one: the remote servers' API keys (each
// server stays, marked for its key to be entered again), the Coral Hub enrollment and any
// enrollment in progress. A usable key is never replaced this way (ErrRekeyNotNeeded); one from
// CORAL_SECRET_KEY is managed outside Coral. The caller stops the Coral Hub tunnel first.
func (s *RemoteServerStore) Rekey(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	box, err := secretbox.LoadKey(s.dir)
	switch {
	case err == nil && box.Source() == secretbox.SourceEnv:
		return fmt.Errorf("the secret key comes from %s; change it there", secretbox.EnvKey)
	case err == nil:
		return ErrRekeyNotNeeded
	case errors.Is(err, secretbox.ErrKeyMissing):
		return ErrRekeyNotNeeded // none yet: one is made when needed
	case !keyRefused(err):
		return fmt.Errorf("%w: %v", ErrKeyNotRefused, err)
	}
	next, commitKey, abortKey, err := secretbox.StageRotatedKey(s.dir)
	if err != nil {
		return fmt.Errorf("make a new secret key: %w", err)
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		abortKey()
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		"UPDATE remote_servers SET api_key = '', status = '" + RemoteStatusKeyUnreadable + "', last_error = 'The API key was discarded by a re-key: enter it again' WHERE api_key != ''",
		"DELETE FROM cloud_connection",
		"DELETE FROM cloud_enrollment",
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			abortKey()
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		abortKey()
		return err
	}
	// The old secrets are gone; now the key. If the rename fails, the old key file stays refused
	// and a re-key can be run again: nothing sealed with either key is left.
	if err := commitKey(); err != nil {
		return fmt.Errorf("install the new secret key: %w", err)
	}
	s.box, s.boxGen = next, masterKeyGen.Add(1)
	return nil
}
