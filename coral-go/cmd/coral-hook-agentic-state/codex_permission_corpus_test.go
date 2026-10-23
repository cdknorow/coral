package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Codex's PermissionRequest hook input as a real Codex (0.163.0, -a on-request) sent it at its command-approval dialog, captured by Codex Test (#6621, ids and paths replaced by placeholders, the
// token a fake one): the approval of a command, and one that was then cancelled. The tool is Bash; its input has a command and a description.
func codexPermissionCorpus(t *testing.T, name string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "codex-0.163.0", name))
	require.NoError(t, err)
	var d map[string]any
	require.NoError(t, json.Unmarshal(raw, &d))
	return d
}

func TestCodexPermissionRequestCorpus(t *testing.T) {
	t.Setenv("CORAL_SESSION_NAME", "codex-11111111-2222-3333-4444-555555555555")
	for name, marker := range map[string]string{"PermissionRequest-approval.json": "APPROVE6621", "PermissionRequest-cancel.json": "DENY6621"} {
		e := parseAgenticEvent(codexPermissionCorpus(t, name), "PermissionRequest", "sid")
		require.NotNil(t, e, name)
		assert.Equal(t, "permission_request", e["event_type"], name)
		assert.Equal(t, "Waiting for the user: permission (Bash)", e["summary"], name+": the summary names the tool and nothing of the command")
		assert.Equal(t, "Bash", e["tool_name"], name)
		d, _ := e["detail_json"].(map[string]any)
		assert.Equal(t, "permission", d["kind"], name)
		wants, _ := d["wants"].(string)
		assert.True(t, strings.HasPrefix(wants, "Wants to run: python3 -c "), name+": "+wants)
		assert.Contains(t, wants, marker, name+": the command's own words")
		assert.NotContains(t, e["summary"], marker, name)
	}
}
