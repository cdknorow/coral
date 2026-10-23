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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/agent"
	at "github.com/subgentic/coral/internal/agenttypes"
)

// A team launch whose member has no mode, no flags and no capabilities starts the CLI in its own
// confirm mode (#6415, the Mode "Default" the dialog shows): no auto flag for any CLI.
func TestATeamMemberWithNothingChosenSendsNoAutoFlags(t *testing.T) {
	for _, cli := range []string{at.Claude, at.Codex} {
		t.Run(cli, func(t *testing.T) {
			rig := setupAgyTestRig(t)
			fake := stubScript(t, filepath.Join(t.TempDir(), "mock-"+cli), "#!/bin/sh\nexit 0\n")
			require.NoError(t, rig.ss.SetSetting(context.Background(), agent.CLIPathSettingKey(cli), fake))

			body, _ := json.Marshal(map[string]any{"board_name": "empty" + cli, "new_board": true, "working_dir": rig.workdir, "agent_type": cli,
				"agents": []map[string]any{{"name": "A"}}})
			rec := httptest.NewRecorder()
			rig.router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/sessions/launch-team", bytes.NewReader(body)))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			cmd := lastSentCommand(rig.term)
			require.Contains(t, cmd, fake, "the member's CLI was typed")
			for _, bad := range []string{"--permission-mode auto", "-a never", "--full-auto", "--sandbox", "--dangerously-bypass-approvals-and-sandbox", "--dangerously-skip-permissions"} {
				assert.False(t, strings.Contains(cmd, bad), "%s: an empty member must not send %q: %s", cli, bad, cmd)
			}
		})
	}
}
