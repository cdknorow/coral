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
	"github.com/subgentic/coral/internal/updater/status"
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
	require.NoError(t, trigger.WriteMarker(dir, "1.5.0", "", time.Now()))
	var out bytes.Buffer
	require.Equal(t, 0, runApply(context.Background(), &out, dir, nil, verify.TrustSource("test")))
	var got map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	assert.True(t, asked, "the MSI apply ran")
	assert.Equal(t, "1.5.0", clickedVersion, "with the clicked version")
	assert.Equal(t, apply.ResultUnsupported, got["result"])
	assert.Equal(t, "no_registered_product", got["failed_stage"])
}

// portableMSI is a Coral registered per machine whose folder the apply refuses (a portable copy beside a registered product, or one moved).
type portableMSI struct {
	apply.MSI
	why string
}

func (m portableMSI) Install() (string, error)           { return `C:\Users\x\coral`, nil }
func (m portableMSI) Unsupported(string) string          { return m.why }
func (m portableMSI) RegisteredProduct() (string, error) { return "", errors.New("none") }

// #5946: the Windows "Install update" quit the server first, so the apply's early answers start Coral again from the server's own folder
// (the marker's): no registered product, a portable or unreadable install. The results the MSI apply answers after it has Coral's folder start the
// old one itself (apply's own tests).
func TestRunApplyThatInstallsNothingStartsCoralFromTheMarkersFolder(t *testing.T) {
	for name, tc := range map[string]struct {
		msi   apply.MSI
		stage string
	}{
		"no registered product": {noProductMSI{asked: new(bool)}, "no_registered_product"},
		"a portable install":    {portableMSI{why: "portable_install"}, "portable_install"},
		"an unreadable install": {portableMSI{why: "unreadable_install"}, "unreadable_install"},
	} {
		t.Run(name, func(t *testing.T) {
			started := fakeStart(t, nil)
			old := newWindowsMSI
			newWindowsMSI = func(string) apply.MSI { return tc.msi }
			t.Cleanup(func() { newWindowsMSI = old })

			dir, install := t.TempDir(), installFolder(t)
			require.NoError(t, trigger.WriteMarker(dir, "1.5.0", install, time.Now()))
			var out bytes.Buffer
			require.Equal(t, 0, runApply(context.Background(), &out, dir, nil, verify.TrustSource("test")))
			var got map[string]any
			require.NoError(t, json.Unmarshal(out.Bytes(), &got))
			assert.Equal(t, apply.ResultUnsupported, got["result"])
			assert.Equal(t, tc.stage, got["failed_stage"])
			assert.Equal(t, []string{install}, *started, "Coral is started again from the server's folder")
			assert.NotContains(t, got, "restart_failed")
		})
	}
	t.Run("a start that fails is said in the answer", func(t *testing.T) {
		fakeStart(t, errors.New("no"))
		old := newWindowsMSI
		newWindowsMSI = func(string) apply.MSI { return noProductMSI{asked: new(bool)} }
		t.Cleanup(func() { newWindowsMSI = old })
		dir := t.TempDir()
		require.NoError(t, trigger.WriteMarker(dir, "1.5.0", installFolder(t), time.Now()))
		var out bytes.Buffer
		require.Equal(t, 0, runApply(context.Background(), &out, dir, nil, verify.TrustSource("test")))
		var got map[string]any
		require.NoError(t, json.Unmarshal(out.Bytes(), &got))
		assert.Equal(t, true, got["restart_failed"])
	})
}

// The status says what this install is and whether the apply can update it, as the one install object every platform fills, for the server to ask before it quits
// (trigger.Preflight reads it).
func TestStatusSaysWhetherThisInstallCanBeUpdated(t *testing.T) {
	old := newWindowsMSI
	t.Cleanup(func() { newWindowsMSI = old })
	for name, tc := range map[string]struct {
		msi  apply.MSI
		want status.Install
	}{
		"no registered product":        {noProductMSI{asked: new(bool)}, status.Install{Kind: "unregistered", Unsupported: "no_registered_product"}},
		"a portable copy":              {portableMSI{why: "portable_install"}, status.Install{Kind: "portable", Unsupported: "portable_install"}},
		"an unreadable install folder": {portableMSI{why: "unreadable_install"}, status.Install{Kind: "unwritable", Unsupported: "unreadable_install"}},
		"a registered per-machine MSI": {portableMSI{why: ""}, status.Install{Kind: "msi", Writable: true}},
	} {
		newWindowsMSI = func(string) apply.MSI { return tc.msi }
		got := probeInstall()
		require.NotNil(t, got, name)
		assert.Equal(t, tc.want, *got, name)
	}
	// and it is in the status the server reads, as the object
	newWindowsMSI = func(string) apply.MSI { return portableMSI{why: ""} }
	var out bytes.Buffer
	require.Equal(t, 0, runStatus([]string{"status", "--json"}, func(string) string { return "" }, &out))
	var got struct {
		Install *status.Install `json:"install"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	require.NotNil(t, got.Install)
	assert.Equal(t, status.Install{Kind: "msi", Writable: true}, *got.Install)
}
