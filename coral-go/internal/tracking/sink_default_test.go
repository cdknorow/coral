//go:build !dev

package tracking

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The test sink (sink_dev.go) lets a dev build send its events to a local capture endpoint. A release build
// must have no way to be redirected: neither the env var's name nor the file may be in a default-tags build (#4935).
// The names are joined at run time so this test's own binary does not contain them as literals.
func TestDefaultBuildHasNoTestSinkSeam(t *testing.T) {
	envName := strings.Join([]string{"CORAL_TELEMETRY", "TEST", "SINK"}, "_")
	exe, err := os.Executable()
	require.NoError(t, err)
	bin, err := os.ReadFile(exe) // this test binary is the tracking package built with the default tags
	require.NoError(t, err)

	assert.True(t, bytes.Contains(bin, []byte("dashboard_active_day")), "control: the scan must be able to find a string this package does contain")
	assert.False(t, bytes.Contains(bin, []byte(envName)), "%s is in a default-tags build: the test sink seam must be dev-only", envName)

	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), "_dev.go") {
			src, err := os.ReadFile(e.Name())
			require.NoError(t, err)
			assert.True(t, bytes.HasPrefix(src, []byte("//go:build dev")), "%s has no dev build constraint, so it would compile into releases", e.Name())
		}
	}
}
