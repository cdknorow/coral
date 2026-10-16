package background

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3518: a team's knowledge folder is always <coral>/agent_knowledge/<one component>.
func TestKnowledgeTeamNamesStayInsideTheFolder(t *testing.T) {
	for _, ok := range []string{"coral-go-team", "team.v2", "a b", "Ünïcode"} {
		if !ValidKnowledgeTeam(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	dir := t.TempDir()
	for _, bad := range []string{"", ".", "..", "../x", "a/b", `a\b`, "a\x00b", "x:stream"} {
		if ValidKnowledgeTeam(bad) {
			t.Errorf("%q should be refused", bad)
		}
		if err := WriteKnowledgeFile(dir, bad, "agent", "x"); !errors.Is(err, ErrBadKnowledgeTeam) {
			t.Errorf("write %q: %v", bad, err)
		}
		if _, err := ReadKnowledgeBundle(dir, bad); !errors.Is(err, ErrBadKnowledgeTeam) {
			t.Errorf("read %q: %v", bad, err)
		}
		if LoadAgentKnowledge(dir, bad, "agent") != "" {
			t.Errorf("load %q", bad)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "x")); err == nil {
		t.Error("nothing is written for a refused name")
	}
}

// #3671 (#3669 finding 4): a team's knowledge folder can't be a Windows device name or end in a
// dot or a space; an agent named after a device ("Con", "NUL") gets a slug that isn't one, the
// same for every read and write.
func TestKnowledgeNamesAreNeverWindowsDevices(t *testing.T) {
	for _, bad := range []string{"CON", "nul", "Com1", "LPT1.team", "team.", "team "} {
		assert.False(t, ValidKnowledgeTeam(bad), bad)
	}
	assert.True(t, ValidKnowledgeTeam("console"))
	assert.Equal(t, "con_", Slugify("Con"))
	assert.Equal(t, "com1_", Slugify("COM1"))
	assert.Equal(t, "console", Slugify("Console"))
	assert.Equal(t, "backend-dev", Slugify("Backend Dev"))

	dir := t.TempDir()
	require.NoError(t, WriteKnowledgeFile(dir, "team", "Nul", "notes"))
	assert.Equal(t, "notes", LoadAgentKnowledge(dir, "team", "Nul"))
	_, err := os.Stat(filepath.Join(dir, "agent_knowledge", "team", "nul_.md"))
	assert.NoError(t, err)
}
