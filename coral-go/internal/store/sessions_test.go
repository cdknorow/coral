package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUserSettings(t *testing.T) {
	db := openTestDB(t)
	s := NewSessionStore(db)
	ctx := context.Background()

	// Empty initially
	settings, err := s.GetSettings(ctx)
	require.NoError(t, err)
	assert.Empty(t, settings)

	// Set a value
	err = s.SetSetting(ctx, "theme", "dark")
	require.NoError(t, err)

	settings, err = s.GetSettings(ctx)
	require.NoError(t, err)
	assert.Equal(t, "dark", settings["theme"])

	// Upsert
	err = s.SetSetting(ctx, "theme", "light")
	require.NoError(t, err)

	settings, err = s.GetSettings(ctx)
	require.NoError(t, err)
	assert.Equal(t, "light", settings["theme"])
}

func TestSessionNotes(t *testing.T) {
	db := openTestDB(t)
	s := NewSessionStore(db)
	ctx := context.Background()

	// Missing session returns empty
	meta, err := s.GetSessionNotes(ctx, "sess-1")
	require.NoError(t, err)
	assert.Equal(t, "", meta.NotesMD)
	assert.False(t, meta.IsUserEdited)

	// Save user notes
	err = s.SaveSessionNotes(ctx, "sess-1", "# My Notes\nSome content")
	require.NoError(t, err)

	meta, err = s.GetSessionNotes(ctx, "sess-1")
	require.NoError(t, err)
	assert.Equal(t, "# My Notes\nSome content", meta.NotesMD)
	assert.True(t, meta.IsUserEdited)

	// Auto-summary should NOT overwrite user edits
	err = s.SaveAutoSummary(ctx, "sess-1", "AI generated summary")
	require.NoError(t, err)

	meta, err = s.GetSessionNotes(ctx, "sess-1")
	require.NoError(t, err)
	assert.Equal(t, "# My Notes\nSome content", meta.NotesMD) // unchanged
}

func TestAutoSummary(t *testing.T) {
	db := openTestDB(t)
	s := NewSessionStore(db)
	ctx := context.Background()

	// Auto-summary works when no user edits
	err := s.SaveAutoSummary(ctx, "sess-2", "First summary")
	require.NoError(t, err)

	meta, err := s.GetSessionNotes(ctx, "sess-2")
	require.NoError(t, err)
	assert.Equal(t, "First summary", meta.AutoSummary)
	assert.False(t, meta.IsUserEdited)
}

func TestDisplayNames(t *testing.T) {
	db := openTestDB(t)
	s := NewSessionStore(db)
	ctx := context.Background()

	// Missing returns nil
	name, err := s.GetDisplayName(ctx, "sess-1")
	require.NoError(t, err)
	assert.Nil(t, name)

	// Set display name
	err = s.SetDisplayName(ctx, "sess-1", "My Agent")
	require.NoError(t, err)

	name, err = s.GetDisplayName(ctx, "sess-1")
	require.NoError(t, err)
	require.NotNil(t, name)
	assert.Equal(t, "My Agent", *name)

	// Bulk get
	err = s.SetDisplayName(ctx, "sess-2", "Other Agent")
	require.NoError(t, err)

	names, err := s.GetDisplayNames(ctx, []string{"sess-1", "sess-2", "sess-3"})
	require.NoError(t, err)
	assert.Equal(t, "My Agent", names["sess-1"])
	assert.Equal(t, "Other Agent", names["sess-2"])
	_, ok := names["sess-3"]
	assert.False(t, ok)

	// Migrate
	err = s.MigrateDisplayName(ctx, "sess-1", "sess-new")
	require.NoError(t, err)

	name, err = s.GetDisplayName(ctx, "sess-new")
	require.NoError(t, err)
	require.NotNil(t, name)
	assert.Equal(t, "My Agent", *name)
}

