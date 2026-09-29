package board

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWorkflowPresetPersistenceAndSnapshots(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "board.db")
	s, err := NewStore(path)
	require.NoError(t, err)
	defer func() { s.Close() }()
	_, err = s.SaveWorkflowPreset(ctx, "team", "review", "Review", "Review changes carefully.", true)
	require.NoError(t, err)
	_, err = s.SetWorkingMode(ctx, "team", WorkingMode{Mode: "review", DependencyGuidance: true, CustomInstructions: "Run checks.", Instructions: "forged"})
	require.NoError(t, err)
	task, err := s.CreateTask(ctx, "team", "First", "", "medium", "lead")
	require.NoError(t, err)
	task, err = s.ClaimTask(ctx, "team", "dev", task.ID)
	require.NoError(t, err)
	original := task.Workflow.Instructions
	require.Contains(t, original, "Review changes carefully.")
	require.Contains(t, original, "separate dependent tasks")
	require.Contains(t, original, "Run checks.")
	require.NotContains(t, original, "forged")
	_, err = s.SaveWorkflowPreset(ctx, "team", "review", "Renamed", "Updated review.", false)
	require.NoError(t, err)
	_, err = s.ReassignTask(ctx, "team", task.ID, "other")
	require.NoError(t, err)
	task, err = s.ClaimTask(ctx, "team", "other", task.ID)
	require.NoError(t, err)
	require.Equal(t, original, task.Workflow.Instructions)
	require.NoError(t, s.Close())
	s, err = NewStore(path)
	require.NoError(t, err)
	presets, err := s.ListWorkflowPresets(ctx, "team")
	require.NoError(t, err)
	require.Len(t, presets, 4)
	require.Equal(t, "Renamed", presets[3].Name)
	next, err := s.CreateTask(ctx, "team", "Next", "", "medium", "lead")
	require.NoError(t, err)
	next, err = s.ClaimTask(ctx, "team", "newdev", next.ID)
	require.NoError(t, err)
	require.Contains(t, next.Workflow.Instructions, "Updated review.")
	require.NotContains(t, next.Workflow.Instructions, "Review changes carefully.")
	other, err := s.ListWorkflowPresets(ctx, "other-team")
	require.NoError(t, err)
	require.Len(t, other, 3)
	_, err = s.SetWorkingMode(ctx, "other-team", WorkingMode{Mode: "review"})
	require.Error(t, err)
}

func TestWorkflowPresetResetAndCompatibility(t *testing.T) {
	s, err := NewStore(filepath.Join(t.TempDir(), "board.db"))
	require.NoError(t, err)
	defer s.Close()
	ctx := context.Background()
	// Pre-preset settings must resolve shipped instructions, not cached generated text.
	_, err = s.db.ExecContext(ctx, `INSERT INTO board_working_modes(board_id,data) VALUES(?,?)`, "team", `{"mode":"shared_checkout","custom_instructions":"Extra","instructions":"stale"}`)
	require.NoError(t, err)
	mode, err := s.GetWorkingMode(ctx, "team")
	require.NoError(t, err)
	require.Contains(t, mode.Instructions, "shared checkout")
	require.NotContains(t, mode.Instructions, "stale")
	_, err = s.SaveWorkflowPreset(ctx, "team", "custom", "Custom", "Keep me", true)
	require.NoError(t, err)
	p, err := s.SaveWorkflowPreset(ctx, "team", "shared_checkout", "Ignored", "", false)
	require.NoError(t, err)
	require.True(t, p.Overridden)
	require.NotEmpty(t, p.DefaultInstructions)
	require.Empty(t, p.Instructions)
	mode, err = s.GetWorkingMode(ctx, "team")
	require.NoError(t, err)
	require.Equal(t, "Extra", mode.Instructions)
	task, err := s.CreateTask(ctx, "team", "Before reset", "", "medium", "lead")
	require.NoError(t, err)
	task, err = s.ClaimTask(ctx, "team", "dev", task.ID)
	require.NoError(t, err)
	original := task.Workflow.Instructions
	p, err = s.ResetWorkflowPreset(ctx, "team", "shared_checkout")
	require.NoError(t, err)
	require.False(t, p.Overridden)
	require.Equal(t, p.DefaultInstructions, p.Instructions)
	mode, err = s.GetWorkingMode(ctx, "team")
	require.NoError(t, err)
	require.Equal(t, "shared_checkout", mode.Mode)
	require.Contains(t, mode.Instructions, "shared checkout")
	require.Equal(t, "Extra", mode.CustomInstructions)
	task, err = s.getTaskByID(ctx, "team", task.ID)
	require.NoError(t, err)
	require.Equal(t, original, task.Workflow.Instructions)
	_, err = s.ResetWorkflowPreset(ctx, "team", "custom")
	require.Error(t, err)
	presets, err := s.ListWorkflowPresets(ctx, "team")
	require.NoError(t, err)
	require.Len(t, presets, 4)
	require.Equal(t, "Keep me", presets[3].Instructions)
	for _, id := range []string{"none", "shared_checkout", "worktrees"} {
		_, err = s.SetWorkingMode(ctx, "team", WorkingMode{Mode: id})
		require.NoError(t, err)
	}
}

