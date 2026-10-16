//go:build !windows

package agentprio

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// #3999 (finding 4): the ps behind the priority loops is killed at psTimeout.
func TestPsPairsGivesUpOnAHungPs(t *testing.T) {
	dir := t.TempDir()
	assert.NoError(t, os.WriteFile(filepath.Join(dir, "ps"), []byte("#!/bin/sh\nexec sleep 30\n"), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	old := psTimeout
	psTimeout = 300 * time.Millisecond
	t.Cleanup(func() { psTimeout = old })
	start := time.Now()
	assert.Empty(t, psPairs("ppid"))
	assert.Less(t, time.Since(start), 10*time.Second)
}
