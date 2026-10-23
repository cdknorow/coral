package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/agent"
	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/ptr"
	"github.com/subgentic/coral/internal/store"
)

// #6417: a Codex agent started in Plan (--sandbox read-only -a on-request typed as its own flags) was recorded in mode Auto (the launch read no mode back from those flags),
// and the wake added Auto's flags after Plan's: Codex took the last, workspace-write and never.
func TestWakeKeepsTheSandboxTheAgentWasLaunchedWith(t *testing.T) {
	for _, mode := range []string{"auto", "default", ""} {
		t.Run("stored mode "+mode, func(t *testing.T) {
			_, h, term, ss := setupSessionsTestServer(t)
			ctx := context.Background()
			const id = "codex-wake-keep-0001"
			require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: id, AgentType: "codex", AgentName: "coral-go", WorkingDir: t.TempDir(),
				Flags: store.MarshalFlags([]string{"--sandbox", "read-only", "-a", "on-request"}), PermissionMode: ptr.NonEmpty(mode)}))
			require.NoError(t, ss.SetSessionSleeping(ctx, id, true))
			req := dropAt2(httptest.NewRequest(http.MethodPost, "/api/sessions/live/"+id+"/wake", nil), map[string]string{"sessionID": id})
			rec := httptest.NewRecorder()
			h.WakeSession(rec, req)
			require.Contains(t, rec.Body.String(), `"ok":true`, rec.Body.String())
			cmds := term.sentCommands()
			require.NotEmpty(t, cmds)
			cmd := cmds[len(cmds)-1]
			assert.Equal(t, 1, strings.Count(cmd, " --sandbox "), "one --sandbox: %s", flagsOnly(cmd))
			assert.Contains(t, cmd, "--sandbox read-only -a on-request", flagsOnly(cmd))
		})
	}
}

