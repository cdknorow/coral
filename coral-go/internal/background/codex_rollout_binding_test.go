package background

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/transcriptlink"
)

// writeRollout writes a Codex rollout for thread in the same folder (cwd) as every other one;
// marker is the Coral session id in its developer message ("" for none).
func writeRollout(t *testing.T, codexHome, thread, cwd, marker string) string {
	t.Helper()
	dir := filepath.Join(codexHome, "sessions", "2026", "10", "18")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	path := filepath.Join(dir, "rollout-2026-10-18T09-00-00-"+thread+".jsonl")
	body := `{"type":"session_meta","payload":{"id":"` + thread + `","cwd":"` + cwd + `"}}` + "\n" +
		`{"type":"turn_context","payload":{"cwd":"` + cwd + `","model":"gpt-5-codex"}}` + "\n"
	if marker != "" {
		body += `{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"CORAL_SESSION_ID: ` + marker + `"}]}}` + "\n"
	}
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

const (
	parentCoral = "11111111-1111-4111-8111-111111111111"
	childCoral  = "22222222-2222-4222-8222-222222222222"
	parentTh    = "0aaaaaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa"
	childTh     = "0bbbbbbb-bbbb-7bbb-8bbb-bbbbbbbbbbbb"
)

// #4590: two Codex sessions run in one folder and one CODEX_HOME. Neither the folder nor the
// time picks a rollout for a session; its link and its marker do, so each gets its own.
func TestFindCodexRollout_TwoSessionsInOneFolderEachGetTheirOwn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	parent := writeRollout(t, home, parentTh, "/repo", parentCoral)
	child := writeRollout(t, home, childTh, "/repo", childCoral)

	ls := func(id string) *store.LiveSession {
		return &store.LiveSession{SessionID: id, AgentType: "codex", WorkingDir: "/repo", CreatedAt: "2026-10-18T09:00:01Z"}
	}
	assert.Equal(t, parent, findCodexRollout(ls(parentCoral)), "found by its marker")
	assert.Equal(t, child, findCodexRollout(ls(childCoral)))

	require.NoError(t, transcriptlink.BindCodex(childCoral, childTh))
	assert.Equal(t, evalLink(t, child), evalLink(t, findCodexRollout(ls(childCoral))), "the link names the child's own rollout")
	assert.Equal(t, parent, findCodexRollout(ls(parentCoral)), "the parent's is unaffected")
}

func evalLink(t *testing.T, p string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(p)
	require.NoError(t, err)
	return real
}

// A session with no link, no id in a file name and no marker gets no rollout: the one in its
// folder that is newest, or started at about its time, is somebody else's.
func TestFindCodexRollout_NoLinkNoMarkerFindsNothingEvenInTheSameFolderAndTime(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	writeRollout(t, home, parentTh, "/repo", parentCoral)
	writeRollout(t, home, childTh, "/repo", "") // no marker

	got := findCodexRollout(&store.LiveSession{SessionID: "33333333-3333-4333-8333-333333333333", AgentType: "codex",
		WorkingDir: "/repo", CreatedAt: "2026-10-18T09:00:00Z"})
	assert.Empty(t, got)
}

// #4590: an empty or wildcard id would match any Pi session file ("" is contained in every name).
func TestResolvePiSessionPath_RefusesAnIdThatIsNotAUUID(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dir)
	t.Setenv("CORAL_DATA_DIR", filepath.Join(dir, "coral"))
	real := "1f2e3d4c-5b6a-4978-8a9b-0c1d2e3f4a5b"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "2026-10-18_"+real+".jsonl"), []byte("{}\n"), 0o600))
	require.NotEmpty(t, resolvePiSessionPath(real), "control")
	for _, bad := range []string{"", "*", "?", "[a-z]", real[:12]} {
		assert.Empty(t, resolvePiSessionPath(bad), "%q", bad)
	}
}
