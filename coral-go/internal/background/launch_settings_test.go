package background

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	at "github.com/cdknorow/coral/internal/agenttypes"
	"github.com/cdknorow/coral/internal/naming"
	"github.com/cdknorow/coral/internal/store"
)

// #4350 (Security's INFO 3): with Coral's settings unreadable (the table gone: no other read here
// touches it), each place that runs an agent CLI returns an error or stops BEFORE running a command,
// where it used to run the CLI found on PATH. A stub claude first on PATH records any run.
func TestCLIRunnersDoNotRunACommandWhenSettingsCannotBeRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stub binary")
	}
	bin := t.TempDir()
	ran := filepath.Join(t.TempDir(), "ran")
	require.NoError(t, os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\necho \"$@\" >> "+ran+"\necho stub\n"), 0o755))
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	naming.SetDefaultLogDir(t.TempDir())
	t.Cleanup(func() { naming.SetDefaultLogDir("") })

	db := setupTestDB(t)
	ss := store.NewSessionStore(db)
	ctx := context.Background()

	// A transcript for the summarizer to read, so it reaches the settings.
	transcript := filepath.Join(t.TempDir(), "t.jsonl")
	require.NoError(t, os.WriteFile(transcript, []byte(`{"type":"user","message":{"content":"fix the bug"}}`+"\n"+`{"type":"assistant","message":{"content":"done"}}`+"\n"), 0o644))
	_, err := db.ExecContext(ctx, `INSERT INTO session_index (session_id, source_type, source_file, indexed_at, file_mtime) VALUES ('sum-1', 'claude', ?, '2026-01-01T00:00:00Z', 0)`, transcript)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `DROP TABLE user_settings`)
	require.NoError(t, err)

	type result struct {
		name string
		err  error // nil: the case stopped quietly (the goal poll has no error to return)
		note string
	}
	var results []result

	rec := &spawnRecorder{}
	_, err = NewAgentLauncher(rec, ss, 8450).LaunchAgent(ctx, t.TempDir(), at.Claude, "job", "", nil, true, "p", "", "")
	require.Error(t, err, "the background launcher fails")
	assert.Contains(t, err.Error(), "settings")
	rec.mu.Lock()
	assert.Empty(t, rec.commands, "the background launcher spawned nothing")
	rec.mu.Unlock()
	results = append(results, result{name: "background launcher", err: err})

	runner := NewWorkflowRunner(store.NewWorkflowStore(db), nil, nil, nil, nil, t.TempDir(), "localhost", 8420)
	err = runner.executeAgentPrint(ctx, 1, StepDef{Name: "agent-step"}, testStepFiles(t), t.TempDir(), os.Environ(), &AgentStepConfig{AgentType: at.Claude}, "hi", nil, &StepResult{})
	require.Error(t, err, "the workflow print step fails")
	assert.Contains(t, err.Error(), "settings")
	results = append(results, result{name: "workflow print step", err: err})

	_, err = DistillTeamKnowledge(ctx, ss, store.NewTeamStore(db), "team", t.TempDir(), nil)
	require.Error(t, err, "the knowledge builder fails")
	assert.Contains(t, err.Error(), "settings")
	results = append(results, result{name: "knowledge builder", err: err})

	err = BuildSummarizeFn(ss)(ctx, "sum-1") // saves a failure note, runs nothing
	assert.NoError(t, err)
	notes, nerr := ss.GetSessionNotes(ctx, "sum-1")
	require.NoError(t, nerr)
	require.NotNil(t, notes, "the summarizer reached the settings and recorded why it did nothing")
	assert.Contains(t, notes.AutoSummary, "settings")
	results = append(results, result{name: "summarizer", note: notes.AutoSummary})

	var logs bytes.Buffer
	var mu sync.Mutex
	gen := NewGoalGenerator(ss, nil, time.Hour)
	gen.logger = slog.New(slog.NewTextHandler(syncBuf{&logs, &mu}, nil))
	gen.pollOnce(ctx)
	mu.Lock()
	assert.Contains(t, logs.String(), "failed to read settings for the goal poll", "the goal poll stopped on the unreadable settings")
	mu.Unlock()
	results = append(results, result{name: "goal poll"})

	// The CLI version recorder asks the CLI for --version, so it too stays away from PATH (INFO 2).
	recordCLIVersion(ss, "sum-1", at.Claude)

	assert.Len(t, results, 5)
	_, statErr := os.Stat(ran)
	assert.True(t, os.IsNotExist(statErr), "no command ran: the stub claude on PATH was never started (%v)", strings.TrimSpace(readIf(ran)))
}

func readIf(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

type syncBuf struct {
	b  *bytes.Buffer
	mu *sync.Mutex
}

func (s syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
