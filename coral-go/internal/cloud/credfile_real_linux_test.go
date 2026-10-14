package cloud

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The real well-known location, no test seams: root-owned /run and /run/coral,
// a tmpfs bootstrap.d owned by this user. Runs only where that layout was made
// for it (CORAL_TEST_REAL_BOOTSTRAP=1, e.g. a container started with
// --tmpfs /run/coral/bootstrap.d:mode=0700,uid=<uid> and the credential written).
func TestRealWellKnownBootstrap(t *testing.T) {
	if os.Getenv("CORAL_TEST_REAL_BOOTSTRAP") != "1" {
		t.Skip("needs the real /run/coral/bootstrap.d layout")
	}
	cred, found, err := ReadWellKnownBootstrap()
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, os.Getenv("CORAL_TEST_REAL_CREDENTIAL"), string(cred))
	_, statErr := os.Lstat("/run/coral/bootstrap.d/credential")
	assert.True(t, os.IsNotExist(statErr), "removed after the read")
}