func TestTags(t *testing.T) {
	db := openTestDB(t)
	s := NewSessionStore(db)
	ctx := context.Background()

	// Create tags
	tag1, err := s.CreateTag(ctx, "bugfix", "#ff0000")
	require.NoError(t, err)
	assert.Equal(t, "bugfix", tag1.Name)

	tag2, err := s.CreateTag(ctx, "feature", "")
	require.NoError(t, err)
	assert.Equal(t, "#58a6ff", tag2.Color) // default color

	// List
	tags, err := s.ListTags(ctx)
	require.NoError(t, err)
	assert.Len(t, tags, 2)

	// Add to session
	err = s.AddSessionTag(ctx, "sess-1", tag1.ID)
	require.NoError(t, err)

	sessionTags, err := s.GetSessionTags(ctx, "sess-1")
	require.NoError(t, err)
	assert.Len(t, sessionTags, 1)
	assert.Equal(t, "bugfix", sessionTags[0].Name)

	// Remove from session
	err = s.RemoveSessionTag(ctx, "sess-1", tag1.ID)
	require.NoError(t, err)

	sessionTags, err = s.GetSessionTags(ctx, "sess-1")
	require.NoError(t, err)
	assert.Empty(t, sessionTags)

	// Delete tag
	err = s.DeleteTag(ctx, tag1.ID)
	require.NoError(t, err)

	tags, err = s.ListTags(ctx)
	require.NoError(t, err)
	assert.Len(t, tags, 1)
}

func TestFolderTags(t *testing.T) {
	db := openTestDB(t)
	s := NewSessionStore(db)
	ctx := context.Background()

	tag, err := s.CreateTag(ctx, "project-a", "#00ff00")
	require.NoError(t, err)

	err = s.AddFolderTag(ctx, "my-repo", tag.ID)
	require.NoError(t, err)

	folderTags, err := s.GetFolderTags(ctx, "my-repo")
	require.NoError(t, err)
	assert.Len(t, folderTags, 1)

	all, err := s.GetAllFolderTags(ctx)
	require.NoError(t, err)
	assert.Len(t, all["my-repo"], 1)

	err = s.RemoveFolderTag(ctx, "my-repo", tag.ID)
	require.NoError(t, err)

	folderTags, err = s.GetFolderTags(ctx, "my-repo")
	require.NoError(t, err)
	assert.Empty(t, folderTags)
}

func TestSessionIndex(t *testing.T) {
	db := openTestDB(t)
	s := NewSessionStore(db)
	ctx := context.Background()

	first := "2024-01-01T10:00:00"
	last := "2024-01-01T11:30:00"

	err := s.UpsertSessionIndex(ctx, &SessionIndex{
		SessionID:      "sess-1",
		SourceType:     "claude",
		SourceFile:     "/path/to/file.jsonl",
		FirstTimestamp: &first,
		LastTimestamp:   &last,
		MessageCount:   42,
		DisplaySummary: "Test session",
		FileMtime:      1234567890.0,
	})
	require.NoError(t, err)

	// Check indexed mtimes
	mtimes, err := s.GetIndexedMtimes(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1234567890.0, mtimes["/path/to/file.jsonl"])
}

func TestFTSSearch(t *testing.T) {
	db := openTestDB(t)
	s := NewSessionStore(db)
	ctx := context.Background()

	err := s.UpsertFTS(ctx, "sess-1", "implementing a new feature for the dashboard")
	require.NoError(t, err)

	err = s.UpsertFTS(ctx, "sess-2", "fixing a critical bug in the authentication system")
	require.NoError(t, err)
}

func TestSummarizerQueue(t *testing.T) {
	db := openTestDB(t)
	s := NewSessionStore(db)
	ctx := context.Background()

	err := s.EnqueueForSummarization(ctx, "sess-1")
	require.NoError(t, err)

	pending, err := s.GetPendingSummaries(ctx, 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"sess-1"}, pending)

	err = s.MarkSummarized(ctx, "sess-1", "done", nil)
	require.NoError(t, err)

	pending, err = s.GetPendingSummaries(ctx, 10)
	require.NoError(t, err)
	assert.Empty(t, pending)
}

func TestAgentLiveState(t *testing.T) {
	db := openTestDB(t)
	s := NewSessionStore(db)
	ctx := context.Background()

	// Initially nil
	id, err := s.GetAgentSessionID(ctx, "agent-1")
	require.NoError(t, err)
	assert.Nil(t, id)

	// Set
	err = s.SetAgentSessionID(ctx, "agent-1", "sess-abc")
	require.NoError(t, err)

	id, err = s.GetAgentSessionID(ctx, "agent-1")
	require.NoError(t, err)
	require.NotNil(t, id)
	assert.Equal(t, "sess-abc", *id)

	// Clear
	err = s.ClearAgentSessionID(ctx, "agent-1")
	require.NoError(t, err)

	id, err = s.GetAgentSessionID(ctx, "agent-1")
	require.NoError(t, err)
	assert.Nil(t, id)
}

