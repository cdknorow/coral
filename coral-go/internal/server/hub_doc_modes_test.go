package server

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// agent_docs/hub.md says which Coral Hub access mode each action needs (#4506: it said stopping one agent stayed at Interact long after #4274 made it
// Launch). The route table (testdata/cloud_routes.golden, what the connector enforces) is the truth; this pins the rows of the doc that name modes to it.
func TestHubDocModesMatchTheRouteTable(t *testing.T) {
	g, err := os.ReadFile(cloudGolden)
	require.NoError(t, err)
	mode := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(string(g)), "\n") {
		i := strings.LastIndex(l, " ")
		mode[l[:i]] = l[i+1:]
	}
	d, err := os.ReadFile("../../agent_docs/hub.md")
	require.NoError(t, err)
	doc := string(d)
	row := func(sub string) string {
		for _, l := range strings.Split(doc, "\n") {
			if strings.Contains(l, sub) {
				return l
			}
		}
		t.Fatalf("hub.md has no row naming %q", sub)
		return ""
	}
	routes := func(want string, rs ...string) {
		t.Helper()
		for _, r := range rs {
			require.Equal(t, want, mode[r], "the route table's mode for %s", r)
		}
	}

	// Stopping, sleeping and waking agents: Launch, one agent or a team; archiving stays at Interact.
	routes("launch", "POST /api/sessions/live/team/{boardName}/kill", "POST /api/sessions/live/{name}/kill", "POST /api/sessions/live/{sessionID}/sleep",
		"POST /api/sessions/live/{sessionID}/wake", "POST /api/sessions/live/{name}/restart", "POST /api/sessions/live/{name}/resume",
		"POST /api/sessions/live/team/{boardName}/sleep", "POST /api/sessions/live/team/{boardName}/wake", "POST /api/sessions/live/team/{boardName}/reset")
	routes("interact", "POST /api/sessions/live/{sessionID}/archive", "POST /api/sessions/live/{sessionID}/unarchive")
	kill := row("stop every agent of a team at once")
	require.Contains(t, kill, "needs **Launch**")
	require.Contains(t, kill, "stopping one agent")
	require.NotContains(t, kill, "stays at Interact", "single-agent kill needs Launch since #4274")
	require.Contains(t, kill, "Archive and unarchive")
	require.Contains(t, kill, "stay at Interact")

	// The team's working mode: Interact; a preset's edit and reset are not reachable through the tunnel.
	routes("interact", "PUT /api/board/{project}/working-mode", "POST /api/board/{project}/working-mode/presets")
	routes("never", "PUT /api/board/{project}/working-mode/presets/{presetID}", "POST /api/board/{project}/working-mode/presets/{presetID}/reset")
	require.Contains(t, row("`PUT /api/board/{p}/working-mode`"), "never reachable through the tunnel")

	// Pause and resume need Launch, a reminder Interact; reassign is never reachable.
	routes("launch", "POST /api/board/{project}/pause", "POST /api/board/{project}/resume", "DELETE /api/board/{project}", "POST /api/tasks/run")
	routes("interact", "POST /api/board/{project}/reminder", "PATCH /api/board/{project}/tasks/{taskID}/amend", "POST /api/board/{project}/tasks/{taskID}/complete")
	routes("never", "POST /api/board/{project}/tasks/{taskID}/reassign")
	// The board settings: the owner's alone at Launch since #5307 (the machine refuses anyone else), the prompt inspection likewise.
	routes("launch", "PUT /api/board/{project}/settings", "GET /api/settings/prompt-inspection")
	require.Contains(t, row("A board write that names no actor"), "pause and resume, which need **Launch**")
	require.Contains(t, row("in any identity field of a board write"), "reassign is never reachable through the tunnel")
	require.Contains(t, row("`PUT /api/board/{p}/settings`"), "needs **Launch** and is this computer's owner's alone")
	require.Contains(t, row("`GET /api/settings/prompt-inspection`"), "needs **Launch** and is the owner's read alone")
}
