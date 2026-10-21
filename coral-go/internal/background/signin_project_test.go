package background

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/agentsignin"
	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/claudestub"
	"github.com/subgentic/coral/internal/store"
)

// #5476: a workflow whose repository holds the credential (a project apiKeyHelper) is not refused: the run's check is asked from the repository, as its steps run there.
func TestWorkflowPreflightAsksFromTheRepositoryTheStepsRunIn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binary")
	}
	claudestub.Isolate(t)
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude-fake")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = auth ] && [ \"$2\" = status ]; then\n" +
		"  if [ -f .claude/settings.json ]; then echo '{\"loggedIn\":true}'; exit 0; fi\n" +
		"  echo '{\"loggedIn\":false}'; exit 1\n" +
		"fi\nexit 0\n"
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))
	repo := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".claude"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".claude", "settings.json"), []byte(`{"apiKeyHelper":"/x/get-key"}`), 0o600))
	agentsignin.Forget(at.Claude, bin)
	t.Cleanup(func() { agentsignin.Forget(at.Claude, bin) })

	db := setupTestDB(t)
	require.NoError(t, store.NewSessionStore(db).SetSetting(context.Background(), agent.CLIPathSettingKey(at.Claude), bin))
	runner := NewWorkflowRunner(store.NewWorkflowStore(db), nil, nil, nil, nil, t.TempDir(), "localhost", 8420)
	steps := []StepDef{{Name: "a", Type: "agent"}}
	def := &AgentStepConfig{AgentType: at.Claude}

	assert.Nil(t, runner.signInPreflightIn(context.Background(), steps, def, repo), "the repository holds the credential")
	assert.NotNil(t, runner.signInPreflightIn(context.Background(), steps, def, t.TempDir()), "another folder does not")
}
