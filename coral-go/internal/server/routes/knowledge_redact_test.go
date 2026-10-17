package routes

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/pkg/redact"
)

// #4414: a knowledge file written before knowledge was redacted reaches a launch prompt redacted.
func TestInjectedKnowledgeIsRedacted(t *testing.T) {
	iso := t.TempDir()
	for _, k := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(k, iso)
	}
	t.Setenv("HOME", filepath.Join(iso, "home"))
	t.Setenv("USERPROFILE", filepath.Join(iso, "home"))
	t.Setenv("CORAL_DATA_DIR", filepath.Join(iso, "data"))
	dir := t.TempDir()
	const token = "ghp_A1b2C3d4E5A1b2C3d4E5A1b2C3d4E5F6g7H8"
	kd := filepath.Join(dir, "agent_knowledge", "my-team")
	require.NoError(t, os.MkdirAll(kd, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(kd, "devops.md"), []byte("deploy with "+token), 0o600))

	got := InjectKnowledgeIntoPrompt(dir, "my-team", "DevOps", "You are DevOps.")
	assert.NotContains(t, got, token)
	assert.Contains(t, got, "deploy with "+redact.Marker)
}
