package jsonl

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodexGuardianIsHiddenFromEveryChatReadButNotTheRawFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	dir := filepath.Join(home, "sessions", "2026", "10", "22")
	require.NoError(t, os.MkdirAll(dir, 0700))
	for _, kind := range []string{"guardian", "parent"} {
		t.Run(kind, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "agent", "testdata", "codex-0163-"+kind+".jsonl"))
			require.NoError(t, err)
			sid := "11111111-1111-4111-8111-111111116081"
			if kind == "guardian" {
				sid = "11111111-1111-4111-8111-111111116082"
			}
			path := filepath.Join(dir, "rollout-2026-10-22T00-00-00-"+sid+".jsonl")
			require.NoError(t, os.WriteFile(path, data, 0600))
			rd := NewSessionReader()
			msgs, total := rd.ReadAllMessagesContext(context.Background(), sid, "", "codex")
			if kind == "guardian" {
				assert.Empty(t, msgs)
				assert.Zero(t, total)
			} else {
				assert.NotEmpty(t, msgs)
				assert.Greater(t, total, 1)
			}
			live, n := NewSessionReader().ReadAllMessagesForLiveContext(context.Background(), sid, "", "codex")
			assert.Equal(t, total, n)
			assert.Equal(t, len(msgs), len(live))
			from, end, err := ReadFromContext(context.Background(), path, "codex", 0)
			require.NoError(t, err)
			assert.Greater(t, end, int64(0))
			restore := SetTailChunkForTest(64)
			defer restore()
			tail, _, err := ReadTail(context.Background(), path, "codex", func(m []map[string]any) bool { return len(m) > 0 })
			require.NoError(t, err)
			if kind == "guardian" {
				assert.Empty(t, from)
				assert.Empty(t, tail)
				from, _, err = ReadFromContext(context.Background(), path, "codex", int64(len(data)/2))
				require.NoError(t, err)
				assert.Empty(t, from)
			} else {
				assert.NotEmpty(t, from)
				assert.NotEmpty(t, tail)
			}
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, data, after)
		})
	}
}
