package startup

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/cdknorow/coral/internal/store"
)

func newSettingsStore(t *testing.T) *store.SessionStore {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return store.NewSessionStore(db)
}

// The setting is what stands between a slow repository and a poller that
// scans it every two minutes, so every path through it has to land somewhere
// sensible rather than on zero.
func TestGitPollIntervalResolvesTheSetting(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name  string
		set   bool
		value string
		want  time.Duration
	}{
		{"unset falls back to the default", false, "", 120 * time.Second},
		{"empty falls back to the default", true, "", 120 * time.Second},
		{"whitespace falls back to the default", true, "   ", 120 * time.Second},
		{"garbage falls back to the default", true, "soon", 120 * time.Second},
		{"a configured interval is used", true, "600", 600 * time.Second},
		{"surrounding whitespace is tolerated", true, " 300 ", 300 * time.Second},
		{"zero means off, and must not read as the default", true, "0", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ss := newSettingsStore(t)
			if tc.set {
				if err := ss.SetSetting(ctx, "git_poll_interval_s", tc.value); err != nil {
					t.Fatalf("set setting: %v", err)
				}
			}
			if got := gitPollInterval(ctx, ss, 120); got != tc.want {
				t.Fatalf("gitPollInterval = %v, want %v", got, tc.want)
			}
		})
	}
}
