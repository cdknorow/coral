package routes

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/config"
)

// #5305: an id read from a file in the data folder, or a name from a request, is one folder name before it is joined onto a folder. "../../x" never leaves it.
const goodID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestAPendingRecordThatNamesAPathIsNotOne(t *testing.T) {
	dir := t.TempDir()
	write := func(id string) string {
		p := filepath.Join(dir, "p-"+strings.ReplaceAll(id[:min(len(id), 6)], "/", "_")+".json")
		b, _ := json.Marshal(pendingReceipt{Rec: crossTeamArtifactRecord{ID: id, Size: 1}, Source: "srv", RelayID: "r1"})
		require.NoError(t, os.WriteFile(p, b, 0o600))
		return p
	}
	_, ok := readPending(write(goodID))
	assert.True(t, ok, "a 64-digit hex id is a pending record")
	for _, id := range []string{"../../x", "..", "a/b", `..\x`, strings.ToUpper(goodID), goodID[:63], goodID + "0", "x"} {
		_, ok := readPending(write(id))
		assert.False(t, ok, "%q", id)
	}
}

func TestCrossTeamRecordHelpersRefuseAnIdThatIsAPath(t *testing.T) {
	_, h := setupBoardTestServer(t)
	coralDir := t.TempDir()
	h.SetTaskArtifactWriter(coralDir, nil)
	dir := h.boardRecordDir("received", "alpha")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	// A file outside the board's folder that a traversing id would reach.
	planted := filepath.Join(filepath.Dir(dir), "planted.json")
	require.NoError(t, os.WriteFile(planted, []byte(`{"id":"../planted","board":"alpha","name":"x"}`), 0o600))
	_, ok := h.crossTeamRecord("received", "alpha", "../planted")
	assert.False(t, ok, "an id with a separator reads nothing")
	// A record whose id is a path is not written, and nothing appears outside the folder.
	err := h.writeCrossTeamRecord("received", crossTeamArtifactRecord{ID: "../escaped", Board: "alpha"})
	assert.Error(t, err)
	_, statErr := os.Stat(filepath.Join(filepath.Dir(dir), "escaped.json"))
	assert.True(t, os.IsNotExist(statErr))
	// The object and its sidecar are read by a hex id only.
	_, ok = h.objectMeta("../planted")
	assert.False(t, ok)
	assert.False(t, h.objectIsStored("../../x"))
	assert.Empty(t, h.objectSidecarDigest("../../x"))
	// And the same helpers work for a real id.
	require.NoError(t, h.writeCrossTeamRecord("received", crossTeamArtifactRecord{ID: goodID, Board: "alpha", Name: "n"}))
	rec, ok := h.crossTeamRecord("received", "alpha", goodID)
	assert.True(t, ok)
	assert.Equal(t, "n", rec.Name)
}

func TestATeamWorktreeFolderIsOneName(t *testing.T) {
	_, h, _, _ := setupSessionsTestServerWithConfig(t, config.Load(t.TempDir()))
	work := t.TempDir()
	for _, name := range []string{"../escape", "a/b", "..", `..\x`, ""} {
		wd, path, failure := h.createTeamWorktree(t.Context(), launchTeamRequest{BoardName: name, WorkingDir: work, Worktree: true})
		assert.Empty(t, wd, "%q", name)
		assert.Empty(t, path, "%q", name)
		assert.NotEmpty(t, failure, "%q", name)
	}
	_, err := os.Stat(filepath.Join(h.cfg.CoralDir(), "escape"))
	assert.True(t, os.IsNotExist(err))
}
