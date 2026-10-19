//go:build dev

package tracking

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The default-tags test proves a release build lacks the seam; this proves the same scan does find it in a dev build.
func TestDevBuildHasTheTestSinkSeam(t *testing.T) {
	exe, err := os.Executable()
	require.NoError(t, err)
	bin, err := os.ReadFile(exe)
	require.NoError(t, err)
	assert.True(t, bytes.Contains(bin, []byte(strings.Join([]string{"CORAL_TELEMETRY", "TEST", "SINK"}, "_"))))
}
