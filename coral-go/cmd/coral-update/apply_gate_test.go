package main

import (
	"bytes"
	"context"
	"encoding/json"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/updater/trigger"
	"github.com/subgentic/coral/internal/updater/verify"
)

// Windows asks the person at the computer (UAC) to allow the install, so the apply runs only after a click in Settings (#166, Security's N5): running
// coral-update apply without the dashboard's one-time marker answers with a word and asks Windows nothing.
func TestApplyWithoutTheInstallMarkerAsksNothing(t *testing.T) {
	dir := t.TempDir()
	assert.Equal(t, resultNotRequested, installRequest(dir, time.Now()), "run by hand")

	require.NoError(t, trigger.WriteMarker(dir, "1.5.0", time.Now()))
	assert.Equal(t, "", installRequest(dir, time.Now()), "after a click in Settings")
	assert.Equal(t, resultNotRequested, installRequest(dir, time.Now()), "the marker is good for one apply only")

	require.NoError(t, trigger.WriteMarker(dir, "1.5.0", time.Now().Add(-trigger.MarkerTTL-time.Minute)))
	assert.Equal(t, resultRequestExpired, installRequest(dir, time.Now()), "a leftover")
	assert.Equal(t, resultNotRequested, installRequest(dir, time.Now()), "and gone")
}

func TestRunApplyOnWindowsPrintsNotRequestedAndTouchesNothing(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the marker gate is Windows' (the other platforms apply without asking anyone)")
	}
	dir := t.TempDir()
	var out bytes.Buffer
	// No trusted root and no staged update: the gate must answer before either is looked at.
	require.Equal(t, 0, runApply(context.Background(), &out, dir, nil, verify.TrustSource("test")))
	var got map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	assert.Equal(t, resultNotRequested, got["result"])
}
