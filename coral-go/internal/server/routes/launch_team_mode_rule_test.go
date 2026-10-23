package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/agent"
	at "github.com/subgentic/coral/internal/agenttypes"
)

// #6425 (QA's argv cases): the team's permission mode is every member's unless it names one, whether or not the member has capabilities. Capabilities never replace it; for
// Codex and Antigravity they only narrow it, and no sandbox comes only from the Bypass Permissions mode, never from chips.
func TestTheTeamModeReachesEveryMemberAndCapabilitiesOnlyNarrowIt(t *testing.T) {
	fourChips := []string{agent.CapShell, agent.CapFileRead, agent.CapFileWrite, agent.CapGitWrite}
	members := map[string][]string{"none": nil, "four chips": fourChips, "read-only": {agent.CapFileRead}}
	type want struct{ has, not []string }
	cases := map[string]map[string]map[string]want{ // cli -> team flags -> member -> argv
		at.Claude: {
			"plan":   {"none": {[]string{"--permission-mode plan"}, nil}, "four chips": {[]string{"--permission-mode plan"}, nil}, "read-only": {[]string{"--permission-mode plan"}, nil}},
			"bypass": {"none": {[]string{"--permission-mode bypassPermissions"}, nil}, "four chips": {[]string{"--permission-mode bypassPermissions"}, nil}, "read-only": {[]string{"--permission-mode bypassPermissions"}, nil}},
			"":       {"none": {nil, []string{"--permission-mode auto"}}, "four chips": {nil, []string{"--permission-mode auto"}}, "read-only": {nil, []string{"--permission-mode auto"}}},
		},
		at.Codex: {
			"plan":   {"none": {[]string{"--sandbox read-only -a on-request"}, []string{"-a never", "bypass"}}, "four chips": {[]string{"--sandbox read-only -a on-request"}, []string{"-a never", "bypass"}}, "read-only": {[]string{"--sandbox read-only -a on-request"}, []string{"-a never", "bypass"}}},
			"bypass": {"none": {[]string{"--dangerously-bypass-approvals-and-sandbox"}, nil}, "four chips": {[]string{"--dangerously-bypass-approvals-and-sandbox"}, nil}, "read-only": {[]string{"--sandbox read-only -a on-request"}, []string{"bypass"}}},
			"":       {"none": {nil, []string{"--sandbox", "bypass", "-a never"}}, "four chips": {[]string{"--sandbox workspace-write -a never"}, []string{"bypass"}}, "read-only": {[]string{"--sandbox read-only -a on-request"}, nil}},
		},
		at.Agy: {
			"plan":   {"none": {[]string{"--mode plan"}, []string{"skip-permissions"}}, "four chips": {[]string{"--mode plan"}, []string{"skip-permissions"}}, "read-only": {[]string{"--mode plan"}, nil}},
			"bypass": {"none": {[]string{"--dangerously-skip-permissions"}, nil}, "four chips": {[]string{"--dangerously-skip-permissions"}, nil}, "read-only": {[]string{"--mode plan"}, []string{"skip-permissions"}}},
			"":       {"none": {nil, []string{"skip-permissions", "--mode"}}, "four chips": {[]string{"--dangerously-skip-permissions"}, nil}, "read-only": {[]string{"--mode plan"}, nil}},
		},
	}
	n := 0
	for cli, byFlags := range cases {
		for team, byMember := range byFlags {
			for member, w := range byMember {
				n++
				t.Run(fmt.Sprintf("%s team %q member %s", cli, team, member), func(t *testing.T) {
					rig := setupAgyTestRig(t)
					fake := stubScript(t, filepath.Join(t.TempDir(), "mock-"+cli), "#!/bin/sh\nexit 0\n")
					require.NoError(t, rig.ss.SetSetting(context.Background(), agent.CLIPathSettingKey(cli), fake))
					var flags []string
					switch team {
					case "plan":
						flags = []string{"--permission-mode", "plan"}
					case "bypass":
						flags = []string{"--permission-mode", "bypassPermissions"}
					}
					m := map[string]any{"name": "A"}
					if caps := members[member]; caps != nil {
						m["capabilities"] = map[string]any{"allow": caps}
					}
					body, _ := json.Marshal(map[string]any{"board_name": fmt.Sprintf("rule%d", n), "new_board": true, "working_dir": rig.workdir, "agent_type": cli, "flags": flags, "agents": []any{m}})
					rec := httptest.NewRecorder()
					rig.router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/sessions/launch-team", bytes.NewReader(body)))
					require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
					cmd := lastSentCommand(rig.term)
					require.Contains(t, cmd, fake)
					for _, h := range w.has {
						assert.Contains(t, cmd, h, flagsOnly(cmd))
					}
					for _, bad := range w.not {
						assert.False(t, strings.Contains(cmd, bad), "must not send %q: %s", bad, flagsOnly(cmd))
					}
				})
			}
		}
	}
}
