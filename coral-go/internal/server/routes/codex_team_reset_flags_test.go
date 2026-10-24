package routes

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #5251: a team of Codex agents launched with a raw --full-auto (or the bypass flag) keeps it through a team reset, as a menu restart does. Found with real Codex: the reset
// relaunched the members with no permission flags at all.
func TestTeamResetKeepsAMembersRawCodexPermissionFlag(t *testing.T) {
	for i, tc := range []struct {
		name  string
		flags []string
		want  string
	}{
		{"--full-auto", []string{"--full-auto"}, "--sandbox workspace-write -a never"},
		{"the bypass flag", []string{"--dangerously-bypass-approvals-and-sandbox"}, "--dangerously-bypass-approvals-and-sandbox"},
		{"--permission-mode acceptEdits", []string{"--permission-mode", "acceptEdits"}, "--sandbox workspace-write -a on-request"},
		{"--permission-mode=plan", []string{"--permission-mode=plan"}, "--sandbox read-only -a on-request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, h, term, _ := setupSessionsTestServer(t)
			board := "reset-flags-" + string(rune('a'+i))
			body, _ := json.Marshal(map[string]any{"board_name": board, "working_dir": t.TempDir(), "agent_type": "codex", "backend": "tmux", "flags": tc.flags,
				"agents": []map[string]any{{"name": "TA", "prompt": ""}, {"name": "TB", "prompt": ""}}})
			rec := httptest.NewRecorder()
			h.LaunchTeam(rec, httptest.NewRequest(http.MethodPost, "/api/sessions/launch-team", bytes.NewReader(body)))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			var launched struct {
				Board string `json:"board"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &launched))
			require.NotEmpty(t, launched.Board, rec.Body.String())
			launchedCmds := len(term.sentCommands())
			require.Equal(t, 2, launchedCmds)
			for _, c := range term.sentCommands() {
				require.Contains(t, c, tc.want, "the members launch with it")
			}

			rr := httptest.NewRecorder()
			h.ResetTeam(rr, dropAt2(httptest.NewRequest(http.MethodPost, "/reset", nil), map[string]string{"boardName": launched.Board}))
			require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
			cmds := term.sentCommands()
			require.Len(t, cmds, 4, "two more starts after the reset")
			for _, c := range cmds[2:] {
				assert.Equal(t, 1, strings.Count(c, " --sandbox ")+strings.Count(c, "--dangerously-bypass-approvals-and-sandbox"), "one permission setting: %s", flagsOnly(c))
				assert.Contains(t, c, tc.want, "after the reset: %s", flagsOnly(c))
			}
		})
	}
}
