package stallwatch

import (
	"context"
	"database/sql"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// A transaction that never ends holds SQLite's single connection: the DB probe waits
// behind it, and the watchdog dumps.
func TestDBProbeStuckBehindATransaction(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "x.db"))
	require.NoError(t, err)
	defer db.Close()
	db.SetMaxOpenConns(1)
	ping := func(ctx context.Context) error { var one int; return db.QueryRowContext(ctx, "SELECT 1").Scan(&one) }
	require.NoError(t, ping(context.Background()))
	tx, err := db.Begin()
	require.NoError(t, err)
	defer tx.Rollback()

	dir := t.TempDir()
	c := newClock()
	w := New(Config{Dir: dir, Threshold: 30 * time.Second, Probes: []Probe{DBProbe("db", ping)}, now: c.now})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Tick(ctx)
	c.add(31 * time.Second)
	path := w.Tick(ctx)
	require.NotEmpty(t, path)
	b, _ := os.ReadFile(path)
	assert.Contains(t, string(b), "Stuck: db (no answer for 31s)")
}

// The HTTP probe goes through the handler as a loopback GET /api/health.
func TestHTTPProbe(t *testing.T) {
	var got *http.Request
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = r; w.Write([]byte("ok")) })
	require.NoError(t, HTTPProbe(h).Check(context.Background()))
	require.NotNil(t, got)
	assert.Equal(t, "/api/health", got.URL.Path)
	assert.Equal(t, "127.0.0.1:1", got.RemoteAddr)
}