// #6423 (Security's conditions, #6417): a Codex agent started with read-only flags, in any spelling, never comes back writing, however it is brought back: Wake, restart, resume
// (and the auto-wake after a restart, which is the same wake) or a team reset. The stored mode is Auto, as the sessions of 158 and 159 have it.
func TestATypedReadOnlyNeverComesBackWritingOnAnyPath(t *testing.T) {
	spellings := map[string][]string{
		"sandbox and -a":    {"--sandbox", "read-only", "-a", "on-request"},
		"equals spellings":  {"--sandbox=read-only", "--ask-for-approval=on-request"},
		"-s and the config": {"-s", "read-only", "-c", "approval_policy=on-request"},
	}
	wrote := func(cmd string) bool {
		return strings.Contains(cmd, "workspace-write") || strings.Contains(cmd, "-a never") || strings.Contains(cmd, "danger-full-access") || strings.Contains(cmd, "--full-auto")
	}
	for name, flags := range spellings {
		flags := flags
		start := func(t *testing.T) (*agyTestRig, string) {
			rig := setupAgyTestRig(t)
			fake := stubScript(t, filepath.Join(t.TempDir(), "mock-codex"), "#!/bin/sh\nexit 0\n")
			require.NoError(t, rig.ss.SetSetting(context.Background(), agent.CLIPathSettingKey(at.Codex), fake))
			return rig, fake
		}
		register := func(t *testing.T, rig *agyTestRig, sid string) {
			require.NoError(t, rig.ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: sid, AgentType: at.Codex, AgentName: "coral-go", WorkingDir: rig.workdir,
				Flags: store.MarshalFlags(flags), PermissionMode: ptr.NonEmpty("auto")}))
		}
		post := func(t *testing.T, rig *agyTestRig, path string, body map[string]any) {
			b, _ := json.Marshal(body)
			rec := httptest.NewRecorder()
			rig.router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b)))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		}
		t.Run(name+"/wake", func(t *testing.T) {
			rig, _ := start(t)
			sid := "55555555-4444-5555-6666-000000000001"
			register(t, rig, sid)
			require.NoError(t, rig.ss.SetSessionSleeping(context.Background(), sid, true))
			ls, err := rig.ss.GetLiveSession(context.Background(), sid)
			require.NoError(t, err)
			require.NoError(t, rig.h.wakeExistingSession(context.Background(), ls))
			cmd := lastSentCommand(rig.term)
			assert.False(t, wrote(cmd), "woke writing: %s", flagsOnly(cmd))
			assert.Contains(t, cmd, "read-only")
		})
		t.Run(name+"/restart", func(t *testing.T) {
			rig, _ := start(t)
			sid := "55555555-4444-5555-6666-000000000002"
			register(t, rig, sid)
			sess := naming.SessionName(at.Codex, sid)
			require.NoError(t, rig.term.CreateSession(context.Background(), sess, rig.workdir))
			post(t, rig, "/api/sessions/live/"+sess+"/restart", map[string]any{"session_id": sid, "agent_type": at.Codex})
			cmd := lastSentCommand(rig.term)
			assert.False(t, wrote(cmd), "restarted writing: %s", flagsOnly(cmd))
			assert.Contains(t, cmd, "read-only")
		})
		t.Run(name+"/resume", func(t *testing.T) {
			rig, _ := start(t)
			sid := "55555555-4444-5555-6666-000000000003"
			register(t, rig, sid)
			sess := naming.SessionName(at.Codex, sid)
			require.NoError(t, rig.term.CreateSession(context.Background(), sess, rig.workdir))
			post(t, rig, "/api/sessions/live/"+sess+"/resume", map[string]any{"session_id": sid, "current_session_id": sid, "agent_type": at.Codex})
			cmd := lastSentCommand(rig.term)
			assert.False(t, wrote(cmd), "resumed writing: %s", flagsOnly(cmd))
			assert.Contains(t, cmd, "read-only")
		})
		t.Run(name+"/team reset", func(t *testing.T) {
			rig, _ := start(t)
			post(t, rig, "/api/sessions/launch-team", map[string]any{"agent_type": at.Codex, "board_name": "ro-team", "working_dir": rig.workdir, "flags": flags,
				"agents": []map[string]any{{"name": "worker1", "prompt": "work"}}})
			first := lastSentCommand(rig.term)
			require.Contains(t, first, "read-only", "the launch itself")
			assert.False(t, wrote(first), "launched writing: %s", flagsOnly(first))
			rCtx := chi.NewRouteContext()
			rCtx.URLParams.Add("boardName", "ro-team")
			req := httptest.NewRequest(http.MethodPost, "/api/sessions/live/team/ro-team/reset", nil)
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rCtx))
			rec := httptest.NewRecorder()
			rig.h.ResetTeam(rec, req)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			cmd := lastSentCommand(rig.term)
			assert.False(t, wrote(cmd), "reset writing: %s", flagsOnly(cmd))
			assert.Contains(t, cmd, "read-only")
		})
	}
}

// (d) Plan stays Plan after a wake for Claude and Agy as well: the stored mode is Auto (what the sessions of 158 and 159 have), the flags are Plan's.
func TestWakeKeepsPlanForClaudeAndAgy(t *testing.T) {
	for _, tc := range []struct {
		agentType string
		flags     []string
		want, not string
	}{
		{at.Claude, []string{"--permission-mode", "plan"}, "--permission-mode plan", "--permission-mode auto"},
		{at.Agy, []string{"--mode", "plan"}, "--mode plan", "accept-edits"},
	} {
		t.Run(tc.agentType, func(t *testing.T) {
			rig := setupAgyTestRig(t)
			fake := stubScript(t, filepath.Join(t.TempDir(), "mock-"+tc.agentType), "#!/bin/sh\nexit 0\n")
			require.NoError(t, rig.ss.SetSetting(context.Background(), agent.CLIPathSettingKey(tc.agentType), fake))
			sid := "66666666-4444-5555-6666-000000000001"
			require.NoError(t, rig.ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: sid, AgentType: tc.agentType, AgentName: "coral-go", WorkingDir: rig.workdir,
				Flags: store.MarshalFlags(tc.flags), PermissionMode: ptr.NonEmpty("auto")}))
			require.NoError(t, rig.ss.SetSessionSleeping(context.Background(), sid, true))
			ls, err := rig.ss.GetLiveSession(context.Background(), sid)
			require.NoError(t, err)
			require.NoError(t, rig.h.wakeExistingSession(context.Background(), ls))
			cmd := lastSentCommand(rig.term)
			assert.Contains(t, cmd, tc.want)
			assert.NotContains(t, cmd, tc.not)
		})
	}
}