func TestWorkflowPresetValidationAndConcurrentCreation(t *testing.T) {
	s, err := NewStore(filepath.Join(t.TempDir(), "board.db"))
	require.NoError(t, err)
	defer s.Close()
	ctx := context.Background()
	for _, tc := range []struct{ id, name, instructions string }{
		{"../bad", "Bad", ""}, {"Bad", "Bad", ""}, {"good", "", ""}, {"good", strings.Repeat("a", 81), ""}, {"good", "Good", strings.Repeat("é", 2049)},
	} {
		_, err = s.SaveWorkflowPreset(ctx, "team", tc.id, tc.name, tc.instructions, true)
		require.Error(t, err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.SaveWorkflowPreset(ctx, "team", "race", "Race", "One winner", true)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		} else {
			require.ErrorIs(t, err, ErrPresetExists)
		}
	}
	require.Equal(t, 1, wins)
	_, err = s.SaveWorkflowPreset(ctx, "team", "race", "Changed", strings.Repeat("x", 4097), false)
	require.Error(t, err)
	p, err := loadWorkflowPreset(ctx, s.db, "team", "race")
	require.NoError(t, err)
	require.Equal(t, "Race", p.Name)
	_, err = s.SaveWorkflowPreset(ctx, "team", "none", "None", "", true)
	require.ErrorIs(t, err, ErrPresetExists)
	_, err = s.SaveWorkflowPreset(ctx, "team", "missing", "Missing", "", false)
	require.Error(t, err)
}

func TestWorkflowPresetBoardDeletion(t *testing.T) {
	s, err := NewStore(filepath.Join(t.TempDir(), "board.db"))
	require.NoError(t, err)
	defer s.Close()
	ctx := context.Background()
	for _, team := range []string{"remove", "keep"} {
		_, err = s.SaveWorkflowPreset(ctx, team, "custom", "Custom", "Team instructions", true)
		require.NoError(t, err)
		_, err = s.SetWorkingMode(ctx, team, WorkingMode{Mode: "custom"})
		require.NoError(t, err)
	}
	require.NoError(t, s.DeleteProject(ctx, "remove"))
	presets, err := s.ListWorkflowPresets(ctx, "remove")
	require.NoError(t, err)
	require.Len(t, presets, 3)
	mode, err := s.GetWorkingMode(ctx, "remove")
	require.NoError(t, err)
	require.Equal(t, "none", mode.Mode)
	mode, err = s.GetWorkingMode(ctx, "keep")
	require.NoError(t, err)
	require.Equal(t, "custom", mode.Mode)
}
