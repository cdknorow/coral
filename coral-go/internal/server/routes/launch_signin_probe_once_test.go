package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/agent"
	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/claudestub"
)

// #5838: the sign-in probe is asked for a Claude launch only, once per CLI path, and never holds the launch. The probe is replaced by a counter that records which binary it was asked
// about, so each of those three is checked on the probe itself, not on what a stand-in CLI happens to be asked.
func TestTheSignInProbeIsForClaudeOnlyOncePerCLIAndNeverHoldsALaunch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake CLIs are shell scripts")
	}
	claudestub.Isolate(t)
	rig := setupAgyTestRig(t)
	dir := t.TempDir()
	script := func(name string) string {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755))
		return p
	}
	claudeBin, otherBin := script("claude"), script("claude2")
	var calls atomic.Int32
	asked := make(chan string, 8)
	release := make(chan struct{})
	old := signInProbe
	signInProbe = func(ctx context.Context, bin, d string) { calls.Add(1); asked <- bin; <-release }
	t.Cleanup(func() { signInProbe = old; signInProbed.Delete(claudeBin); signInProbed.Delete(otherBin) })
	signInProbed.Delete(claudeBin)
	signInProbed.Delete(otherBin)

	launch := func(typ string) {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"agent_type": typ, "working_dir": rig.workdir})
		rec := httptest.NewRecorder()
		rig.router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/sessions/launch", bytes.NewReader(b)))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	}
	setPath := func(typ, p string) {
		require.NoError(t, rig.ss.SetSetting(context.Background(), agent.CLIPathSettingKey(typ), p))
	}

	setPath(at.Agy, otherBin)
	launch(at.Agy)
	time.Sleep(150 * time.Millisecond)
	assert.Zero(t, calls.Load(), "another agent's launch never asks Claude's question")

	setPath(at.Claude, claudeBin)
	done := make(chan struct{})
	go func() { launch(at.Claude); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the launch waits for the probe: it must run in the background")
	}
	select {
	case got := <-asked:
		assert.Equal(t, claudeBin, got, "the probe is asked about the launched Claude CLI")
	case <-time.After(5 * time.Second):
		t.Fatal("a Claude launch asked nothing")
	}
	launch(at.Claude)
	time.Sleep(150 * time.Millisecond)
	assert.EqualValues(t, 1, calls.Load(), "a second launch of the same CLI does not ask again")

	setPath(at.Claude, otherBin)
	launch(at.Claude)
	select {
	case got := <-asked:
		assert.Equal(t, otherBin, got, "another Claude CLI path is asked once, for itself")
	case <-time.After(5 * time.Second):
		t.Fatal("a different Claude CLI was never asked")
	}
	close(release)
}
