//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/updater/apply"
	"github.com/subgentic/coral/internal/updater/trigger"
	"github.com/subgentic/coral/internal/updater/verify"
)

// QA on #80/#82: the version the person clicked Install update for reaches the Windows apply, which refuses any other (apply's
// TestTheApplyHonoursOnlyAClickForWhatIsStaged); with no click it gets none and refuses as no_request.
func TestTheWindowsApplyGetsTheClickedVersion(t *testing.T) {
	old := clickedVersion
	t.Cleanup(func() { clickedVersion = old })

	clickedVersion = "1.5.0"
	assert.Equal(t, "1.5.0", windowsMSIConfig(t.TempDir(), nil, verify.DefaultPins()).ClickedVersion)
	clickedVersion = ""
	assert.Empty(t, windowsMSIConfig(t.TempDir(), nil, verify.DefaultPins()).ClickedVersion)
}

// noProductMSI is a computer with no Coral registered: the apply stops at its first question and nothing else of the MSI is used.
type noProductMSI struct {
	apply.MSI
	asked *bool
}

func (m noProductMSI) Install() (string, error) {
	*m.asked = true
	return "", errors.New("no Coral registered")
}

// After a click in Settings, runApply on Windows reaches the MSI apply (#162) with the clicked version, never the "unsupported_platform" of a
// platform without one.
func TestRunApplyOnWindowsReachesTheMSIApply(t *testing.T) {
	asked := false
	old := newWindowsMSI
	newWindowsMSI = func(string) apply.MSI { return noProductMSI{asked: &asked} }
	t.Cleanup(func() { newWindowsMSI = old })

	// A click in Settings: #82's one-time marker, which runApply's gate reads before the apply.
	dir := t.TempDir()
	require.NoError(t, trigger.WriteMarker(dir, "1.5.0", time.Now()))
	var out bytes.Buffer
	require.Equal(t, 0, runApply(context.Background(), &out, dir, nil, verify.TrustSource("test")))
	var got map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	assert.True(t, asked, "the MSI apply ran")
	assert.Equal(t, "1.5.0", clickedVersion, "with the clicked version")
	assert.Equal(t, apply.ResultUnsupported, got["result"])
	assert.Equal(t, "no_registered_product", got["failed_stage"])
}
