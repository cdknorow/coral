package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// CloudEnrollment is the Coral Hub enrollment in progress and its start rate limit,
// kept across restarts (#3285 follow-up). DeviceCode is a secret: sealed at rest.
type CloudEnrollment struct {
	HubURL          string
	DeviceCode      string // "" when no code is pending
	UserCode        string
	VerificationURL string
	Interval        time.Duration
	ExpiresAt       time.Time
	// NextStartAt is the earliest a new device code may be asked for; Failures counts
	// consecutive failed starts (for the backoff).
	NextStartAt time.Time
	Failures    int
}

type cloudEnrollmentRow struct {
	HubURL          string `db:"hub_url"`
	DeviceCode      string `db:"device_code"`
	UserCode        string `db:"user_code"`
	VerificationURL string `db:"verification_url"`
	IntervalS       int    `db:"interval_s"`
	ExpiresAt       string `db:"expires_at"`
	NextStartAt     string `db:"next_start_at"`
	Failures        int    `db:"failures"`
}

func enrollAAD(hubURL string) string { return "cloud-enrollment:" + hubURL }

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// GetEnrollment returns the stored enrollment state for hubURL, or nil if none (or
// it was for another hub). A device code that can't be unsealed reads as none.
func (s *CloudStore) GetEnrollment(ctx context.Context, hubURL string) (*CloudEnrollment, error) {
	var r cloudEnrollmentRow
	err := s.db.GetContext(ctx, &r, `SELECT hub_url, device_code, user_code, verification_url, interval_s, expires_at, next_start_at, failures FROM cloud_enrollment WHERE id = 1`)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if r.HubURL != hubURL {
		return nil, nil
	}
	e := &CloudEnrollment{HubURL: r.HubURL, UserCode: r.UserCode, VerificationURL: r.VerificationURL,
		Interval: time.Duration(r.IntervalS) * time.Second, ExpiresAt: parseTime(r.ExpiresAt),
		NextStartAt: parseTime(r.NextStartAt), Failures: r.Failures}
	if r.DeviceCode != "" {
		if box, err := s.rs.getBox(ctx, false, false); err == nil {
			if pt, err := box.Open(enrollAAD(hubURL), r.DeviceCode); err == nil {
				e.DeviceCode = string(pt)
			}
		}
	}
	if e.DeviceCode == "" {
		e.UserCode, e.VerificationURL, e.ExpiresAt = "", "", time.Time{}
	}
	return e, nil
}

// SaveEnrollment replaces the stored enrollment state; the device code is sealed.
func (s *CloudStore) SaveEnrollment(ctx context.Context, e *CloudEnrollment) error {
	sealed := ""
	if e.DeviceCode != "" {
		box, err := s.rs.getBox(ctx, true, false)
		if err != nil {
			return err
		}
		if sealed, err = box.Seal(enrollAAD(e.HubURL), []byte(e.DeviceCode)); err != nil {
			return err
		}
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO cloud_enrollment (id, hub_url, device_code, user_code, verification_url, interval_s, expires_at, next_start_at, failures)
		VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET hub_url=excluded.hub_url, device_code=excluded.device_code, user_code=excluded.user_code,
		  verification_url=excluded.verification_url, interval_s=excluded.interval_s, expires_at=excluded.expires_at,
		  next_start_at=excluded.next_start_at, failures=excluded.failures`,
		e.HubURL, sealed, e.UserCode, e.VerificationURL, int(e.Interval/time.Second), formatTime(e.ExpiresAt), formatTime(e.NextStartAt), e.Failures)
	return err
}

// ClearEnrollment forgets the pending code and the rate limit (enrolled, or disconnected).
func (s *CloudStore) ClearEnrollment(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM cloud_enrollment`)
	return err
}
