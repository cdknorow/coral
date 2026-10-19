package store

import (
	"context"
	"time"
)

// ── User Settings ──────────────────────────────────────────────────────

// GetSettings returns all user settings as a map.
func (s *SessionStore) GetSettings(ctx context.Context) (map[string]string, error) {
	var settings []UserSetting
	if err := s.reader().SelectContext(ctx, &settings, "SELECT key, value FROM user_settings"); err != nil {
		return nil, err
	}
	result := make(map[string]string, len(settings))
	for _, s := range settings {
		result[s.Key] = s.Value
	}
	return result, nil
}

// settingsForCLITimeout bounds GetSettingsForCLI.
const settingsForCLITimeout = 10 * time.Second

// GetSettingsForCLI is GetSettings for a caller that is about to run an agent CLI and must not
// fall back to the one on PATH when the read fails (#4350): it reads with a context the caller's
// cancellation cannot end (a client that hangs up in the middle of a team launch), bounded by
// settingsForCLITimeout so a stuck database cannot hold the launch for ever. A caller treats an
// error as "do not run anything".
func (s *SessionStore) GetSettingsForCLI(ctx context.Context) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), settingsForCLITimeout)
	defer cancel()
	return s.GetSettings(ctx)
}

// SetSetting upserts a user setting.
func (s *SessionStore) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO user_settings (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value)
	return err
}

// DeleteSetting deletes a user setting. Returns true if the key existed.
func (s *SessionStore) DeleteSetting(ctx context.Context, key string) (bool, error) {
	res, err := s.db.ExecContext(ctx, "DELETE FROM user_settings WHERE key = ?", key)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
