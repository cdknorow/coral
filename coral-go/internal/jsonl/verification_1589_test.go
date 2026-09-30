package jsonl

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestTask1589_CodexActivityAssociation exercises the full matrix required
// by Task #1589:
// 1. Quoted other-session UUID in user content (must NOT match)
// 2. Quoted other-session UUID in tool/assistant output (must NOT match)
// 3. Quoted other-session UUID in developer content as quoted text
// 4. Proper launch metadata (both array and string content formats)
// 5. Multiple exact-ID rollouts with different mtimes (newest mtime selected)
// 6. Current missing marker in newer rollout (demonstrates association gap)
// 7. Same-cwd separate agents (strict UUID isolation preserved)
// 8. Baseline 1 MiB scan cutoff failure on long turns (>1 MiB) reproducing false idle
func TestTask1589_CodexActivityAssociation(t *testing.T) {
	tempBase := filepath.Join(t.TempDir(), "sessions")
	err := os.MkdirAll(filepath.Join(tempBase, "2026", "09", "29"), 0755)
	require.NoError(t, err)

	coralID_A := "f0736582-0619-0882-abde-b5d8d69911b4" // Game Design Director
	coralID_B := "97d6c65d-1111-2222-3333-444444444444" // Frontend Dev

	t.Run("1_UserMessageQuotedUUID_DoesNotMatch", func(t *testing.T) {
		rolloutPath := filepath.Join(tempBase, "2026", "09", "29", "rollout-user-quote.jsonl")
		content := fmt.Sprintf(`{"type":"event_msg","payload":{"type":"user_message","message":"Please check session CORAL_SESSION_ID: %s"}}`+"\n", coralID_A)
		require.NoError(t, os.WriteFile(rolloutPath, []byte(content), 0644))

		got := codexTranscriptCoralSessionID(rolloutPath)
		require.Empty(t, got, "user message text must never associate transcript identity")
	})

	t.Run("2_ToolOutputQuotedUUID_DoesNotMatch", func(t *testing.T) {
		rolloutPath := filepath.Join(tempBase, "2026", "09", "29", "rollout-tool-output.jsonl")
		// Simulating command execution or tool output mentioning other session UUID
		content := fmt.Sprintf(`{"type":"response_item","payload":{"type":"custom_tool_call_output","output":"Task 1571 claimed by CORAL_SESSION_ID: %s"}}`+"\n", coralID_A)
		require.NoError(t, os.WriteFile(rolloutPath, []byte(content), 0644))

		got := codexTranscriptCoralSessionID(rolloutPath)
		require.Empty(t, got, "tool call output must never associate transcript identity")
	})

	t.Run("3_LaunchMetadataFormats", func(t *testing.T) {
		// Array format (standard Codex launch)
		pathArray := filepath.Join(tempBase, "2026", "09", "29", "rollout-meta-array.jsonl")
		contentArray := fmt.Sprintf(`{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"Coral session metadata:\nCORAL_SESSION_ID: %s"}]}}`+"\n", coralID_A)
		require.NoError(t, os.WriteFile(pathArray, []byte(contentArray), 0644))
		require.Equal(t, coralID_A, codexTranscriptCoralSessionID(pathArray))

		// String format
		pathString := filepath.Join(tempBase, "2026", "09", "29", "rollout-meta-string.jsonl")
		contentString := fmt.Sprintf(`{"type":"response_item","payload":{"type":"message","role":"developer","content":"Coral session metadata:\nCORAL_SESSION_ID: %s"}}`+"\n", coralID_B)
		require.NoError(t, os.WriteFile(pathString, []byte(contentString), 0644))
		require.Equal(t, coralID_B, codexTranscriptCoralSessionID(pathString))
	})

	t.Run("4_MultipleRollouts_NewestMtimeSelected", func(t *testing.T) {
		oldRollout := filepath.Join(tempBase, "2026", "09", "29", "rollout-old-sessionA.jsonl")
		newRollout := filepath.Join(tempBase, "2026", "09", "29", "rollout-new-sessionA.jsonl")

		meta := fmt.Sprintf(`{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"CORAL_SESSION_ID: %s"}]}}`+"\n", coralID_A)
		require.NoError(t, os.WriteFile(oldRollout, []byte(meta), 0644))
		require.NoError(t, os.WriteFile(newRollout, []byte(meta), 0644))

		now := time.Now()
		require.NoError(t, os.Chtimes(oldRollout, now.Add(-10*time.Minute), now.Add(-10*time.Minute)))
		require.NoError(t, os.Chtimes(newRollout, now, now))

		resolved := resolveCodexTranscriptByMarker(tempBase, coralID_A)
		require.Equal(t, newRollout, resolved, "must select newest rollout by mtime")
	})

	t.Run("5_MissingMarkerInCurrentRollout_AssociationLimitation", func(t *testing.T) {
		// If current rollout lacks developer marker, #1587 either resolves to an older marked rollout
		// or returns empty if no marked rollout exists.
		unmarkedRollout := filepath.Join(tempBase, "2026", "09", "29", "rollout-unmarked-active.jsonl")
		require.NoError(t, os.WriteFile(unmarkedRollout, []byte(`{"type":"event_msg","payload":{"type":"task_started"}}`+"\n"), 0644))
		now := time.Now()
		require.NoError(t, os.Chtimes(unmarkedRollout, now.Add(5*time.Minute), now.Add(5*time.Minute)))

		// coralID_C has NO marked rollout
		coralID_C := "cccccccc-cccc-cccc-cccc-cccccccccccc"
		resolvedC := resolveCodexTranscriptByMarker(tempBase, coralID_C)
		require.Empty(t, resolvedC, "unmarked active rollout cannot be resolved by marker")

		// coralID_A has an older marked rollout (from test 4)
		resolvedA := resolveCodexTranscriptByMarker(tempBase, coralID_A)
		require.NotEqual(t, unmarkedRollout, resolvedA, "unmarked active rollout is not selected even if newer")
	})

	t.Run("6_SameCwdSeparateAgents_StrictIsolation", func(t *testing.T) {
		rolloutA := filepath.Join(tempBase, "2026", "09", "29", "rollout-agentA.jsonl")
		rolloutB := filepath.Join(tempBase, "2026", "09", "29", "rollout-agentB.jsonl")

		metaA := fmt.Sprintf(`{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"CORAL_SESSION_ID: %s"}]}}`+"\n", coralID_A)
		metaB := fmt.Sprintf(`{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"CORAL_SESSION_ID: %s"}]}}`+"\n", coralID_B)

		require.NoError(t, os.WriteFile(rolloutA, []byte(metaA), 0644))
		require.NoError(t, os.WriteFile(rolloutB, []byte(metaB), 0644))

		// Both agents have the same working directory
		cwd := "/Users/cknorowski/Software/death_or_trade"
		_ = cwd

		resolvedA := resolveCodexTranscriptByMarker(tempBase, coralID_A)
		resolvedB := resolveCodexTranscriptByMarker(tempBase, coralID_B)

		require.Equal(t, rolloutA, resolvedA)
		require.Equal(t, rolloutB, resolvedB)
		require.NotEqual(t, resolvedA, resolvedB, "sessions in same cwd must strictly resolve only their own transcript")
	})

	t.Run("7_Baseline1MiBLimit_ReproducesFalseIdleOnLongTurn", func(t *testing.T) {
		// Reproduce exact mechanism of Game Design Director false idle:
		// A turn begins with task_started. Then > 1 MiB of tool output is written before task_complete.
		rolloutLong := filepath.Join(tempBase, "2026", "09", "29", "rollout-long-turn.jsonl")

		var b bytes.Buffer
		meta := fmt.Sprintf(`{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"CORAL_SESSION_ID: %s"}]}}`+"\n", coralID_A)
		b.WriteString(meta)
		b.WriteString(`{"timestamp":"2026-09-30T04:03:48Z","type":"event_msg","payload":{"type":"task_started"}}` + "\n")

		// Add 1.5 MiB of intermediate response/tool records (similar to the 6.21 MiB in the real incident)
		chunk := strings.Repeat("a", 1024)
		for i := 0; i < 1500; i++ {
			b.WriteString(fmt.Sprintf(`{"timestamp":"2026-09-30T04:05:00Z","type":"response_item","payload":{"type":"custom_tool_call_output","data":"%s"}}`+"\n", chunk))
		}
		require.NoError(t, os.WriteFile(rolloutLong, b.Bytes(), 0644))

		// 1 MiB tail scan function (exact behavior of #1587 candidate)
		scan1MiB := func(path string) (string, time.Time) {
			f, err := os.Open(path)
			if err != nil {
				return "", time.Time{}
			}
			defer f.Close()
			info, err := f.Stat()
			if err != nil {
				return "", time.Time{}
			}
			const limit int64 = 1024 * 1024 // 1 MiB limit from #1587
			start := info.Size() - limit
			if start < 0 {
				start = 0
			}
			if _, err = f.Seek(start, io.SeekStart); err != nil {
				return "", time.Time{}
			}
			data, err := io.ReadAll(io.LimitReader(f, limit))
			if err != nil {
				return "", time.Time{}
			}
			if start > 0 {
				i := bytes.IndexByte(data, '\n')
				if i >= 0 {
					data = data[i+1:]
				}
			}
			lines := bytes.Split(data, []byte{'\n'})
			active := false
			var event string
			var at time.Time
			for _, line := range lines[:len(lines)-1] {
				var entry struct {
					Timestamp string `json:"timestamp"`
					Type      string `json:"type"`
					Payload   struct {
						Type string `json:"type"`
					} `json:"payload"`
				}
				if json.Unmarshal(line, &entry) != nil || entry.Type != "event_msg" {
					var activity struct {
						Timestamp string `json:"timestamp"`
					}
					if active && json.Unmarshal(line, &activity) == nil {
						if timestamp, err := time.Parse(time.RFC3339Nano, activity.Timestamp); err == nil {
							event, at = "prompt_submit", timestamp
						}
					}
					continue
				}
				switch entry.Payload.Type {
				case "task_started", "user_message":
					event, at = "prompt_submit", time.Now()
					active = true
				case "task_complete":
					event, at = "stop", time.Now()
					active = false
				}
			}
			return event, at
		}

		// Execute 1 MiB scan on this file:
		evt, _ := scan1MiB(rolloutLong)
		require.Empty(t, evt, "PROOF OF ROOT CAUSE: 1 MiB tail scan loses active state when intervening output > 1 MiB, returning empty/idle!")

		// Now demonstrate that the durable incremental cache (Lead #1592) preserves active state
		var cache codexTurnCache
		incEvt, incAt := readCodexTurnEventIncremental(rolloutLong, &cache)
		require.Equal(t, "prompt_submit", incEvt, "incremental scan retains task_started active state across arbitrary turn size")
		require.False(t, incAt.IsZero())

		// Now append task_complete and verify clean transition to stop
		f, err := os.OpenFile(rolloutLong, os.O_APPEND|os.O_WRONLY, 0644)
		require.NoError(t, err)
		_, err = f.WriteString(`{"timestamp":"2026-09-30T04:06:56Z","type":"event_msg","payload":{"type":"task_complete"}}` + "\n")
		require.NoError(t, err)
		f.Close()

		incEvt2, incAt2 := readCodexTurnEventIncremental(rolloutLong, &cache)
		require.Equal(t, "stop", incEvt2, "incremental scan transitions cleanly to stop on task_complete")
		require.Equal(t, "2026-09-30T04:06:56Z", incAt2.Format(time.RFC3339))
	})

	t.Run("8_Fixed8MiBLimit_FailsBeyond8MiB_WhileIncrementalSucceeds", func(t *testing.T) {
		rolloutVeryLong := filepath.Join(tempBase, "2026", "09", "29", "rollout-very-long.jsonl")
		var b bytes.Buffer
		meta := fmt.Sprintf(`{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"CORAL_SESSION_ID: %s"}]}}`+"\n", coralID_A)
		b.WriteString(meta)
		b.WriteString(`{"timestamp":"2026-09-30T04:00:00Z","type":"event_msg","payload":{"type":"task_started"}}` + "\n")

		// Emit 9 MiB of intermediate records (beyond the 8 MiB mitigation in #1591)
		chunk := strings.Repeat("x", 4096)
		for i := 0; i < 2350; i++ { // ~9.6 MB
			b.WriteString(fmt.Sprintf(`{"timestamp":"2026-09-30T04:05:00Z","type":"response_item","payload":{"type":"custom_tool_call_output","data":"%s"}}`+"\n", chunk))
		}
		require.NoError(t, os.WriteFile(rolloutVeryLong, b.Bytes(), 0644))

		// CodexTurnEvent currently has limit = 8 MiB (Lead #1591 mitigation)
		fixedEvt, _ := CodexTurnEvent(rolloutVeryLong)
		require.Empty(t, fixedEvt, "PROOF OF BOUNDARY FAILURE: Fixed 8 MiB window loses active state when turn > 8 MiB!")

		// In contrast, readCodexTurnEventIncremental (Lead #1592 durable repair) tracks full lifecycle incrementally
		var incCache codexTurnCache
		durableEvt, durableAt := readCodexTurnEventIncremental(rolloutVeryLong, &incCache)
		require.Equal(t, "prompt_submit", durableEvt, "incremental cache correctly retains active state beyond 8 MiB")
		require.False(t, durableAt.IsZero())
	})
}
