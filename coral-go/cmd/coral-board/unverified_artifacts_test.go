package main

import (
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func stdoutOf(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	old := os.Stdout
	os.Stdout = w
	f()
	os.Stdout = old
	require.NoError(t, w.Close())
	out, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(out)
}

// #4379: a completion whose coral://artifacts links the team never uploaded says so, by name.
func TestPrintUnverifiedArtifacts(t *testing.T) {
	out := stdoutOf(t, func() {
		printUnverifiedArtifacts([]byte(`{"title":"t","artifacts_unverified":2,"artifacts_unverified_names":["a.md","b.png"]}`))
	})
	assert.Contains(t, out, "2 artifact link(s) are not uploads of this team")
	assert.Contains(t, out, "a.md, b.png")
	assert.Contains(t, out, "coral-agent artifact upload")
	assert.Empty(t, stdoutOf(t, func() { printUnverifiedArtifacts([]byte(`{"title":"t"}`)) }), "nothing when all are verified")
}
