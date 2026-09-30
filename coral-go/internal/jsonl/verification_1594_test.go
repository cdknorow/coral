package jsonl

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestTask1594_IncrementalCodexActivityTracking independently verifies the
// complete #1592 candidate and integrated marker hardening.
func TestTask1594_IncrementalCodexActivityTracking(t *testing.T) {
	tempBase := filepath.Join(t.TempDir(), "sessions")
	require.NoError(t, os.MkdirAll(filepath.Join(tempBase, "2026", "09", "29"), 0755))

	coralID_A := "f0736582-0619-0882-abde-b5d8d69911b4" // Game Design Director
	coralID_B := "97d6c65d-3333-4444-5555-666666666666" // Frontend Dev

	t.Run("1_BaselineFalseIdleVsFinalIncrementalBeyond8MiB", func(t *testing.T) {
		p := filepath.Join(tempBase, "2026", "09", "29", "rollout-huge-turn.jsonl")
		var b bytes.Buffer
		meta := fmt.Sprintf(`{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"CORAL_SESSION_ID: %s"}]}}`+"\n", coralID_A)
		b.WriteString(meta)
		b.WriteString(`{"timestamp":"2026-09-30T04:00:00Z","type":"event_msg","payload":{"type":"task_started"}}` + "\n")

		// Create >8 MiB of intervening tool records (e.g. ~9.5 MiB)
		chunk := strings.Repeat("z", 4096)
		for i := 0; i < 2350; i++ {
			b.WriteString(fmt.Sprintf(`{"timestamp":"2026-09-30T04:05:00Z","type":"response_item","payload":{"type":"custom_tool_call_output","data":"%s"}}`+"\n", chunk))
		}
		require.NoError(t, os.WriteFile(p, b.Bytes(), 0644))

		// 1. Original 1 MiB window (#1587) fails on >1 MiB:
		// (Simulated with CodexTurnEvent bounded to 1 MiB)
		evt1M, _ := scanCodexTail(p, 1024*1024)
		require.Empty(t, evt1M, "baseline 1 MiB scan must reproduce false idle on >1 MiB turn")

		// 2. Mitigation 8 MiB window (#1591) fails on >8 MiB:
		evt8M, _ := CodexTurnEvent(p) // uses 8 MiB limit
		require.Empty(t, evt8M, "mitigation 8 MiB scan must fail when turn > 8 MiB")

		// 3. Durable incremental cache (#1592) retains active prompt_submit state:
		var state codexTurnCache
		evtInc, atInc := readCodexTurnEventIncremental(p, &state)
		require.Equal(t, "prompt_submit", evtInc, "incremental tracker must preserve active state beyond 8 MiB")
		require.False(t, atInc.IsZero())
	})

	t.Run("2_FullLifecycleTransitions_StartWorkCompleteAbortSubsequent", func(t *testing.T) {
		p := filepath.Join(tempBase, "2026", "09", "29", "rollout-lifecycle.jsonl")
		meta := fmt.Sprintf(`{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"CORAL_SESSION_ID: %s"}]}}`+"\n", coralID_A)
		require.NoError(t, os.WriteFile(p, []byte(meta), 0644))

		var state codexTurnCache

		// 2a. Initial state: no turn events
		evt, atTime := readCodexTurnEventIncremental(p, &state)
		require.Empty(t, evt)
		require.True(t, atTime.IsZero())

		// 2b. task_started initiates active turn
		appendToFile(t, p, `{"timestamp":"2026-09-30T01:00:00Z","type":"event_msg","payload":{"type":"task_started"}}`+"\n")
		evt, atTime = readCodexTurnEventIncremental(p, &state)
		require.Equal(t, "prompt_submit", evt)
		require.Equal(t, "2026-09-30T01:00:00Z", atTime.Format(time.RFC3339))

		// 2c. intermediate tool activity advances timestamp while keeping prompt_submit
		appendToFile(t, p, `{"timestamp":"2026-09-30T01:05:00Z","type":"response_item","payload":{"type":"custom_tool_call_output","out":"done"}}`+"\n")
		evt, atTime = readCodexTurnEventIncremental(p, &state)
		require.Equal(t, "prompt_submit", evt)
		require.Equal(t, "2026-09-30T01:05:00Z", atTime.Format(time.RFC3339))

		// 2d. task_complete transitions to stop
		appendToFile(t, p, `{"timestamp":"2026-09-30T01:10:00Z","type":"event_msg","payload":{"type":"task_complete"}}`+"\n")
		evt, atTime = readCodexTurnEventIncremental(p, &state)
		require.Equal(t, "stop", evt)
		require.Equal(t, "2026-09-30T01:10:00Z", atTime.Format(time.RFC3339))

		// 2e. subsequent task_started transitions back to prompt_submit
		appendToFile(t, p, `{"timestamp":"2026-09-30T01:20:00Z","type":"event_msg","payload":{"type":"task_started"}}`+"\n")
		evt, atTime = readCodexTurnEventIncremental(p, &state)
		require.Equal(t, "prompt_submit", evt)
		require.Equal(t, "2026-09-30T01:20:00Z", atTime.Format(time.RFC3339))

		// 2f. turn_aborted transitions to session_reset
		appendToFile(t, p, `{"timestamp":"2026-09-30T01:25:00Z","type":"event_msg","payload":{"type":"turn_aborted"}}`+"\n")
		evt, atTime = readCodexTurnEventIncremental(p, &state)
		require.Equal(t, "session_reset", evt)
		require.Equal(t, "2026-09-30T01:25:00Z", atTime.Format(time.RFC3339))
	})

	t.Run("3_ColdAndWarmCacheEfficiency", func(t *testing.T) {
		p := filepath.Join(tempBase, "2026", "09", "29", "rollout-perf.jsonl")
		var b bytes.Buffer
		b.WriteString(fmt.Sprintf(`{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"CORAL_SESSION_ID: %s"}]}}`+"\n", coralID_A))
		b.WriteString(`{"timestamp":"2026-09-30T02:00:00Z","type":"event_msg","payload":{"type":"task_started"}}` + "\n")
		for i := 0; i < 5000; i++ {
			b.WriteString(fmt.Sprintf(`{"timestamp":"2026-09-30T02:00:%02dZ","type":"response_item","payload":{"type":"message","text":"chunk %d"}}`+"\n", i%60, i))
		}
		require.NoError(t, os.WriteFile(p, b.Bytes(), 0644))

		var state codexTurnCache

		// Cold scan
		t0 := time.Now()
		evt, _ := readCodexTurnEventIncremental(p, &state)
		coldDuration := time.Since(t0)
		require.Equal(t, "prompt_submit", evt)
		require.True(t, state.initialized)
		require.Greater(t, state.offset, int64(0))

		// Warm scan (no new data) - must be extremely fast (< 1ms)
		t1 := time.Now()
		for i := 0; i < 100; i++ {
			evtWarm, _ := readCodexTurnEventIncremental(p, &state)
			require.Equal(t, "prompt_submit", evtWarm)
		}
		warmAvg := time.Since(t1) / 100
		t.Logf("Cold scan: %v, Warm poll avg: %v", coldDuration, warmAvg)
		require.Less(t, warmAvg, 5*time.Millisecond, "warm poll must be negligible cost")
	})

	t.Run("4_PartialAppends_TrailingPartialLineRetained", func(t *testing.T) {
		p := filepath.Join(tempBase, "2026", "09", "29", "rollout-partial.jsonl")
		require.NoError(t, os.WriteFile(p, []byte(`{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"CORAL_SESSION_ID: test"}]}}`+"\n"), 0644))

		var state codexTurnCache
		_, _ = readCodexTurnEventIncremental(p, &state)

		// Append an incomplete JSON line (simulating in-flight chunk write)
		partial := `{"timestamp":"2026-09-30T03:00:00Z","type":"event_msg","pay`
		appendToFile(t, p, partial)

		evt, _ := readCodexTurnEventIncremental(p, &state)
		require.Empty(t, evt, "partial line must not trigger state update")
		require.Equal(t, []byte(partial), state.partial, "partial buffer must retain incomplete line")

		// Complete the partial line with newline
		remainder := `load":{"type":"task_started"}}` + "\n"
		appendToFile(t, p, remainder)

		evt, atTime := readCodexTurnEventIncremental(p, &state)
		require.Equal(t, "prompt_submit", evt, "completing line must trigger state update")
		require.Equal(t, "2026-09-30T03:00:00Z", atTime.Format(time.RFC3339))
		require.Empty(t, state.partial, "partial buffer must be emptied after complete line processed")
	})

	t.Run("5_TruncateAndReplaceResetsCleanly", func(t *testing.T) {
		p := filepath.Join(tempBase, "2026", "09", "29", "rollout-replace.jsonl")
		initial := `{"timestamp":"2026-09-30T04:00:00Z","type":"event_msg","payload":{"type":"task_started"}}` + "\n"
		initial += `{"timestamp":"2026-09-30T04:10:00Z","type":"event_msg","payload":{"type":"task_complete"}}` + "\n"
		require.NoError(t, os.WriteFile(p, []byte(initial), 0644))

		var state codexTurnCache
		evt, _ := readCodexTurnEventIncremental(p, &state)
		require.Equal(t, "stop", evt)

		// Case 5a: File truncated to smaller size
		truncated := `{"timestamp":"2026-09-30T04:00:00Z","type":"event_msg","payload":{"type":"task_started"}}` + "\n"
		require.NoError(t, os.WriteFile(p, []byte(truncated), 0644))

		evt, atTime := readCodexTurnEventIncremental(p, &state)
		require.Equal(t, "prompt_submit", evt, "file truncation must trigger reset and re-scan")
		require.Equal(t, "2026-09-30T04:00:00Z", atTime.Format(time.RFC3339))

		// Case 5b: File replaced with different header
		replaced := `{"timestamp":"2026-09-30T05:00:00Z","type":"event_msg","payload":{"type":"turn_aborted"}}` + "\n"
		require.NoError(t, os.WriteFile(p, []byte(replaced), 0644))

		evt, atTime = readCodexTurnEventIncremental(p, &state)
		require.Equal(t, "session_reset", evt, "header replacement must trigger reset and re-scan")
		require.Equal(t, "2026-09-30T05:00:00Z", atTime.Format(time.RFC3339))
	})

	t.Run("6_DeletedAndMissingPathSafety", func(t *testing.T) {
		var state codexTurnCache
		evt, atTime := readCodexTurnEventIncremental("", &state)
		require.Empty(t, evt)
		require.True(t, atTime.IsZero())

		missingPath := filepath.Join(tempBase, "2026", "09", "29", "nonexistent.jsonl")
		evt, atTime = readCodexTurnEventIncremental(missingPath, &state)
		require.Empty(t, evt)
		require.True(t, atTime.IsZero())
	})

	t.Run("7_ConcurrentPollingAndSameCwdIsolation", func(t *testing.T) {
		rolloutA := filepath.Join(tempBase, "2026", "09", "29", "rollout-concurrent-A.jsonl")
		rolloutB := filepath.Join(tempBase, "2026", "09", "29", "rollout-concurrent-B.jsonl")

		metaA := fmt.Sprintf(`{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"CORAL_SESSION_ID: %s"}]}}`+"\n", coralID_A)
		metaB := fmt.Sprintf(`{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"CORAL_SESSION_ID: %s"}]}}`+"\n", coralID_B)

		require.NoError(t, os.WriteFile(rolloutA, []byte(metaA+`{"timestamp":"2026-09-30T06:00:00Z","type":"event_msg","payload":{"type":"task_started"}}`+"\n"), 0644))
		require.NoError(t, os.WriteFile(rolloutB, []byte(metaB+`{"timestamp":"2026-09-30T06:00:00Z","type":"event_msg","payload":{"type":"task_complete"}}`+"\n"), 0644))

		reader := NewSessionReader()
		// Inject paths into reader
		reader.cache[coralID_A] = &sessionCache{path: rolloutA}
		reader.cache[coralID_B] = &sessionCache{path: rolloutB}

		var wg sync.WaitGroup
		errCh := make(chan error, 20)

		for worker := 0; worker < 10; worker++ {
			wg.Add(2)
			go func() {
				defer wg.Done()
				for i := 0; i < 50; i++ {
					evtA, _ := reader.ReadCodexTurnEvent(coralID_A, "/repo")
					if evtA != "prompt_submit" {
						errCh <- fmt.Errorf("expected prompt_submit for A, got %q", evtA)
						return
					}
				}
			}()
			go func() {
				defer wg.Done()
				for i := 0; i < 50; i++ {
					evtB, _ := reader.ReadCodexTurnEvent(coralID_B, "/repo")
					if evtB != "stop" {
						errCh <- fmt.Errorf("expected stop for B, got %q", evtB)
						return
					}
				}
			}()
		}
		wg.Wait()
		close(errCh)

		for err := range errCh {
			t.Fatal(err)
		}
	})

	t.Run("8_ReadCodexTurnEvent_EndToEnd_LongTurnAndLifecycle", func(t *testing.T) {
		rolloutPath := filepath.Join(tempBase, "2026", "09", "29", "rollout-e2e.jsonl")
		meta := fmt.Sprintf(`{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"CORAL_SESSION_ID: %s"}]}}`+"\n", coralID_A)
		require.NoError(t, os.WriteFile(rolloutPath, []byte(meta), 0644))

		reader := NewSessionReader()
		reader.cache[coralID_A] = &sessionCache{path: rolloutPath}

		// 8a. Initial state: empty
		evt, _ := reader.ReadCodexTurnEvent(coralID_A, "/repo")
		require.Empty(t, evt)

		// 8b. task_started: prompt_submit
		appendToFile(t, rolloutPath, `{"timestamp":"2026-09-30T07:00:00Z","type":"event_msg","payload":{"type":"task_started"}}`+"\n")
		evt, atTime := reader.ReadCodexTurnEvent(coralID_A, "/repo")
		require.Equal(t, "prompt_submit", evt)
		require.Equal(t, "2026-09-30T07:00:00Z", atTime.Format(time.RFC3339))

		// 8c. Long turn with 2 MiB of tool output: retains prompt_submit and updates timestamp
		chunk := strings.Repeat("y", 1024)
		for i := 0; i < 2000; i++ {
			appendToFile(t, rolloutPath, fmt.Sprintf(`{"timestamp":"2026-09-30T07:05:00Z","type":"response_item","payload":{"type":"custom_tool_call_output","data":"%s"}}`+"\n", chunk))
		}
		evt, atTime = reader.ReadCodexTurnEvent(coralID_A, "/repo")
		require.Equal(t, "prompt_submit", evt, "agent must remain prompt_submit during long multi-megabyte turn")
		require.Equal(t, "2026-09-30T07:05:00Z", atTime.Format(time.RFC3339))

		// 8d. task_complete: transitions to stop
		appendToFile(t, rolloutPath, `{"timestamp":"2026-09-30T07:10:00Z","type":"event_msg","payload":{"type":"task_complete"}}`+"\n")
		evt, atTime = reader.ReadCodexTurnEvent(coralID_A, "/repo")
		require.Equal(t, "stop", evt, "task_complete must transition to stop")
		require.Equal(t, "2026-09-30T07:10:00Z", atTime.Format(time.RFC3339))
	})
}

