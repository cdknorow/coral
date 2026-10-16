package cloud

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/store/storetest"
	"github.com/cdknorow/coral/pkg/secretbox"
	"github.com/cdknorow/coral/pkg/tunnel"
)

// #3693: when the token can't be stored here (on Windows every existing key file was refused),
// enrolling first left an enrollment in Coral Hub with no connection. Now the store is proved
// first: with an unusable key file, Connect fails, says why, and the hub hears nothing.
func TestConnectChecksTheSecretStoreBeforeEnrolling(t *testing.T) {
	t.Setenv(secretbox.EnvKey, "")
	dir := t.TempDir()
	db, err := storetest.Open(t, filepath.Join(dir, "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, os.WriteFile(secretbox.KeyFilePath(dir), []byte("not a key\n"), 0o600))
	e := &env{db: db, cs: store.NewCloudStore(db, store.NewRemoteServerStore(db, dir)), hub: newFakeHub(t), local: newLocalAPI(t), logs: &bytes.Buffer{}}
	e.mode.Store(int32(tunnel.TierView))
	m := e.manager(e.hub.srv.URL)
	start(t, m)

	err = m.Connect(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrKeyUnreadable)
	st := m.Status()
	assert.Equal(t, StateIdle, st.State)
	assert.Contains(t, st.LastError, "nothing was enrolled")
	assert.EqualValues(t, 0, e.hub.starts.Load(), "no enrollment was started")
	assert.EqualValues(t, 0, e.hub.polls.Load())
}

// If the token still can't be saved after the hub enrolled, the status says so and what to do,
// and neither it nor the log holds the token.
func TestASaveFailureAfterEnrollingSaysSoWithoutTheToken(t *testing.T) {
	e := newEnv(t)
	e.hub.approveAt.Store(5)
	m := e.manager(e.hub.srv.URL)
	start(t, m)
	require.NoError(t, m.Connect(context.Background()))
	_, err := e.db.ExecContext(context.Background(), "DROP TABLE cloud_connection") // Save will fail
	require.NoError(t, err)

	require.Eventually(t, func() bool { return strings.Contains(m.Status().LastError, "Remove this computer in Coral Hub") },
		10*time.Second, 10*time.Millisecond, "last status %+v", m.Status())
	st := m.Status()
	assert.Equal(t, StateIdle, st.State)
	assert.Contains(t, st.LastError, "the token could not be stored here")
	assert.NotContains(t, st.LastError, fakeToken)
	assert.NotContains(t, e.logs.String(), fakeToken)
	assert.EqualValues(t, 0, e.hub.tunnels.Load(), "never dialed without a stored token")
}
