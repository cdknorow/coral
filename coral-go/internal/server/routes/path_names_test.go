package routes

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
)

// #5305: an id read from a file in the data folder, or a name from a request, is one folder name before it is joined onto a folder. "../../x" never leaves it.
const goodID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestAPendingRecordThatNamesAPathIsNotOne(t *testing.T) {
	dir := t.TempDir()
	write := func(id string) string {
		p := filepath.Join(dir, "p-"+strings.NewReplacer("/", "_", `\`, "_").Replace(id[:min(len(id), 6)])+".json")
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
	// A real repository with a commit, so a name that is let through would make a worktree outside the folder.
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "--allow-empty", "-m", "x"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	for _, name := range []string{"../escape", "a/b", "..", `..\x`, ""} {
		wd, path, failure := h.createTeamWorktree(t.Context(), launchTeamRequest{BoardName: name, WorkingDir: work, Worktree: true})
		assert.Empty(t, wd, "%q", name)
		assert.Empty(t, path, "%q", name)
		assert.NotEmpty(t, failure, "%q", name)
	}
	_, err := os.Stat(filepath.Join(h.cfg.CoralDir(), "escape"))
	assert.True(t, os.IsNotExist(err))
}

// One table over every id-checked site (#5305, QA's follow-up): each is called with an id that is a path, with a file outside the folder it would reach if the id were joined as
// it is. Each case fails by itself when its own check is deleted (mutated one by one), because each plants what that function would read, remove or report.
func TestEveryIdCheckedSiteRefusesAPathForAnId(t *testing.T) {
	_, h := setupBoardTestServer(t)
	coralDir := t.TempDir()
	h.SetTaskArtifactWriter(coralDir, nil)
	art := filepath.Join(coralDir, "artifacts")
	objects := filepath.Join(art, "objects")
	require.NoError(t, os.MkdirAll(objects, 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(art, "uploads", "s1"), 0o700))
	const sentinelBody = "sentinel bytes outside the objects folder"
	sentinel := filepath.Join(art, "sentinel") // objects/../sentinel
	require.NoError(t, os.WriteFile(sentinel, []byte(sentinelBody), 0o600))
	require.NoError(t, os.WriteFile(sentinel+".json", []byte(`{"name":"outside","media_type":"text/plain","size":41,"digest":"sha256:../sentinel"}`), 0o600))
	// uploads/s1/../x.json: an upload record outside any session folder.
	require.NoError(t, os.WriteFile(filepath.Join(art, "uploads", "x.json"), []byte(`{"name":"x","created_at":"2026-10-20T10:00:00Z"}`), 0o600))
	// A cross-team record outside the board's folder.
	recDir := h.boardRecordDir("received", "alpha")
	require.NoError(t, os.MkdirAll(recDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(recDir), "planted.json"), []byte(`{"id":"../planted","board":"alpha","name":"planted"}`), 0o600))

	// readAttachment and loadAttachment are guarded twice: by their own check and by objectSidecarDigest's, which they call before they read anything. Deleting either one alone is
	// not observable (the other still refuses); deleting both fails these cases (mutated), and objectSidecarDigest has its own.
	const asObject, asUpload, asRecord = "../sentinel", "../x", "../planted"
	cases := []struct {
		site string
		call func(t *testing.T)
	}{
		{"objectMeta", func(t *testing.T) { _, ok := h.objectMeta(asObject); assert.False(t, ok) }},
		{"objectIsStored", func(t *testing.T) { assert.False(t, h.objectIsStored(asObject)) }},
		{"objectSidecarDigest", func(t *testing.T) { assert.Empty(t, h.objectSidecarDigest(asObject)) }},
		{"crossTeamRecord", func(t *testing.T) { _, ok := h.crossTeamRecord("received", "alpha", asRecord); assert.False(t, ok) }},
		{"readAttachment", func(t *testing.T) {
			opened := ""
			old := attachBeforeHash
			attachBeforeHash = func(path string) { opened = path }
			defer func() { attachBeforeHash = old }()
			_, refusal := h.readAttachment(objects, asObject, "coral://artifacts/"+asObject, attachSender{person: true}, agentUploadRecord{})
			assert.NotNil(t, refusal)
			assert.Empty(t, opened, "the sentinel was not opened as an attachment")
		}},
		{"loadAttachment", func(t *testing.T) {
			opened := ""
			old := attachBeforeLoad
			attachBeforeLoad = func(path string) { opened = path }
			defer func() { attachBeforeLoad = old }()
			data, refusal := h.loadAttachment(attachedArtifact{ID: asObject, Size: int64(len(sentinelBody)), Name: "n", MediaType: "text/plain"})
			assert.NotNil(t, refusal)
			assert.Empty(t, data, "the sentinel's bytes were not read")
			assert.Empty(t, opened, "the sentinel was not opened for sending")
		}},
		{"writeCrossTeamRecord", func(t *testing.T) {
			assert.Error(t, h.writeCrossTeamRecord("received", crossTeamArtifactRecord{ID: "../escaped", Board: "alpha"}))
			_, err := os.Stat(filepath.Join(filepath.Dir(recDir), "escaped.json"))
			assert.True(t, os.IsNotExist(err), "nothing was written outside the board's folder")
		}},
		{"writePendingReceipt", func(t *testing.T) {
			err := h.writePendingReceipt("srv", "relay-1", crossTeamArtifactRecord{ID: "../escaped", Board: "alpha"})
			assert.EqualError(t, err, "not an artifact id")
		}},
		{"enrichTeamArtifact", func(t *testing.T) {
			it := &teamArtifact{managed: asObject}
			enrichTeamArtifact(it, objects)
			assert.False(t, it.Available, "the sentinel was not reported as a stored artifact")
		}},
		{"deleteArtifactFilesLocked", func(t *testing.T) {
			_, _, found, err := deleteArtifactFilesLocked(coralDir, asObject)
			assert.Error(t, err)
			assert.False(t, found)
			_, statErr := os.Stat(sentinel)
			assert.NoError(t, statErr, "the sentinel was not removed")
		}},
		{"newestRecordFor", func(t *testing.T) { assert.Empty(t, newestRecordFor(coralDir, asUpload)) }},
		{"objectNewestUse", func(t *testing.T) {
			assert.True(t, objectNewestUse(coralDir, asObject, nil).IsZero(), "the sentinel's mtime was not reported")
		}},
		{"hasUploadRecord", func(t *testing.T) { assert.False(t, hasUploadRecord(coralDir, asUpload)) }},
	}
	for _, c := range cases {
		t.Run(c.site, c.call)
	}
}
