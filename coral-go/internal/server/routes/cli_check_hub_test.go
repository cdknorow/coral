package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/agent"
	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/pkg/tunnel"
)

// The prerequisite check through Coral Hub names nothing of this computer's account (#4579): no resolved path, no configured path, and
// only the version number. A local caller's answer is unchanged.
func TestCLICheckThroughTheHubCarriesNoPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binary")
	}
	home := filepath.Join(t.TempDir(), "alice-account")
	bin := filepath.Join(home, ".local", "bin")
	require.NoError(t, os.MkdirAll(bin, 0o755))
	script := filepath.Join(bin, "claude")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\necho \"2.1.300 (Claude Code) at "+script+"\"\n"), 0o755))
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	h := newSystemHandlerForTest(t)
	set := func(typ, value string) {
		require.NoError(t, store.NewSessionStore(h.db).SetSetting(context.Background(), agent.CLIPathSettingKey(typ), value))
	}

	check := func(query string, tunneled bool) (map[string]any, string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/system/cli-check?"+query, nil)
		req = req.WithContext(context.WithValue(req.Context(), callerKindKey{}, callerDashCookie))
		if tunneled {
			req.Header.Set(tunnel.ViaHeader, "1")
		}
		rec := httptest.NewRecorder()
		h.CLICheck(rec, req)
		require.Equal(t, 200, rec.Code, rec.Body.String())
		var out map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		if tunneled {
			assert.NotContains(t, rec.Body.String(), "alice-account", "the reply names the account: "+rec.Body.String())
			assert.NotContains(t, rec.Body.String(), home)
		}
		return out, rec.Body.String()
	}

	t.Run("found on PATH", func(t *testing.T) {
		local, _ := check("type=claude", false)
		assert.Equal(t, script, local["path"], "a local caller still gets the path")
		assert.Contains(t, local["version"], "at "+script, "and the whole version line")
		hub, _ := check("type=claude", true)
		assert.NotContains(t, hub, "path")
		assert.Equal(t, "2.1.300", hub["version"], "only the version number")
		assert.Equal(t, true, hub["found"])
		assert.Equal(t, "available", hub["status"])
		assert.Equal(t, "ok", hub["probe"])
	})

	t.Run("a configured path that runs", func(t *testing.T) {
		set(at.Claude, script)
		local, _ := check("type=claude&source=cli_recheck", false)
		assert.Equal(t, script, local["path"])
		hub, _ := check("type=claude&source=cli_recheck", true)
		assert.NotContains(t, hub, "path")
		assert.Equal(t, "2.1.300", hub["version"])
	})

	t.Run("a configured path that cannot be run", func(t *testing.T) {
		gone := filepath.Join(home, "gone", "claude")
		set(at.Claude, gone)
		local, _ := check("type=claude", false)
		assert.Equal(t, gone, local["binary"], "a local caller is told which path")
		hub, _ := check("type=claude", true)
		assert.Equal(t, "claude", hub["binary"], "through the Hub it is the CLI's own name")
		assert.Equal(t, false, hub["found"])
		assert.Equal(t, true, hub["configured"])
		assert.Contains(t, hub["message"], "CLI path set in Settings")
		assert.NotContains(t, hub, "path")
	})

	t.Run("not installed", func(t *testing.T) {
		set(at.Claude, "")
		os.Remove(script)
		hub, _ := check("type=agy", true)
		assert.Equal(t, false, hub["found"])
		assert.Equal(t, "agy", hub["binary"])
		assert.Equal(t, "https://antigravity.google", hub["install_url"])
	})

	t.Run("a binary path is the Operator's: refused through the Hub", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/system/cli-check?type=claude&binary=/bin/sh", nil)
		req = req.WithContext(context.WithValue(req.Context(), callerKindKey{}, callerDashCookie))
		req.Header.Set(tunnel.ViaHeader, "1")
		rec := httptest.NewRecorder()
		h.CLICheck(rec, req)
		assert.Equal(t, 403, rec.Code)
		assert.Contains(t, rec.Body.String(), "operator_only")
	})
}

// The route table accepts exactly the agent types the handler knows: an unknown type would be answered as found.
func TestCLICheckTunnelTypesAreTheAgentTypes(t *testing.T) {
	known := map[string]bool{}
	for _, typ := range []string{at.Claude, at.Codex, at.Gemini, at.Agy, at.Antigravity, at.Pi} {
		known[typ] = agent.GetCLIInfo(typ) != nil
		assert.True(t, known[typ], typ+" has CLI info")
		assert.Equal(t, tunnel.TierLaunch, tunnel.Classify("GET", "/api/system/cli-check", "type="+typ), typ)
	}
	assert.Nil(t, agent.GetCLIInfo(at.Terminal), "terminal has none, so the handler would answer found")
	assert.Equal(t, tunnel.TierNever, tunnel.Classify("GET", "/api/system/cli-check", "type="+at.Terminal))
	assert.Len(t, known, 6)
}