func appendToFile(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	require.NoError(t, err)
	defer f.Close()
	_, err = f.WriteString(content)
	require.NoError(t, err)
}

func scanCodexTail(path string, limit int64) (string, time.Time) {
	f, err := os.Open(path)
	if err != nil {
		return "", time.Time{}
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", time.Time{}
	}
	start := info.Size() - limit
	if start < 0 {
		start = 0
	}
	if _, err = f.Seek(start, 0); err != nil {
		return "", time.Time{}
	}
	var b bytes.Buffer
	_, _ = io.Copy(&b, f)
	data := b.Bytes()
	if start > 0 {
		idx := bytes.IndexByte(data, '\n')
		if idx >= 0 {
			data = data[idx+1:]
		}
	}
	lines := bytes.Split(data, []byte{'\n'})
	active := false
	var evt string
	var at time.Time
	for _, l := range lines[:len(lines)-1] {
		var entry struct {
			Timestamp string `json:"timestamp"`
			Type      string `json:"type"`
			Payload   struct {
				Type string `json:"type"`
			} `json:"payload"`
		}
		if json.Unmarshal(l, &entry) != nil || entry.Type != "event_msg" {
			var activity struct {
				Timestamp string `json:"timestamp"`
			}
			if active && json.Unmarshal(l, &activity) == nil {
				if t, err := time.Parse(time.RFC3339Nano, activity.Timestamp); err == nil {
					evt, at = "prompt_submit", t
				}
			}
			continue
		}
		switch entry.Payload.Type {
		case "task_started", "user_message":
			evt, at = "prompt_submit", time.Now()
			active = true
		case "task_complete":
			evt, at = "stop", time.Now()
			active = false
		}
	}
	return evt, at
}
