package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodexGuardianMetadataUsesOnlyTheThreadMarker(t *testing.T) {
	for _, tc := range []struct {
		payload string
		hidden  bool
	}{
		{`{"thread_source":"guardian_review"}`, true},
		{`{"source":{"subagent":{"other":"guardian"}}}`, true},
		{`{"thread_source":"user","source":{"subagent":{"other":"guardian"}}}`, false},
		{`{"thread_source":"subagent","source":{"subagent":{"other":"worker"}}}`, false},
		{`{"source":{"subagent":{"other":"worker"}}}`, false},
		{`{"message":"guardian_review","role":"assistant"}`, false},
	} {
		var entry map[string]any
		require.NoError(t, json.Unmarshal([]byte(`{"type":"session_meta","payload":`+tc.payload+`}`), &entry))
		assert.Equal(t, tc.hidden, IsCodexGuardianMetadata(entry), tc.payload)
	}
}

func TestCodexGuardianRealRolloutAndNormalParent(t *testing.T) {
	for _, kind := range []string{"guardian", "parent"} {
		t.Run(kind, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", "codex-0163-"+kind+".jsonl"))
			require.NoError(t, err)
			path := filepath.Join(t.TempDir(), "rollout-2026-10-22-"+kind+".jsonl")
			require.NoError(t, os.WriteFile(path, data, 0600))
			got, err := parseCodexSession(path, 1)
			require.NoError(t, err)
			require.NotNil(t, got)
			assert.Equal(t, kind == "guardian", got.Excluded)
			if kind == "parent" {
				assert.Greater(t, got.MessageCount, 1)
				assert.Contains(t, got.FTSBody, "GUARDIAN_PROOF_DONE")
			}
			f, err := os.Open(path)
			require.NoError(t, err)
			defer f.Close()
			_, err = f.Seek(7, 0)
			require.NoError(t, err)
			assert.Equal(t, kind == "guardian", IsCodexGuardianRollout(f))
			pos, err := f.Seek(0, 1)
			require.NoError(t, err)
			assert.EqualValues(t, 7, pos)
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, data, after, "source is untouched")
		})
	}
	assert.False(t, IsCodexGuardianRollout(strings.NewReader(strings.Repeat("x", (1<<20)+1))))
}

func TestCodexIndexKeepsRepeatedMessagesButMergesFormatCopies(t *testing.T) {
	p := filepath.Join(t.TempDir(), "rollout-repeat.jsonl")
	require.NoError(t, os.WriteFile(p, []byte(`{"timestamp":"2026-10-22T00:00:00Z","type":"event_msg","payload":{"type":"agent_message","message":"guardian_review is ordinary text"}}
{"timestamp":"2026-10-22T00:00:00Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"guardian_review is ordinary text"}]}}
{"timestamp":"2026-10-22T00:00:00Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"guardian_review is ordinary text"}]}}
`), 0600))
	got, err := parseCodexSession(p, 1)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, 2, got.MessageCount)
	assert.Contains(t, got.DisplaySummary, "guardian_review")
}
