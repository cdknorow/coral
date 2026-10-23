//go:build dev

package startup

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3999 (finding 7): the unauthenticated profiler says so, in the log and in the docs.
func TestPprofSaysItHasNoCredential(t *testing.T) {
	for _, want := range []string{"needs no password", "API key", "session cookies", "CORAL_PPROF_ADDR"} {
		assert.Contains(t, pprofWarning, want)
	}
	doc, err := os.ReadFile("../../agent_docs/troubleshooting.md")
	require.NoError(t, err)
	d := string(doc)
	i := strings.Index(d, "### Profiling a slow server")
	require.GreaterOrEqual(t, i, 0)
	section := d[i:]
	if j := strings.Index(section[4:], "\n### "); j >= 0 {
		section = section[:j+4]
	}
	assert.Contains(t, section, "bypasses strict local auth")
	assert.Contains(t, section, "API key and browser session cookies")
}