func TestLiveSessions(t *testing.T) {
	db := openTestDB(t)
	s := NewSessionStore(db)
	ctx := context.Background()

	prompt := "Build a feature"
	board := "my-board"

	err := s.RegisterLiveSession(ctx, &LiveSession{
		SessionID:  "sess-1",
		AgentType:  "claude",
		AgentName:  "my-repo",
		WorkingDir: "/tmp/repo",
		Prompt:     &prompt,
		BoardName:  &board,
	})
	require.NoError(t, err)

	sessions, err := s.GetAllLiveSessions(ctx)
	require.NoError(t, err)
	assert.Len(t, sessions, 1)
	assert.Equal(t, "sess-1", sessions[0].SessionID)

	// Get prompt info
	info, err := s.GetLiveSessionPromptInfo(ctx, "sess-1")
	require.NoError(t, err)
	require.NotNil(t, info)
	assert.Equal(t, &prompt, info.Prompt)
	assert.Equal(t, &board, info.BoardName)

	// Agent type lookup
	agentType := s.GetAgentTypeForSession(ctx, "sess-1")
	assert.Equal(t, "claude", agentType)

	// Missing session defaults to claude
	agentType = s.GetAgentTypeForSession(ctx, "nonexistent")
	assert.Equal(t, "claude", agentType)

	// Replace
	newPrompt := "Updated prompt"
	err = s.ReplaceLiveSession(ctx, "sess-1", &LiveSession{
		SessionID:  "sess-2",
		AgentType:  "claude",
		AgentName:  "my-repo",
		WorkingDir: "/tmp/repo",
		Prompt:     &newPrompt,
	})
	require.NoError(t, err)

	sessions, err = s.GetAllLiveSessions(ctx)
	require.NoError(t, err)
	assert.Len(t, sessions, 1)
	assert.Equal(t, "sess-2", sessions[0].SessionID)
	// Board should be carried forward
	assert.Equal(t, &board, sessions[0].BoardName)

	// Unregister
	err = s.UnregisterLiveSession(ctx, "sess-2")
	require.NoError(t, err)

	sessions, err = s.GetAllLiveSessions(ctx)
	require.NoError(t, err)
	assert.Empty(t, sessions)
}

func TestSanitizeFTSQuery(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		mode     string
		expected string
	}{
		{"empty", "", "phrase", ""},
		{"phrase mode", "hello world", "phrase", `"hello world"`},
		{"and mode", "hello world", "and", "hello AND world"},
		{"or mode", "hello world", "or", "hello OR world"},
		{"strips operators", "hello AND world OR NOT foo", "and", "hello AND world AND foo"},
		{"quoted phrases", `"exact phrase" other`, "and", `"exact phrase" AND other`},
		{"invalid mode defaults", "hello", "invalid", `"hello"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizeFTSQuery(tt.raw, tt.mode)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestComputeDuration(t *testing.T) {
	tests := []struct {
		name     string
		first    *string
		last     *string
		expected *int
	}{
		{"nil timestamps", nil, nil, nil},
		{"empty strings", strPtr(""), strPtr(""), nil},
		{"valid timestamps", strPtr("2024-01-01T10:00:00"), strPtr("2024-01-01T11:30:00"), intPtr(5400)},
		{"with timezone", strPtr("2024-01-01T10:00:00+00:00"), strPtr("2024-01-01T10:05:00+00:00"), intPtr(300)},
		{"with fractional", strPtr("2024-01-01T10:00:00.123"), strPtr("2024-01-01T10:01:00.456"), intPtr(60)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := computeDuration(tt.first, tt.last)
			if tt.expected == nil {
				assert.Nil(t, result)
			} else {
				require.NotNil(t, result)
				assert.Equal(t, *tt.expected, *result)
			}
		})
	}
}

func TestExtractFirstHeader(t *testing.T) {
	assert.Equal(t, "My Header", extractFirstHeader("# My Header\nSome content"))
	assert.Equal(t, "Sub Header", extractFirstHeader("## Sub Header"))
	assert.Equal(t, "", extractFirstHeader("No header here"))
	assert.Equal(t, "", extractFirstHeader(""))
}

func TestParseFlags(t *testing.T) {
	assert.Nil(t, ParseFlags(nil))
	empty := ""
	assert.Nil(t, ParseFlags(&empty))
	flags := `["--verbose","--model","opus"]`
	result := ParseFlags(&flags)
	assert.Equal(t, []string{"--verbose", "--model", "opus"}, result)
}

func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }
