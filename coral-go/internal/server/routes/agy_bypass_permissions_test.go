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
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/agent"
	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
)

type agyTestRig struct {
	h       *SessionsHandler
	term    *mockSessionTerminal
	ss      *store.SessionStore
	cfg     *config.Config
	workdir string
	fakeAgy string
	router  *chi.Mux
}

func setupAgyTestRig(t *testing.T) *agyTestRig {
	dataDir := t.TempDir()
	cfg := config.Load(dataDir)
	cfg.LogDir = filepath.Join(dataDir, "logs")
	require.NoError(t, os.MkdirAll(cfg.LogDir, 0o700))

	binDir := t.TempDir()
	fakeAgy := filepath.Join(binDir, "agy")
	require.NoError(t, os.WriteFile(fakeAgy, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	if runtime.GOOS == "windows" {
		require.NoError(t, os.WriteFile(filepath.Join(binDir, "agy.bat"), []byte("@exit /b 0\r\n"), 0o755))
	}

	// Ensure PATH has fakeAgy prepended and strips any real agy executable from host PATH (#4354).
	// Note: stripping a host PATH directory that contains agy can remove other tools installed
	// alongside it for the test, which is intentional to prevent tests from reaching host CLIs.
	var cleanPath []string
	cleanPath = append(cleanPath, binDir)
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if p == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(p, "agy")); err == nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(p, "agy.exe")); err == nil {
			continue
		}
		cleanPath = append(cleanPath, p)
	}
	t.Setenv("PATH", strings.Join(cleanPath, string(os.PathListSeparator)))

	db, err := storetest.Open(t, filepath.Join(dataDir, "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	term := newMockTerminal()
	h := NewSessionsHandler(db, cfg, nil, term, nil)
	ss := store.NewSessionStore(db)
	require.NoError(t, ss.SetSetting(context.Background(), agent.CLIPathSettingKey(at.Agy), fakeAgy))

	r := chi.NewRouter()
	r.Post("/api/sessions/launch", h.Launch)
	r.Post("/api/sessions/live/{name}/restart", h.Restart)
	r.Post("/api/sessions/live/{name}/resume", h.Resume)
	r.Post("/api/sessions/live/{name}/wake", h.Wake)
	r.Post("/api/sessions/launch-team", h.LaunchTeam)
	r.Post("/api/sessions/live/team/{boardName}/reset", h.ResetTeam)

	return &agyTestRig{
		h:       h,
		term:    term,
		ss:      ss,
		cfg:     cfg,
		workdir: dataDir,
		fakeAgy: fakeAgy,
		router:  r,
	}
}

func lastSentCommand(term *mockSessionTerminal) string {
	term.mu.Lock()
	defer term.mu.Unlock()
	var all []string
	for _, cmds := range term.sent {
		all = append(all, cmds...)
	}
	if len(all) == 0 {
		return ""
	}
	return all[len(all)-1]
}

// 1. Fresh launch
func TestAgyBypassPermissions_Path1_FreshLaunch(t *testing.T) {
	rig := setupAgyTestRig(t)

	customCLI := stubScript(t, filepath.Join(t.TempDir(), "mock-agy-bin"), "#!/bin/sh\nexit 0\n")
	require.NoError(t, rig.h.ss.SetSetting(context.Background(), "cli_path_agy", customCLI))

	body, _ := json.Marshal(map[string]any{
		"agent_type":  at.Agy,
		"working_dir": rig.workdir,
		"flags":       []string{"--dangerously-skip-permissions"},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/launch", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	rig.router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	cmd := lastSentCommand(rig.term)
	assert.Contains(t, cmd, customCLI, "fresh launch must use the configured custom CLI path")
	assert.Contains(t, cmd, "--dangerously-skip-permissions", "fresh launch must include --dangerously-skip-permissions")
}

// 2. Restart from agent menu
func TestAgyBypassPermissions_Path2_Restart(t *testing.T) {
	rig := setupAgyTestRig(t)
	sid := "11111111-2222-3333-4444-555555555555"
	name := naming.SessionName(at.Agy, sid)

	require.NoError(t, rig.term.CreateSession(context.Background(), name, rig.workdir))
	require.NoError(t, rig.ss.RegisterLiveSession(context.Background(), &store.LiveSession{
		SessionID:  sid,
		AgentType:  at.Agy,
		AgentName:  filepath.Base(rig.workdir),
		WorkingDir: rig.workdir,
		Flags:      store.MarshalFlags([]string{"--dangerously-skip-permissions"}),
	}))

	body, _ := json.Marshal(map[string]any{
		"session_id": sid,
		"agent_type": at.Agy,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/live/"+name+"/restart", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	rig.router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	cmd := lastSentCommand(rig.term)
	assert.Contains(t, cmd, "--dangerously-skip-permissions", "restarted agy must keep --dangerously-skip-permissions")
}

// 3. Resume of ended session
func TestAgyBypassPermissions_Path3_Resume(t *testing.T) {
	rig := setupAgyTestRig(t)
	sid := "22222222-3333-4444-5555-666666666666"
	name := naming.SessionName(at.Agy, sid)

	require.NoError(t, rig.term.CreateSession(context.Background(), name, rig.workdir))
	require.NoError(t, rig.ss.RegisterLiveSession(context.Background(), &store.LiveSession{
		SessionID:  sid,
		AgentType:  at.Agy,
		AgentName:  filepath.Base(rig.workdir),
		WorkingDir: rig.workdir,
		Flags:      store.MarshalFlags([]string{"--dangerously-skip-permissions"}),
	}))

	body, _ := json.Marshal(map[string]any{
		"session_id":         sid,
		"current_session_id": sid,
		"agent_type":         at.Agy,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/live/"+name+"/resume", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	rig.router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	cmd := lastSentCommand(rig.term)
	assert.Contains(t, cmd, "--dangerously-skip-permissions", "resumed agy must keep --dangerously-skip-permissions")
	assert.Contains(t, cmd, "--conversation", "resumed agy must have --conversation")
}

// 4. Waking a sleeping agent
func TestAgyBypassPermissions_Path4_Wake(t *testing.T) {
	rig := setupAgyTestRig(t)
	sid := "33333333-4444-5555-6666-777777777777"

	require.NoError(t, rig.ss.RegisterLiveSession(context.Background(), &store.LiveSession{
		SessionID:  sid,
		AgentType:  at.Agy,
		AgentName:  filepath.Base(rig.workdir),
		WorkingDir: rig.workdir,
		Flags:      store.MarshalFlags([]string{"--dangerously-skip-permissions"}),
	}))
	require.NoError(t, rig.ss.SetSessionSleeping(context.Background(), sid, true))

	ls, err := rig.ss.GetLiveSession(context.Background(), sid)
	require.NoError(t, err)
	require.NoError(t, rig.h.wakeExistingSession(context.Background(), ls))

	cmd := lastSentCommand(rig.term)
	assert.Contains(t, cmd, "--dangerously-skip-permissions", "woken agy must keep --dangerously-skip-permissions")

	// Now test Wake after a Restart:
	// Restart must preserve flags in live_sessions, so subsequent Wake preserves them too
	restartedName := naming.SessionName(at.Agy, sid)
	require.NoError(t, rig.term.CreateSession(context.Background(), restartedName, rig.workdir))
	restartBody, _ := json.Marshal(map[string]any{
		"session_id": sid,
		"agent_type": at.Agy,
	})
	restartReq := httptest.NewRequest(http.MethodPost, "/api/sessions/live/"+restartedName+"/restart", bytes.NewReader(restartBody))
	restartRec := httptest.NewRecorder()
	rig.router.ServeHTTP(restartRec, restartReq)
	require.Equal(t, http.StatusOK, restartRec.Code, restartRec.Body.String())

	var restartRes map[string]any
	require.NoError(t, json.Unmarshal(restartRec.Body.Bytes(), &restartRes))
	restartedSid := restartRes["session_id"].(string)

	require.NoError(t, rig.ss.SetSessionSleeping(context.Background(), restartedSid, true))
	lsRestarted, err := rig.ss.GetLiveSession(context.Background(), restartedSid)
	require.NoError(t, err)
	require.NoError(t, rig.h.wakeExistingSession(context.Background(), lsRestarted))

	cmdAfterRestartWake := lastSentCommand(rig.term)
	assert.Contains(t, cmdAfterRestartWake, "--dangerously-skip-permissions", "woken agy after restart must keep --dangerously-skip-permissions")
}

// 6a. Team launch
func TestAgyBypassPermissions_Path6a_TeamLaunch(t *testing.T) {
	rig := setupAgyTestRig(t)

	body, _ := json.Marshal(map[string]any{
		"agent_type":  at.Agy,
		"board_name":  "test-team",
		"working_dir": rig.workdir,
		"flags":       []string{"--dangerously-skip-permissions"},
		"agents": []map[string]any{
			{"name": "worker1", "prompt": "work"},
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/launch-team", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	rig.router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	cmd := lastSentCommand(rig.term)
	assert.Contains(t, cmd, rig.fakeAgy, "team member launch must run the fake agy stub, never host CLI")
	assert.Contains(t, cmd, "--dangerously-skip-permissions", "team member launch must inherit --dangerously-skip-permissions")
}

// 6b. Team reset
func TestAgyBypassPermissions_Path6b_TeamReset(t *testing.T) {
	rig := setupAgyTestRig(t)

	// First launch team member
	body, _ := json.Marshal(map[string]any{
		"agent_type":  at.Agy,
		"board_name":  "reset-team",
		"working_dir": rig.workdir,
		"flags":       []string{"--dangerously-skip-permissions"},
		"agents": []map[string]any{
			{"name": "worker1", "prompt": "work"},
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/launch-team", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	rig.router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// Now reset team
	rCtx := chi.NewRouteContext()
	rCtx.URLParams.Add("boardName", "reset-team")
	resetReq := httptest.NewRequest(http.MethodPost, "/api/sessions/live/team/reset-team/reset", nil)
	resetReq = resetReq.WithContext(context.WithValue(resetReq.Context(), chi.RouteCtxKey, rCtx))
	resetRec := httptest.NewRecorder()
	rig.h.ResetTeam(resetRec, resetReq)
	require.Equal(t, http.StatusOK, resetRec.Code, resetRec.Body.String())

	cmd := lastSentCommand(rig.term)
	assert.Contains(t, cmd, rig.fakeAgy, "reset team must run the fake agy stub, never host CLI")
	assert.Contains(t, cmd, "--dangerously-skip-permissions", "reset team must re-launch member with --dangerously-skip-permissions")
}

// 7. Model change restart
func TestAgyBypassPermissions_Path7_ModelChangeRestart(t *testing.T) {
	rig := setupAgyTestRig(t)
	sid := "44444444-5555-6666-7777-888888888888"
	name := naming.SessionName(at.Agy, sid)

	require.NoError(t, rig.term.CreateSession(context.Background(), name, rig.workdir))
	require.NoError(t, rig.ss.RegisterLiveSession(context.Background(), &store.LiveSession{
		SessionID:  sid,
		AgentType:  at.Agy,
		AgentName:  filepath.Base(rig.workdir),
		WorkingDir: rig.workdir,
		Flags:      store.MarshalFlags([]string{"--dangerously-skip-permissions"}),
	}))

	newModel := "gemini-2.5-flash"
	body, _ := json.Marshal(map[string]any{
		"session_id": sid,
		"agent_type": at.Agy,
		"model":      newModel,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/live/"+name+"/restart", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	rig.router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	cmd := lastSentCommand(rig.term)
	assert.Contains(t, cmd, "--dangerously-skip-permissions", "model change restart must keep --dangerously-skip-permissions")
	assert.Contains(t, cmd, "--model "+newModel, "model change restart must include --model")
}

// 8. Capabilities preset: read_only must stay plan mode (--mode plan), NEVER bypass permissions
func TestAgyBypassPermissions_Path8_ReadOnlyPreset(t *testing.T) {
	rig := setupAgyTestRig(t)
	readOnlyCaps := &agent.Capabilities{Allow: []string{agent.CapFileRead}}

	// 8a. Fresh launch with read_only caps
	body, _ := json.Marshal(map[string]any{
		"agent_type":   at.Agy,
		"working_dir":  rig.workdir,
		"capabilities": readOnlyCaps,
		"flags":        []string{"--dangerously-skip-permissions"}, // even if client sent bypass flag!
	})
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/launch", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	rig.router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	cmd := lastSentCommand(rig.term)
	assert.Contains(t, cmd, rig.fakeAgy, "read_only launch must run the fake agy stub, never host CLI")
	assert.Contains(t, cmd, "--mode plan", "read_only must start with --mode plan")
	assert.NotContains(t, cmd, "--dangerously-skip-permissions", "read_only must NEVER have --dangerously-skip-permissions")

	var launchRes map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &launchRes))
	sid := launchRes["session_id"].(string)
	name := launchRes["session_name"].(string)

	// 8b. Restart with read_only caps
	require.NoError(t, rig.term.CreateSession(context.Background(), name, rig.workdir))
	restartBody, _ := json.Marshal(map[string]any{
		"session_id": sid,
		"agent_type": at.Agy,
	})
	restartReq := httptest.NewRequest(http.MethodPost, "/api/sessions/live/"+name+"/restart", bytes.NewReader(restartBody))
	restartRec := httptest.NewRecorder()
	rig.router.ServeHTTP(restartRec, restartReq)
	require.Equal(t, http.StatusOK, restartRec.Code, restartRec.Body.String())

	cmd = lastSentCommand(rig.term)
	assert.Contains(t, cmd, "--mode plan", "restarted read_only must keep --mode plan")
	assert.NotContains(t, cmd, "--dangerously-skip-permissions", "restarted read_only must NEVER have --dangerously-skip-permissions")

	var restartRes map[string]any
	require.NoError(t, json.Unmarshal(restartRec.Body.Bytes(), &restartRes))
	newSid := restartRes["session_id"].(string)
	newName := restartRes["session_name"].(string)

	// 8c. Resume with read_only caps
	require.NoError(t, rig.term.CreateSession(context.Background(), newName, rig.workdir))
	resumeBody, _ := json.Marshal(map[string]any{
		"session_id":         newSid,
		"current_session_id": newSid,
		"agent_type":         at.Agy,
	})
	resumeReq := httptest.NewRequest(http.MethodPost, "/api/sessions/live/"+newName+"/resume", bytes.NewReader(resumeBody))
	resumeRec := httptest.NewRecorder()
	rig.router.ServeHTTP(resumeRec, resumeReq)
	require.Equal(t, http.StatusOK, resumeRec.Code, resumeRec.Body.String())

	cmd = lastSentCommand(rig.term)
	assert.Contains(t, cmd, "--mode plan", "resumed read_only must keep --mode plan")
	assert.NotContains(t, cmd, "--dangerously-skip-permissions", "resumed read_only must NEVER have --dangerously-skip-permissions")

	var resumeRes map[string]any
	require.NoError(t, json.Unmarshal(resumeRec.Body.Bytes(), &resumeRes))
	resumedSid := resumeRes["session_id"].(string)

	// 8d. Wake with read_only caps
	require.NoError(t, rig.ss.SetSessionSleeping(context.Background(), resumedSid, true))
	ls, err := rig.ss.GetLiveSession(context.Background(), resumedSid)
	require.NoError(t, err)
	require.NoError(t, rig.h.wakeExistingSession(context.Background(), ls))

	cmd = lastSentCommand(rig.term)
	assert.Contains(t, cmd, "--mode plan", "woken read_only must keep --mode plan")
	assert.NotContains(t, cmd, "--dangerously-skip-permissions", "woken read_only must NEVER have --dangerously-skip-permissions")
}

func TestPermissionModeFromFlags_TableTest_NeverWidens(t *testing.T) {
	tests := []struct {
		name         string
		flags        []string
		expectedMode string
		agyFlags     []string
		claudeFlags  []string
		codexFlags   []string
	}{
		{
			name:         "dangerously-skip-permissions",
			flags:        []string{"--dangerously-skip-permissions"},
			expectedMode: "bypassPermissions",
			agyFlags:     []string{"--dangerously-skip-permissions"},
			claudeFlags:  []string{"--permission-mode", "bypassPermissions"},
			codexFlags:   []string{"--dangerously-bypass-approvals-and-sandbox"},
		},
		{
			name:         "full-auto (Codex auto-accept preserves sandbox, agy gets nil)",
			flags:        []string{"--full-auto"},
			expectedMode: "auto",
			agyFlags:     nil,
			claudeFlags:  []string{"--permission-mode", "auto"},
			codexFlags:   []string{"--sandbox", "workspace-write", "-a", "never"},
		},
		{
			name:         "yolo alias",
			flags:        []string{"--yolo"},
			expectedMode: "bypassPermissions",
			agyFlags:     []string{"--dangerously-skip-permissions"},
			claudeFlags:  []string{"--permission-mode", "bypassPermissions"},
			codexFlags:   []string{"--dangerously-bypass-approvals-and-sandbox"},
		},
		{
			name:         "codex dangerous bypass",
			flags:        []string{"--dangerously-bypass-approvals-and-sandbox"},
			expectedMode: "bypassPermissions",
			agyFlags:     []string{"--dangerously-skip-permissions"},
			claudeFlags:  []string{"--permission-mode", "bypassPermissions"},
			codexFlags:   []string{"--dangerously-bypass-approvals-and-sandbox"},
		},
		{
			name:         "mode plan space",
			flags:        []string{"--mode", "plan"},
			expectedMode: "plan",
			agyFlags:     []string{"--mode", "plan"},
			claudeFlags:  []string{"--permission-mode", "plan"},
			codexFlags:   []string{"--sandbox", "read-only", "-a", "on-request"},
		},
		{
			name:         "mode plan equals",
			flags:        []string{"--mode=plan"},
			expectedMode: "plan",
			agyFlags:     []string{"--mode", "plan"},
			claudeFlags:  []string{"--permission-mode", "plan"},
			codexFlags:   []string{"--sandbox", "read-only", "-a", "on-request"},
		},
		{
			name:         "mode accept-edits",
			flags:        []string{"--mode", "accept-edits"},
			expectedMode: "acceptEdits",
			agyFlags:     []string{"--mode", "accept-edits"},
			claudeFlags:  []string{"--permission-mode", "acceptEdits"},
			codexFlags:   []string{"--sandbox", "workspace-write", "-a", "on-request"},
		},
		{
			name:         "mode accept-edits equals",
			flags:        []string{"--mode=accept-edits"},
			expectedMode: "acceptEdits",
			agyFlags:     []string{"--mode", "accept-edits"},
			claudeFlags:  []string{"--permission-mode", "acceptEdits"},
			codexFlags:   []string{"--sandbox", "workspace-write", "-a", "on-request"},
		},
		{
			name:         "permission-mode plan",
			flags:        []string{"--permission-mode", "plan"},
			expectedMode: "plan",
			agyFlags:     []string{"--mode", "plan"},
			claudeFlags:  []string{"--permission-mode", "plan"},
			codexFlags:   []string{"--sandbox", "read-only", "-a", "on-request"},
		},
		{
			name:         "permission-mode dontAsk",
			flags:        []string{"--permission-mode", "dontAsk"},
			expectedMode: "dontAsk",
			agyFlags:     nil,
			claudeFlags:  []string{"--permission-mode", "dontAsk"},
			codexFlags:   []string{"--sandbox", "workspace-write", "-a", "never"},
		},
		{
			name:         "permission-mode auto",
			flags:        []string{"--permission-mode", "auto"},
			expectedMode: "auto",
			agyFlags:     nil,
			claudeFlags:  []string{"--permission-mode", "auto"},
			codexFlags:   []string{"--sandbox", "workspace-write", "-a", "never"},
		},
		{
			name:         "permission-mode weird (unknown string)",
			flags:        []string{"--permission-mode", "weird"},
			expectedMode: "weird",
			agyFlags:     nil,
			claudeFlags:  []string{"--permission-mode", "weird"},
			codexFlags:   []string{"--sandbox", "workspace-write", "-a", "on-request"},
		},
		{
			name:         "permission-mode equals bypassPermissions",
			flags:        []string{"--permission-mode=bypassPermissions"},
			expectedMode: "bypassPermissions",
			agyFlags:     []string{"--dangerously-skip-permissions"},
			claudeFlags:  []string{"--permission-mode", "bypassPermissions"},
			codexFlags:   []string{"--dangerously-bypass-approvals-and-sandbox"},
		},
		{
			name:         "codex sandbox workspace-write -a never (carried as empty)",
			flags:        []string{"--sandbox", "workspace-write", "-a", "never"},
			expectedMode: "",
			agyFlags:     nil,
			claudeFlags:  nil,
			codexFlags:   nil,
		},
		{
			name:         "codex sandbox danger-full-access (carried as empty)",
			flags:        []string{"--sandbox", "danger-full-access"},
			expectedMode: "",
			agyFlags:     nil,
			claudeFlags:  nil,
			codexFlags:   nil,
		},
		{
			name:         "order: bypass first wins over plan",
			flags:        []string{"--dangerously-skip-permissions", "--mode", "plan"},
			expectedMode: "bypassPermissions",
			agyFlags:     []string{"--dangerously-skip-permissions"},
			claudeFlags:  []string{"--permission-mode", "bypassPermissions"},
			codexFlags:   []string{"--dangerously-bypass-approvals-and-sandbox"},
		},
		{
			name:         "order: plan first wins over bypass",
			flags:        []string{"--mode", "plan", "--dangerously-skip-permissions"},
			expectedMode: "plan",
			agyFlags:     []string{"--mode", "plan"},
			claudeFlags:  []string{"--permission-mode", "plan"},
			codexFlags:   []string{"--sandbox", "read-only", "-a", "on-request"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mode := agent.PermissionModeFromFlags(tc.flags)
			assert.Equal(t, tc.expectedMode, mode, "extracted permission mode")

			agy := agent.PermissionFlagsForAgent(at.Agy, mode)
			assert.Equal(t, tc.agyFlags, agy, "agy flags")

			gemini := agent.PermissionFlagsForAgent(at.Gemini, mode)
			assert.Equal(t, tc.agyFlags, gemini, "gemini flags match agy")

			claude := agent.PermissionFlagsForAgent(at.Claude, mode)
			assert.Equal(t, tc.claudeFlags, claude, "claude flags")

			codex := agent.PermissionFlagsForAgent(at.Codex, mode)
			assert.Equal(t, tc.codexFlags, codex, "codex flags")

			// Critical security assertion: for every mode string x agent type, the carried flags
			// are never the bypass flag unless the stored mode was bypassPermissions.
			for _, agType := range []string{at.Agy, at.Gemini, "antigravity", at.Codex, at.Claude} {
				flags := agent.PermissionFlagsForAgent(agType, mode)
				if tc.expectedMode != "bypassPermissions" {
					assert.NotContains(t, flags, "--dangerously-skip-permissions",
						"flags for %s under non-bypass mode %q must never bypass permissions", agType, mode)
					assert.NotContains(t, flags, "--dangerously-bypass-approvals-and-sandbox",
						"flags for %s under non-bypass mode %q must never bypass sandbox/approvals", agType, mode)
				}
			}
		})
	}
}

func TestAgyBypassPermissions_FailClosed_CorruptCapabilities(t *testing.T) {
	rig := setupAgyTestRig(t)
	sid := "a1111111-2222-3333-4444-555555555555"
	name := naming.SessionName(at.Agy, sid)
	corruptCaps := "{corrupt-invalid-json"

	require.NoError(t, rig.term.CreateSession(context.Background(), name, rig.workdir))
	require.NoError(t, rig.ss.RegisterLiveSession(context.Background(), &store.LiveSession{
		SessionID:    sid,
		AgentType:    at.Agy,
		AgentName:    filepath.Base(rig.workdir),
		WorkingDir:   rig.workdir,
		Capabilities: &corruptCaps,
		Flags:        store.MarshalFlags([]string{"--dangerously-skip-permissions"}),
	}))

	// 1. Restart must fail closed with error
	restartBody, _ := json.Marshal(map[string]any{
		"session_id": sid,
		"agent_type": at.Agy,
	})
	restartReq := httptest.NewRequest(http.MethodPost, "/api/sessions/live/"+name+"/restart", bytes.NewReader(restartBody))
	restartRec := httptest.NewRecorder()
	rig.router.ServeHTTP(restartRec, restartReq)
	assert.Equal(t, http.StatusInternalServerError, restartRec.Code, "restart must fail closed on corrupt capabilities")
	assert.Contains(t, restartRec.Body.String(), "corrupt stored capabilities")

	// 2. Resume must fail closed with error
	resumeBody, _ := json.Marshal(map[string]any{
		"session_id":         sid,
		"current_session_id": sid,
		"agent_type":         at.Agy,
	})
	resumeReq := httptest.NewRequest(http.MethodPost, "/api/sessions/live/"+name+"/resume", bytes.NewReader(resumeBody))
	resumeRec := httptest.NewRecorder()
	rig.router.ServeHTTP(resumeRec, resumeReq)
	assert.Equal(t, http.StatusInternalServerError, resumeRec.Code, "resume must fail closed on corrupt capabilities")
	assert.Contains(t, resumeRec.Body.String(), "corrupt stored capabilities")

	// 3. Wake must fail closed with error
	ls, err := rig.ss.GetLiveSession(context.Background(), sid)
	require.NoError(t, err)
	wakeErr := rig.h.wakeExistingSession(context.Background(), ls)
	assert.Error(t, wakeErr, "wake must fail closed on corrupt capabilities")
	assert.Contains(t, wakeErr.Error(), "corrupt stored capabilities")
}

// --------------------------------------------------------------------------
// Follow-up #5188: Pinned Launch Permission Mode tests
// --------------------------------------------------------------------------

// 1. Launch with default=bypass then set default=default:
// restart/resume/wake/team reset must all STAY bypass.
func TestPinnedLaunchPermissionMode_BypassToDefault(t *testing.T) {
	rig := setupAgyTestRig(t)
	ctx := context.Background()

	// Launch with global default_permission_mode = "bypassPermissions"
	require.NoError(t, rig.ss.SetSetting(ctx, "default_permission_mode", "bypassPermissions"))

	body, _ := json.Marshal(map[string]any{
		"agent_type":  at.Agy,
		"working_dir": rig.workdir,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/launch", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	rig.router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	cmd := lastSentCommand(rig.term)
	assert.Contains(t, cmd, "--dangerously-skip-permissions", "launch with default=bypass must include bypass flag")

	var launchRes map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &launchRes))
	sid := launchRes["session_id"].(string)
	name := launchRes["session_name"].(string)

	ls, err := rig.ss.GetLiveSession(ctx, sid)
	require.NoError(t, err)
	require.NotNil(t, ls)
	require.NotNil(t, ls.PermissionMode)
	assert.Equal(t, "bypassPermissions", *ls.PermissionMode, "stored permission_mode must be recorded as bypassPermissions")

	// Now flip global default setting to "default"
	require.NoError(t, rig.ss.SetSetting(ctx, "default_permission_mode", "default"))

	// Test A: Restart stays bypass
	require.NoError(t, rig.term.CreateSession(ctx, name, rig.workdir))
	restartBody, _ := json.Marshal(map[string]any{
		"session_id": sid,
		"agent_type": at.Agy,
	})
	restartReq := httptest.NewRequest(http.MethodPost, "/api/sessions/live/"+name+"/restart", bytes.NewReader(restartBody))
	restartRec := httptest.NewRecorder()
	rig.router.ServeHTTP(restartRec, restartReq)
	require.Equal(t, http.StatusOK, restartRec.Code, restartRec.Body.String())

	cmd = lastSentCommand(rig.term)
	assert.Contains(t, cmd, "--dangerously-skip-permissions", "restart must stay bypass after global default changed to default")

	var restartRes map[string]any
	require.NoError(t, json.Unmarshal(restartRec.Body.Bytes(), &restartRes))
	restartedSid := restartRes["session_id"].(string)
	restartedName := restartRes["session_name"].(string)

	lsRestarted, err := rig.ss.GetLiveSession(ctx, restartedSid)
	require.NoError(t, err)
	require.NotNil(t, lsRestarted.PermissionMode)
	assert.Equal(t, "bypassPermissions", *lsRestarted.PermissionMode, "replacement row must preserve stored bypassPermissions mode")

	// Test B: Resume stays bypass
	require.NoError(t, rig.term.CreateSession(ctx, restartedName, rig.workdir))
	resumeBody, _ := json.Marshal(map[string]any{
		"session_id":         restartedSid,
		"current_session_id": restartedSid,
		"agent_type":         at.Agy,
	})
	resumeReq := httptest.NewRequest(http.MethodPost, "/api/sessions/live/"+restartedName+"/resume", bytes.NewReader(resumeBody))
	resumeRec := httptest.NewRecorder()
	rig.router.ServeHTTP(resumeRec, resumeReq)
	require.Equal(t, http.StatusOK, resumeRec.Code, resumeRec.Body.String())

	cmd = lastSentCommand(rig.term)
	assert.Contains(t, cmd, "--dangerously-skip-permissions", "resume must stay bypass after global default changed to default")

	var resumeRes map[string]any
	require.NoError(t, json.Unmarshal(resumeRec.Body.Bytes(), &resumeRes))
	resumedSid := resumeRes["session_id"].(string)

	lsResumed, err := rig.ss.GetLiveSession(ctx, resumedSid)
	require.NoError(t, err)
	require.NotNil(t, lsResumed.PermissionMode)
	assert.Equal(t, "bypassPermissions", *lsResumed.PermissionMode, "resumed row must preserve stored bypassPermissions mode")

	// Test C: Wake stays bypass
	require.NoError(t, rig.ss.SetSessionSleeping(ctx, resumedSid, true))
	lsSleeping, err := rig.ss.GetLiveSession(ctx, resumedSid)
	require.NoError(t, err)
	require.NoError(t, rig.h.wakeExistingSession(ctx, lsSleeping))
	cmd = lastSentCommand(rig.term)
	assert.Contains(t, cmd, "--dangerously-skip-permissions", "wake must stay bypass after global default changed to default")

	// Test D: Team Reset stays bypass
	// Reset default to bypass, launch a team
	require.NoError(t, rig.ss.SetSetting(ctx, "default_permission_mode", "bypassPermissions"))
	boardName := "team-bypass-board"
	teamBody, _ := json.Marshal(map[string]any{
		"board_name":  boardName,
		"working_dir": rig.workdir,
		"agent_type":  at.Agy,
		"agents": []map[string]any{
			{"name": "lead", "agent_type": at.Agy},
		},
	})
	teamReq := httptest.NewRequest(http.MethodPost, "/api/sessions/launch-team", bytes.NewReader(teamBody))
	teamRec := httptest.NewRecorder()
	rig.router.ServeHTTP(teamRec, teamReq)
	require.Equal(t, http.StatusOK, teamRec.Code, teamRec.Body.String())

	teamSessions, err := rig.ss.GetBoardSessions(ctx, boardName)
	require.NoError(t, err)
	require.Len(t, teamSessions, 1)
	require.NotNil(t, teamSessions[0].PermissionMode)
	assert.Equal(t, "bypassPermissions", *teamSessions[0].PermissionMode)

	// Now flip global default to "default" and ResetTeam
	require.NoError(t, rig.ss.SetSetting(ctx, "default_permission_mode", "default"))
	resetReq := httptest.NewRequest(http.MethodPost, "/api/sessions/live/team/"+boardName+"/reset", nil)
	resetRec := httptest.NewRecorder()
	rig.router.ServeHTTP(resetRec, resetReq)
	require.Equal(t, http.StatusOK, resetRec.Code, resetRec.Body.String())

	cmd = lastSentCommand(rig.term)
	assert.Contains(t, cmd, "--dangerously-skip-permissions", "team reset must stay bypass after global default changed to default")

	resetSessions, err := rig.ss.GetBoardSessions(ctx, boardName)
	require.NoError(t, err)
	require.Len(t, resetSessions, 1)
	require.NotNil(t, resetSessions[0].PermissionMode)
	assert.Equal(t, "bypassPermissions", *resetSessions[0].PermissionMode, "team reset row must preserve stored bypassPermissions mode")
}

// 2. Launch with default=default then set default=bypass:
// restart/resume/wake/team reset must all STAY default (never widen to bypass!).
func TestPinnedLaunchPermissionMode_DefaultToBypass(t *testing.T) {
	rig := setupAgyTestRig(t)
	ctx := context.Background()

	// Launch with global default_permission_mode = "default"
	require.NoError(t, rig.ss.SetSetting(ctx, "default_permission_mode", "default"))

	body, _ := json.Marshal(map[string]any{
		"agent_type":  at.Agy,
		"working_dir": rig.workdir,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/launch", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	rig.router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	cmd := lastSentCommand(rig.term)
	assert.NotContains(t, cmd, "--dangerously-skip-permissions", "launch with default=default must not include bypass flag")

	var launchRes map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &launchRes))
	sid := launchRes["session_id"].(string)
	name := launchRes["session_name"].(string)

	ls, err := rig.ss.GetLiveSession(ctx, sid)
	require.NoError(t, err)
	require.NotNil(t, ls)
	require.NotNil(t, ls.PermissionMode)
	assert.Equal(t, "default", *ls.PermissionMode, "stored permission_mode must be recorded as default")

	// Now flip global default setting to "bypassPermissions"
	require.NoError(t, rig.ss.SetSetting(ctx, "default_permission_mode", "bypassPermissions"))

	// Test A: Restart stays default (never widens to bypass!)
	require.NoError(t, rig.term.CreateSession(ctx, name, rig.workdir))
	restartBody, _ := json.Marshal(map[string]any{
		"session_id": sid,
		"agent_type": at.Agy,
	})
	restartReq := httptest.NewRequest(http.MethodPost, "/api/sessions/live/"+name+"/restart", bytes.NewReader(restartBody))
	restartRec := httptest.NewRecorder()
	rig.router.ServeHTTP(restartRec, restartReq)
	require.Equal(t, http.StatusOK, restartRec.Code, restartRec.Body.String())

	cmd = lastSentCommand(rig.term)
	assert.NotContains(t, cmd, "--dangerously-skip-permissions", "restart must stay default (not widen) after global default changed to bypass")

	var restartRes map[string]any
	require.NoError(t, json.Unmarshal(restartRec.Body.Bytes(), &restartRes))
	restartedSid := restartRes["session_id"].(string)
	restartedName := restartRes["session_name"].(string)

	lsRestarted, err := rig.ss.GetLiveSession(ctx, restartedSid)
	require.NoError(t, err)
	require.NotNil(t, lsRestarted.PermissionMode)
	assert.Equal(t, "default", *lsRestarted.PermissionMode, "replacement row must preserve stored default mode")

	// Test B: Resume stays default (never widens to bypass!)
	require.NoError(t, rig.term.CreateSession(ctx, restartedName, rig.workdir))
	resumeBody, _ := json.Marshal(map[string]any{
		"session_id":         restartedSid,
		"current_session_id": restartedSid,
		"agent_type":         at.Agy,
	})
	resumeReq := httptest.NewRequest(http.MethodPost, "/api/sessions/live/"+restartedName+"/resume", bytes.NewReader(resumeBody))
	resumeRec := httptest.NewRecorder()
	rig.router.ServeHTTP(resumeRec, resumeReq)
	require.Equal(t, http.StatusOK, resumeRec.Code, resumeRec.Body.String())

	cmd = lastSentCommand(rig.term)
	assert.NotContains(t, cmd, "--dangerously-skip-permissions", "resume must stay default (not widen) after global default changed to bypass")

	var resumeRes map[string]any
	require.NoError(t, json.Unmarshal(resumeRec.Body.Bytes(), &resumeRes))
	resumedSid := resumeRes["session_id"].(string)

	lsResumed, err := rig.ss.GetLiveSession(ctx, resumedSid)
	require.NoError(t, err)
	require.NotNil(t, lsResumed.PermissionMode)
	assert.Equal(t, "default", *lsResumed.PermissionMode, "resumed row must preserve stored default mode")

	// Test C: Wake stays default (never widens to bypass!)
	require.NoError(t, rig.ss.SetSessionSleeping(ctx, resumedSid, true))
	lsSleeping, err := rig.ss.GetLiveSession(ctx, resumedSid)
	require.NoError(t, err)
	require.NoError(t, rig.h.wakeExistingSession(ctx, lsSleeping))
	cmd = lastSentCommand(rig.term)
	assert.NotContains(t, cmd, "--dangerously-skip-permissions", "wake must stay default (not widen) after global default changed to bypass")

	// Test D: Team Reset stays default (never widens to bypass!)
	// Reset default to default, launch a team
	require.NoError(t, rig.ss.SetSetting(ctx, "default_permission_mode", "default"))
	boardName := "team-default-board"
	teamBody, _ := json.Marshal(map[string]any{
		"board_name":  boardName,
		"working_dir": rig.workdir,
		"agent_type":  at.Agy,
		"agents": []map[string]any{
			{"name": "lead", "agent_type": at.Agy},
		},
	})
	teamReq := httptest.NewRequest(http.MethodPost, "/api/sessions/launch-team", bytes.NewReader(teamBody))
	teamRec := httptest.NewRecorder()
	rig.router.ServeHTTP(teamRec, teamReq)
	require.Equal(t, http.StatusOK, teamRec.Code, teamRec.Body.String())

	teamSessions, err := rig.ss.GetBoardSessions(ctx, boardName)
	require.NoError(t, err)
	require.Len(t, teamSessions, 1)
	require.NotNil(t, teamSessions[0].PermissionMode)
	assert.Equal(t, "default", *teamSessions[0].PermissionMode)

	// Now flip global default to "bypassPermissions" and ResetTeam
	require.NoError(t, rig.ss.SetSetting(ctx, "default_permission_mode", "bypassPermissions"))
	resetReq := httptest.NewRequest(http.MethodPost, "/api/sessions/live/team/"+boardName+"/reset", nil)
	resetRec := httptest.NewRecorder()
	rig.router.ServeHTTP(resetRec, resetReq)
	require.Equal(t, http.StatusOK, resetRec.Code, resetRec.Body.String())

	cmd = lastSentCommand(rig.term)
	assert.NotContains(t, cmd, "--dangerously-skip-permissions", "team reset must stay default after global default changed to bypass")

	resetSessions, err := rig.ss.GetBoardSessions(ctx, boardName)
	require.NoError(t, err)
	require.Len(t, resetSessions, 1)
	require.NotNil(t, resetSessions[0].PermissionMode)
	assert.Equal(t, "default", *resetSessions[0].PermissionMode, "team reset row must preserve stored default mode")
}

// 3. Capabilities presets keep driving the agent's permissions under every default.
func TestPinnedLaunchPermissionMode_PresetUnderEveryDefault(t *testing.T) {
	for _, defaultMode := range []string{"bypassPermissions", "default"} {
		t.Run("default_"+defaultMode, func(t *testing.T) {
			rig := setupAgyTestRig(t)
			ctx := context.Background()
			require.NoError(t, rig.ss.SetSetting(ctx, "default_permission_mode", defaultMode))

			readOnlyCaps := &agent.Capabilities{Allow: []string{agent.CapFileRead}}
			body, _ := json.Marshal(map[string]any{
				"agent_type":   at.Agy,
				"working_dir":  rig.workdir,
				"capabilities": readOnlyCaps,
			})
			req := httptest.NewRequest(http.MethodPost, "/api/sessions/launch", bytes.NewReader(body))
			rec := httptest.NewRecorder()
			rig.router.ServeHTTP(rec, req)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			cmd := lastSentCommand(rig.term)
			assert.Contains(t, cmd, "--mode plan", "preset must start with --mode plan")
			assert.NotContains(t, cmd, "--dangerously-skip-permissions", "preset must never bypass under default=%s", defaultMode)

			var launchRes map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &launchRes))
			sid := launchRes["session_id"].(string)
			name := launchRes["session_name"].(string)

			// Restart
			require.NoError(t, rig.term.CreateSession(ctx, name, rig.workdir))
			restartBody, _ := json.Marshal(map[string]any{
				"session_id": sid,
				"agent_type": at.Agy,
			})
			restartReq := httptest.NewRequest(http.MethodPost, "/api/sessions/live/"+name+"/restart", bytes.NewReader(restartBody))
			restartRec := httptest.NewRecorder()
			rig.router.ServeHTTP(restartRec, restartReq)
			require.Equal(t, http.StatusOK, restartRec.Code, restartRec.Body.String())

			cmd = lastSentCommand(rig.term)
			assert.Contains(t, cmd, "--mode plan", "restarted preset must keep --mode plan")
			assert.NotContains(t, cmd, "--dangerously-skip-permissions", "restarted preset must never bypass under default=%s", defaultMode)

			var restartRes map[string]any
			require.NoError(t, json.Unmarshal(restartRec.Body.Bytes(), &restartRes))
			newSid := restartRes["session_id"].(string)

			// Wake
			require.NoError(t, rig.ss.SetSessionSleeping(ctx, newSid, true))
			ls, err := rig.ss.GetLiveSession(ctx, newSid)
			require.NoError(t, err)
			require.NoError(t, rig.h.wakeExistingSession(ctx, ls))

			cmd = lastSentCommand(rig.term)
			assert.Contains(t, cmd, "--mode plan", "woken preset must keep --mode plan")
			assert.NotContains(t, cmd, "--dangerously-skip-permissions", "woken preset must never bypass under default=%s", defaultMode)
		})
	}
}

// 4. Stored mode written once at launch and not overwritten by restart's extra flags.
func TestPinnedLaunchPermissionMode_StoredModeNotOverwrittenByRestartExtraFlags(t *testing.T) {
	rig := setupAgyTestRig(t)
	ctx := context.Background()

	require.NoError(t, rig.ss.SetSetting(ctx, "default_permission_mode", "default"))

	body, _ := json.Marshal(map[string]any{
		"agent_type":  at.Agy,
		"working_dir": rig.workdir,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/launch", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	rig.router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var launchRes map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &launchRes))
	sid := launchRes["session_id"].(string)
	name := launchRes["session_name"].(string)

	ls, err := rig.ss.GetLiveSession(ctx, sid)
	require.NoError(t, err)
	require.NotNil(t, ls.PermissionMode)
	assert.Equal(t, "default", *ls.PermissionMode)

	// Restart with extra flags containing bypass
	require.NoError(t, rig.term.CreateSession(ctx, name, rig.workdir))
	restartBody, _ := json.Marshal(map[string]any{
		"session_id":  sid,
		"agent_type":  at.Agy,
		"extra_flags": "--dangerously-skip-permissions",
	})
	restartReq := httptest.NewRequest(http.MethodPost, "/api/sessions/live/"+name+"/restart", bytes.NewReader(restartBody))
	restartRec := httptest.NewRecorder()
	rig.router.ServeHTTP(restartRec, restartReq)
	require.Equal(t, http.StatusOK, restartRec.Code, restartRec.Body.String())

	var restartRes map[string]any
	require.NoError(t, json.Unmarshal(restartRec.Body.Bytes(), &restartRes))
	restartedSid := restartRes["session_id"].(string)
	restartedName := restartRes["session_name"].(string)

	// Check replacement row in DB: PermissionMode MUST STILL BE "default"!
	lsRestarted, err := rig.ss.GetLiveSession(ctx, restartedSid)
	require.NoError(t, err)
	require.NotNil(t, lsRestarted.PermissionMode)
	assert.Equal(t, "default", *lsRestarted.PermissionMode, "stored mode must NOT be overwritten by restart's extra flags")

	// Now restart again without extra flags: must stay default
	require.NoError(t, rig.term.CreateSession(ctx, restartedName, rig.workdir))
	restart2Body, _ := json.Marshal(map[string]any{
		"session_id": restartedSid,
		"agent_type": at.Agy,
	})
	restart2Req := httptest.NewRequest(http.MethodPost, "/api/sessions/live/"+restartedName+"/restart", bytes.NewReader(restart2Body))
	restart2Rec := httptest.NewRecorder()
	rig.router.ServeHTTP(restart2Rec, restart2Req)
	require.Equal(t, http.StatusOK, restart2Rec.Code, restart2Rec.Body.String())

	cmd := lastSentCommand(rig.term)
	assert.NotContains(t, cmd, "--dangerously-skip-permissions", "subsequent restart must stay default")
}

// 5. Old sessions migration: row with PermissionMode=nil takes current default setting.
func TestPinnedLaunchPermissionMode_OldSessionFallback(t *testing.T) {
	rig := setupAgyTestRig(t)
	ctx := context.Background()

	// Case A: when setting is bypass, old session takes current default (bypass)
	sid1 := "11111111-aaaa-bbbb-cccc-000000000001"
	name1 := naming.SessionName(at.Agy, sid1)
	require.NoError(t, rig.term.CreateSession(ctx, name1, rig.workdir))
	require.NoError(t, rig.ss.RegisterLiveSession(ctx, &store.LiveSession{
		SessionID:      sid1,
		AgentType:      at.Agy,
		AgentName:      filepath.Base(rig.workdir),
		WorkingDir:     rig.workdir,
		PermissionMode: nil, // old row from before migration
	}))
	require.NoError(t, rig.ss.SetSetting(ctx, "default_permission_mode", "bypassPermissions"))
	restartBody1, _ := json.Marshal(map[string]any{
		"session_id": sid1,
		"agent_type": at.Agy,
	})
	restartReq1 := httptest.NewRequest(http.MethodPost, "/api/sessions/live/"+name1+"/restart", bytes.NewReader(restartBody1))
	restartRec1 := httptest.NewRecorder()
	rig.router.ServeHTTP(restartRec1, restartReq1)
	require.Equal(t, http.StatusOK, restartRec1.Code, restartRec1.Body.String())
	assert.Contains(t, lastSentCommand(rig.term), "--dangerously-skip-permissions")

	// Case B: when setting is default, old session takes current default (default)
	sid2 := "11111111-aaaa-bbbb-cccc-000000000002"
	name2 := naming.SessionName(at.Agy, sid2)
	require.NoError(t, rig.term.CreateSession(ctx, name2, rig.workdir))
	require.NoError(t, rig.ss.RegisterLiveSession(ctx, &store.LiveSession{
		SessionID:      sid2,
		AgentType:      at.Agy,
		AgentName:      filepath.Base(rig.workdir),
		WorkingDir:     rig.workdir,
		PermissionMode: nil, // old row from before migration
	}))
	require.NoError(t, rig.ss.SetSetting(ctx, "default_permission_mode", "default"))
	// Forget case A's command: lastSentCommand reads a map of sessions in random order, so with both cases' commands in it the "last" one was
	// case A's about 1 run in 8 and this case failed.
	rig.term.mu.Lock()
	rig.term.sent = map[string][]string{}
	rig.term.mu.Unlock()
	restartBody2, _ := json.Marshal(map[string]any{
		"session_id": sid2,
		"agent_type": at.Agy,
	})
	restartReq2 := httptest.NewRequest(http.MethodPost, "/api/sessions/live/"+name2+"/restart", bytes.NewReader(restartBody2))
	restartRec2 := httptest.NewRecorder()
	rig.router.ServeHTTP(restartRec2, restartReq2)
	require.Equal(t, http.StatusOK, restartRec2.Code, restartRec2.Body.String())
	assert.NotContains(t, lastSentCommand(rig.term), "--dangerously-skip-permissions")
}

// 6. Corrupt stored values fail closed (both corrupt capabilities and corrupt permission mode).
func TestPinnedLaunchPermissionMode_FailClosed_CorruptValues(t *testing.T) {
	rig := setupAgyTestRig(t)
	ctx := context.Background()

	corruptMode := "corrupt_mode_value"
	sid := "c1111111-2222-3333-4444-555555555555"
	name := naming.SessionName(at.Agy, sid)

	require.NoError(t, rig.term.CreateSession(ctx, name, rig.workdir))
	require.NoError(t, rig.ss.RegisterLiveSession(ctx, &store.LiveSession{
		SessionID:      sid,
		AgentType:      at.Agy,
		AgentName:      filepath.Base(rig.workdir),
		WorkingDir:     rig.workdir,
		PermissionMode: &corruptMode,
	}))

	// 1. Restart fails closed with 500
	restartBody, _ := json.Marshal(map[string]any{
		"session_id": sid,
		"agent_type": at.Agy,
	})
	restartReq := httptest.NewRequest(http.MethodPost, "/api/sessions/live/"+name+"/restart", bytes.NewReader(restartBody))
	restartRec := httptest.NewRecorder()
	rig.router.ServeHTTP(restartRec, restartReq)
	assert.Equal(t, http.StatusInternalServerError, restartRec.Code, "restart must fail closed on corrupt permission mode")
	assert.Contains(t, restartRec.Body.String(), "corrupt stored permission mode")

	// 2. Resume fails closed with 500
	resumeBody, _ := json.Marshal(map[string]any{
		"session_id":         sid,
		"current_session_id": sid,
		"agent_type":         at.Agy,
	})
	resumeReq := httptest.NewRequest(http.MethodPost, "/api/sessions/live/"+name+"/resume", bytes.NewReader(resumeBody))
	resumeRec := httptest.NewRecorder()
	rig.router.ServeHTTP(resumeRec, resumeReq)
	assert.Equal(t, http.StatusInternalServerError, resumeRec.Code, "resume must fail closed on corrupt permission mode")
	assert.Contains(t, resumeRec.Body.String(), "corrupt stored permission mode")

	// 3. Wake fails closed with error
	ls, err := rig.ss.GetLiveSession(ctx, sid)
	require.NoError(t, err)
	wakeErr := rig.h.wakeExistingSession(ctx, ls)
	assert.Error(t, wakeErr, "wake must fail closed on corrupt permission mode")
	assert.Contains(t, wakeErr.Error(), "corrupt stored permission mode")

	// 4. ResetTeam skips agent with corrupt permission mode
	boardName := "corrupt-perm-board"
	corruptSid := "c2222222-2222-3333-4444-555555555555"
	require.NoError(t, rig.ss.RegisterLiveSession(ctx, &store.LiveSession{
		SessionID:      corruptSid,
		AgentType:      at.Agy,
		AgentName:      filepath.Base(rig.workdir),
		BoardName:      &boardName,
		WorkingDir:     rig.workdir,
		PermissionMode: &corruptMode,
	}))
	resetReq := httptest.NewRequest(http.MethodPost, "/api/sessions/live/team/"+boardName+"/reset", nil)
	resetRec := httptest.NewRecorder()
	rig.router.ServeHTTP(resetRec, resetReq)
	require.Equal(t, http.StatusOK, resetRec.Code, resetRec.Body.String())
	var resetRes struct {
		Ok     bool             `json:"ok"`
		Board  string           `json:"board"`
		Agents []map[string]any `json:"agents"`
	}
	require.NoError(t, json.Unmarshal(resetRec.Body.Bytes(), &resetRes))
	assert.Empty(t, resetRes.Agents, "agent with corrupt permission mode must be skipped during team reset")
}

func TestPinnedLaunchPermissionMode_UnknownModeDoesNotMakeAgentUnrestartable(t *testing.T) {
	rig := setupAgyTestRig(t)

	// Launch with an unknown permission mode string (--permission-mode=somethingnew)
	body, _ := json.Marshal(map[string]any{
		"agent_type":  at.Agy,
		"working_dir": rig.workdir,
		"flags":       []string{"--permission-mode=somethingnew"},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/launch", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	rig.router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var launchRes map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &launchRes))
	sid := launchRes["session_id"].(string)
	name := launchRes["session_name"].(string)

	// Check DB row: unknown mode was NOT stored (legacy fallback, nil)
	ls, err := rig.ss.GetLiveSession(context.Background(), sid)
	require.NoError(t, err)
	assert.Nil(t, ls.PermissionMode, "unknown permission mode must not be stored on live_sessions row")

	// Restart must succeed (fallback to legacy flags/default instead of failing closed)
	require.NoError(t, rig.term.CreateSession(context.Background(), name, rig.workdir))
	restartBody, _ := json.Marshal(map[string]any{
		"session_id": sid,
		"agent_type": at.Agy,
	})
	restartReq := httptest.NewRequest(http.MethodPost, "/api/sessions/live/"+name+"/restart", bytes.NewReader(restartBody))
	restartRec := httptest.NewRecorder()
	rig.router.ServeHTTP(restartRec, restartReq)
	require.Equal(t, http.StatusOK, restartRec.Code, restartRec.Body.String())
}
