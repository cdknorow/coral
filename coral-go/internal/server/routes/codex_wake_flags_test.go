package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/ptr"
	"github.com/cdknorow/coral/internal/store"
)

// #5222 (found with real Codex 0.163): a Codex agent in mode auto, dontAsk, acceptEdits or plan, woken after an earlier restart or resume, typed
// "--sandbox workspace-write -a never" twice, because the restart had stored the expanded flags in the session's flags and the wake added the mode's
// again. Codex refuses a repeated --sandbox and the agent ended at a bare shell.
func TestWakeOfACodexAgentNeverTypesTheSandboxFlagsTwice(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		stored     []string
		want       string
	}{
		{"auto, the expanded flags stored by a restart", "auto", []string{"--sandbox", "workspace-write", "-a", "never"}, "--sandbox workspace-write -a never"},
		{"dontAsk, the same", "dontAsk", []string{"--sandbox", "workspace-write", "-a", "never"}, "--sandbox workspace-write -a never"},
		{"acceptEdits, stored", "acceptEdits", []string{"--sandbox", "workspace-write", "-a", "on-request"}, "--sandbox workspace-write -a on-request"},
		{"plan, stored", "plan", []string{"--sandbox", "read-only", "-a", "on-request"}, "--sandbox read-only -a on-request"},
		{"auto, nothing stored in the flags", "auto", nil, "--sandbox workspace-write -a never"},
		{"plan, a model flag stored", "plan", []string{"--model", "gpt-5-codex", "--sandbox", "read-only", "-a", "on-request"}, "--sandbox read-only -a on-request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, h, term, ss := setupSessionsTestServer(t)
			ctx := context.Background()
			const id = "codex-wake-flags-0001"
			require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: id, AgentType: "codex", AgentName: "coral-go", WorkingDir: t.TempDir(),
				Flags: store.MarshalFlags(tc.stored), PermissionMode: ptr.NonEmpty(tc.mode)}))
			require.NoError(t, ss.SetSessionSleeping(ctx, id, true))
			req := dropAt2(httptest.NewRequest(http.MethodPost, "/api/sessions/live/"+id+"/wake", nil), map[string]string{"sessionID": id})
			rec := httptest.NewRecorder()
			h.WakeSession(rec, req)
			require.Contains(t, rec.Body.String(), `"ok":true`, rec.Body.String())
			cmds := term.sentCommands()
			require.NotEmpty(t, cmds, "the agent's start command was typed")
			cmd := cmds[len(cmds)-1]
			assert.Equal(t, 1, strings.Count(cmd, " --sandbox "), "one --sandbox: %s", flagsOnly(cmd))
			assert.Equal(t, 1, strings.Count(cmd, " -a "), "one -a: %s", flagsOnly(cmd))
			assert.Contains(t, cmd, tc.want)
		})
	}
}

// flagsOnly is the permission part of a command, for a failure message (the command itself holds the session's token).
func flagsOnly(cmd string) string {
	var out []string
	f := strings.Fields(cmd)
	for i, w := range f {
		if (w == "--sandbox" || w == "-a") && i+1 < len(f) {
			out = append(out, w+" "+f[i+1])
		}
	}
	return strings.Join(out, " ")
}

// A Codex agent that never completed a turn has no saved session ("codex resume" ends at "No saved session found" and a bare shell, found with real Codex): its wake
// starts a new one. One that has a saved session is resumed.
func TestWakeOfACodexAgentWithNoSavedSessionStartsANewOne(t *testing.T) {
	for _, saved := range []bool{false, true} {
		name := map[bool]string{false: "no saved session: a new one", true: "a saved session: resumed"}[saved]
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("CODEX_HOME", home)
			_, h, term, ss := setupSessionsTestServer(t)
			ctx := context.Background()
			const id = "33333333-2222-3333-4444-555555555555"
			if saved {
				dir := home + "/sessions/2026/10/19"
				require.NoError(t, os.MkdirAll(dir, 0o700))
				require.NoError(t, os.WriteFile(dir+"/rollout-2026-10-19T10-00-00-"+id+".jsonl", []byte(`{"type":"session_meta"}`+"\n"), 0o600))
			}
			require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: id, AgentType: "codex", AgentName: "coral-go", WorkingDir: t.TempDir(), PermissionMode: ptr.NonEmpty("auto")}))
			require.NoError(t, ss.SetSessionSleeping(ctx, id, true))
			req := dropAt2(httptest.NewRequest(http.MethodPost, "/api/sessions/live/"+id+"/wake", nil), map[string]string{"sessionID": id})
			rec := httptest.NewRecorder()
			h.WakeSession(rec, req)
			require.Contains(t, rec.Body.String(), `"ok":true`, rec.Body.String())
			cmds := term.sentCommands()
			require.NotEmpty(t, cmds)
			cmd := cmds[len(cmds)-1]
			if saved {
				assert.Contains(t, cmd, " resume "+id, "resumed")
			} else {
				assert.NotContains(t, cmd, " resume ", "a new agent, not a resume of nothing")
			}
			assert.Contains(t, cmd, "--sandbox workspace-write -a never", "the permission mode is kept either way")
		})
	}
}
