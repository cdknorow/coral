//go:build !dev

package startup

import (
	"bytes"
	"context"
	"log"
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #6204: a shipped Coral has no profiler. With CORAL_PPROF_ADDR set to a free loopback port, nothing listens there afterwards, and the log says the variable was ignored.
func TestAReleaseBuildStartsNoProfilerWhateverTheVariableSays(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close()) // a free port the profiler would take
	t.Setenv("CORAL_PPROF_ADDR", addr)

	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	startPprof(context.Background())

	c, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if err == nil {
		c.Close()
	}
	assert.Error(t, err, "nothing listens on the port the variable names")
	assert.Contains(t, buf.String(), "CORAL_PPROF_ADDR is ignored")
	assert.Equal(t, 1, bytes.Count(buf.Bytes(), []byte("[pprof]")), "one line")

	// unset: silent
	t.Setenv("CORAL_PPROF_ADDR", "")
	buf.Reset()
	startPprof(context.Background())
	assert.Empty(t, buf.String())
}
