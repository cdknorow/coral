//go:build !windows

package claudeartifact

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The transcript is opened as a regular file and never through a link as its last name: a link to another file is not followed.
func TestLastAssistantIDsDoesNotFollowALink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.jsonl")
	require.NoError(t, os.WriteFile(real, []byte(`{"type":"assistant","message":{"role":"assistant","content":"https://claude.ai/artifact/linklink12"}}`+"\n"), 0o600))
	link := filepath.Join(dir, "link.jsonl")
	require.NoError(t, os.Symlink(real, link))
	assert.Equal(t, []string{"linklink12"}, LastAssistantIDs(real))
	assert.Nil(t, LastAssistantIDs(link), "a symlink is refused")
	assert.Nil(t, LastAssistantIDs(dir), "a folder is not a transcript")
	assert.Nil(t, LastAssistantIDs(""), "no path")
}
