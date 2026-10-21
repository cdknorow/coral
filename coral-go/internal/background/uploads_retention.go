package background

import (
	"context"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/uploads"
)

// UploadsRetention removes uploads (images pasted or dropped for agents) older than
// uploads_retention_days, 30 by default, and trims the folder back under uploads_max_mb
// if that was lowered (#3364). Five minutes after startup, then hourly.
type UploadsRetention struct {
	ss     *store.SessionStore
	dir    string
	logger *slog.Logger

	// PurgePasted, when set, is called with the cutoff after the files are removed: the artifact copies of pasted images follow the same retention (#5284).
	PurgePasted func(ctx context.Context, cutoff time.Time)

	FirstRun, Interval time.Duration
	now                func() time.Time
}

// NewUploadsRetention returns the job for <coralDir>/uploads.
func NewUploadsRetention(db *store.DB, coralDir string) *UploadsRetention {
	return &UploadsRetention{
		ss: store.NewSessionStore(db), dir: filepath.Join(coralDir, "uploads"), logger: slog.Default(),
		FirstRun: 5 * time.Minute, Interval: time.Hour, now: time.Now,
	}
}

// Run prunes FirstRun after startup, then every Interval, until ctx is done.
func (j *UploadsRetention) Run(ctx context.Context) error {
	return runEvery(ctx, j.FirstRun, j.Interval, func() { j.RunOnce(ctx) })
}

// RunOnce removes expired uploads, then trims to the size cap, and reports how many
// files it removed for age.
func (j *UploadsRetention) RunOnce(ctx context.Context) int {
	settings, err := j.ss.GetSettings(ctx)
	if err != nil {
		settings = nil // defaults
	}
	now := j.now()
	cutoff := now.Add(-time.Duration(uploads.RetentionDays(settings)) * 24 * time.Hour)
	files, bytes, err := uploads.Prune(j.dir, cutoff)
	if err != nil {
		j.logger.Warn("uploads retention: could not read the uploads folder", "error", err)
		return 0
	}
	if files > 0 {
		j.logger.Info("uploads retention: removed expired uploads", "files", files, "bytes", bytes)
	}
	if j.PurgePasted != nil {
		j.PurgePasted(ctx, cutoff)
	}
	if err := uploads.MakeRoom(j.dir, 0, uploads.MaxBytes(settings), now); err != nil && err != uploads.ErrFull {
		j.logger.Warn("uploads retention: could not trim the uploads folder", "error", err)
	}
	return files
}
