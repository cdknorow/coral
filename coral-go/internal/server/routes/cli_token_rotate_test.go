package routes

import (
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/store/storetest"
)

// Mutation: on a failed write in RotateCLIToken, keep the old token: after `coral reset-password` whatever read the old
// token (an agent, a copy) still signs as the Operator.
func TestRotateCLITokenFailsClosedWhenTheNewTokenCannotBeWritten(t *testing.T) {
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "c.db"))
	require.NoError(t, err)
	defer db.Close()
	dir := t.TempDir()
	h := NewCloudHandler(store.NewSessionStore(db), dir)
	nonce := base64.RawURLEncoding.EncodeToString(func() []byte { b := make([]byte, 32); _, _ = rand.Read(b); return b }())
	_, _, err = h.cliAuth.Hello(nonce)
	require.NoError(t, err, "the command line works before the reset")

	// A data dir that cannot hold a file: its parent is a regular file.
	blocker := filepath.Join(dir, "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))
	h.dataDir = filepath.Join(blocker, "data")
	h.RotateCLIToken()

	_, _, err = h.cliAuth.Hello(nonce)
	assert.Error(t, err, "no token could be written: the command line is off, not left on the old token")
	h.mu.Lock()
	assert.Empty(t, h.cliToken)
	h.mu.Unlock()
}
