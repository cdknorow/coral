package subagent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	fixtureDir        = "testdata/session-dir/subagents"
	fixtureID         = "a1b2c3d4e5f6a7b8c"
	fixtureTranscript = fixtureDir + "/agent-" + fixtureID + ".jsonl"
	fixtureMeta       = fixtureDir + "/agent-" + fixtureID + ".meta.json"
	fixtureSession    = "11111111-2222-3333-4444-555555555555"
)

// rec builds one transcript line. usage is [input, output, cacheRead, cacheWrite];
// pass nil for a record with no usage block.
func rec(typ, msgID, model, ts string, usage []int) string {
	m := map[string]any{"role": typ}
	if msgID != "" {
		m["id"] = msgID
	}
	if model != "" {
		m["model"] = model
	}
	if usage != nil {
		m["usage"] = map[string]int{
			"input_tokens": usage[0], "output_tokens": usage[1],
			"cache_read_input_tokens": usage[2], "cache_creation_input_tokens": usage[3],
		}
	}
	b, _ := json.Marshal(map[string]any{
		"type": typ, "timestamp": ts, "agentId": "agentX", "sessionId": "parent-sess",
		"isSidechain": true, "message": m,
	})
	return string(b)
}

func writeTranscript(t *testing.T, name string, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644))
	return path
}

// ── Realistic fixture ────────────────────────────────────────

func TestParseTranscript_Fixture(t *testing.T) {
	tr, err := ParseTranscript(fixtureTranscript)
	require.NoError(t, err)

	assert.Equal(t, fixtureID, tr.SubagentID)
	assert.Equal(t, fixtureSession, tr.ParentSessionID, "sessionId on the records is the launching session")
	assert.Equal(t, "2026-09-17T02:10:18.133Z", tr.StartedAt, "first record, even though it is a user record")
	assert.Equal(t, "2026-09-17T02:14:11.667Z", tr.LastActivityAt)

	// Four assistant records, but only two API requests: msg_01AAA was
	// written three times, once per content block.
	require.Len(t, tr.Calls, 2)
	assert.Equal(t, Call{
		MessageID: "msg_01AAA", Model: "claude-opus-5", Timestamp: "2026-09-17T02:10:21.250Z",
		InputTokens: 2, OutputTokens: 180, CacheReadTokens: 9913, CacheWriteTokens: 6994,
	}, tr.Calls[0])
	assert.Equal(t, Call{
		MessageID: "msg_01BBB", Model: "claude-opus-5", Timestamp: "2026-09-17T02:14:11.667Z",
		InputTokens: 3, OutputTokens: 420, CacheReadTokens: 16907, CacheWriteTokens: 310,
	}, tr.Calls[1])

	assert.Equal(t, Totals{
		APICalls: 2, InputTokens: 5, OutputTokens: 600, CacheReadTokens: 26820, CacheWriteTokens: 7304,
	}, tr.Totals())
	assert.Equal(t, "claude-opus-5", tr.PrimaryModel())
}

// ── De-duplication of streamed records ───────────────────────

// The core correctness property. Summing every record would report output=190
// and triple the cache tokens; keeping the first record would report output=5.
func TestParse_StreamedRecordsCountOnceAtFinalValue(t *testing.T) {
	path := writeTranscript(t, "agent-x.jsonl",
		rec("assistant", "msg_1", "claude-opus-5", "2026-01-01T00:00:01Z", []int{2, 5, 1000, 50}),
		rec("assistant", "msg_1", "claude-opus-5", "2026-01-01T00:00:02Z", []int{2, 5, 1000, 50}),
		rec("assistant", "msg_1", "claude-opus-5", "2026-01-01T00:00:03Z", []int{2, 180, 1000, 50}),
	)
	tr, err := ParseTranscript(path)
	require.NoError(t, err)
	assert.Equal(t, Totals{APICalls: 1, InputTokens: 2, OutputTokens: 180, CacheReadTokens: 1000, CacheWriteTokens: 50}, tr.Totals())
	assert.Equal(t, "2026-01-01T00:00:03Z", tr.Calls[0].Timestamp)
}

func TestParse_MaxIsTakenPerCounterRegardlessOfRecordOrder(t *testing.T) {
	path := writeTranscript(t, "agent-x.jsonl",
		rec("assistant", "msg_1", "m", "2026-01-01T00:00:01Z", []int{2, 180, 1000, 50}),
		rec("assistant", "msg_1", "m", "2026-01-01T00:00:02Z", []int{2, 5, 1000, 50}), // stale snapshot written late
	)
	tr, err := ParseTranscript(path)
	require.NoError(t, err)
	assert.Equal(t, 180, tr.Totals().OutputTokens)
}

func TestParse_InterleavedMessagesStayDistinctAndOrdered(t *testing.T) {
	path := writeTranscript(t, "agent-x.jsonl",
		rec("assistant", "msg_1", "m", "2026-01-01T00:00:01Z", []int{1, 10, 0, 0}),
		rec("assistant", "msg_2", "m", "2026-01-01T00:00:02Z", []int{1, 20, 0, 0}),
		rec("assistant", "msg_1", "m", "2026-01-01T00:00:03Z", []int{1, 15, 0, 0}),
	)
	tr, err := ParseTranscript(path)
	require.NoError(t, err)
	require.Len(t, tr.Calls, 2)
	assert.Equal(t, "msg_1", tr.Calls[0].MessageID, "ordered by first appearance")
	assert.Equal(t, 15, tr.Calls[0].OutputTokens)
	assert.Equal(t, 20, tr.Calls[1].OutputTokens)
}

func TestParse_RecordsWithoutMessageIDAreNotMerged(t *testing.T) {
	path := writeTranscript(t, "agent-x.jsonl",
		rec("assistant", "", "m", "2026-01-01T00:00:01Z", []int{1, 10, 0, 0}),
		rec("assistant", "", "m", "2026-01-01T00:00:02Z", []int{1, 10, 0, 0}),
	)
	tr, err := ParseTranscript(path)
	require.NoError(t, err)
	assert.Equal(t, 2, tr.Totals().APICalls, "with no id there is nothing to group on, so neither may be dropped")
	assert.Equal(t, 20, tr.Totals().OutputTokens)
}

// ── What does and does not count as spend ────────────────────

func TestParse_OnlyAssistantRecordsWithUsageCount(t *testing.T) {
	path := writeTranscript(t, "agent-x.jsonl",
		rec("user", "", "", "2026-01-01T00:00:00Z", nil),
		rec("user", "msg_u", "m", "2026-01-01T00:00:01Z", []int{999, 999, 999, 999}), // usage on a non-assistant record
		rec("assistant", "msg_nousage", "m", "2026-01-01T00:00:02Z", nil),
		rec("assistant", "msg_zero", "<synthetic>", "2026-01-01T00:00:03Z", []int{0, 0, 0, 0}),
		rec("assistant", "msg_real", "m", "2026-01-01T00:00:04Z", []int{1, 2, 3, 4}),
		rec("system", "", "", "2026-01-01T00:00:05Z", nil),
	)
	tr, err := ParseTranscript(path)
	require.NoError(t, err)
	assert.Equal(t, Totals{APICalls: 1, InputTokens: 1, OutputTokens: 2, CacheReadTokens: 3, CacheWriteTokens: 4}, tr.Totals())
	assert.Equal(t, "2026-01-01T00:00:00Z", tr.StartedAt)
	assert.Equal(t, "2026-01-01T00:00:05Z", tr.LastActivityAt, "activity spans all records, not just billable ones")
}

func TestParse_CacheOnlyUsageCounts(t *testing.T) {
	path := writeTranscript(t, "agent-x.jsonl",
		rec("assistant", "msg_1", "m", "2026-01-01T00:00:01Z", []int{0, 0, 5000, 0}),
	)
	tr, err := ParseTranscript(path)
	require.NoError(t, err)
	assert.Equal(t, 5000, tr.Totals().CacheReadTokens, "input_tokens == 0 must not cause the record to be skipped")
}

// ── Robustness ───────────────────────────────────────────────

// The file is appended to while it is read, so a torn last line is routine.
func TestParse_TornAndMalformedLinesAreSkipped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-x.jsonl")
	content := rec("assistant", "msg_1", "m", "2026-01-01T00:00:01Z", []int{1, 10, 0, 0}) + "\n" +
		"this is not json\n" +
		"\n" +
		rec("assistant", "msg_2", "m", "2026-01-01T00:00:02Z", []int{1, 20, 0, 0}) + "\n" +
		`{"type":"assistant","message":{"id":"msg_3","usage":{"input_tok` // torn, no newline
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	tr, err := ParseTranscript(path)
	require.NoError(t, err)
	assert.Equal(t, 2, tr.Totals().APICalls)
	assert.Equal(t, 30, tr.Totals().OutputTokens)
}

func TestParse_FinalLineWithoutTrailingNewlineIsRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-x.jsonl")
	content := rec("assistant", "msg_1", "m", "2026-01-01T00:00:01Z", []int{1, 10, 0, 0}) + "\n" +
		rec("assistant", "msg_2", "m", "2026-01-01T00:00:02Z", []int{1, 20, 0, 0})
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	tr, err := ParseTranscript(path)
	require.NoError(t, err)
	assert.Equal(t, 30, tr.Totals().OutputTokens)
}

// A tool result can embed a whole file. bufio.Scanner's default 64KB token
// limit would abort the parse at such a line and silently lose all later spend.
func TestParse_VeryLongLineDoesNotStopParsing(t *testing.T) {
	huge, _ := json.Marshal(map[string]any{
		"type": "user", "timestamp": "2026-01-01T00:00:02Z",
		"message": map[string]any{"role": "user", "content": strings.Repeat("x", 2*1024*1024)},
	})
	path := writeTranscript(t, "agent-x.jsonl",
		rec("assistant", "msg_1", "m", "2026-01-01T00:00:01Z", []int{1, 10, 0, 0}),
		string(huge),
		rec("assistant", "msg_2", "m", "2026-01-01T00:00:03Z", []int{1, 20, 0, 0}),
	)
	tr, err := ParseTranscript(path)
	require.NoError(t, err)
	assert.Equal(t, 30, tr.Totals().OutputTokens, "the call after the 2MB line must still be counted")
}

func TestParse_EmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-empty.jsonl")
	require.NoError(t, os.WriteFile(path, nil, 0o644))
	tr, err := ParseTranscript(path)
	require.NoError(t, err)
	assert.Empty(t, tr.Calls)
	assert.Equal(t, Totals{}, tr.Totals())
	assert.Equal(t, "empty", tr.SubagentID)
	assert.Equal(t, "", tr.PrimaryModel())
}

func TestParseTranscript_MissingFileIsAnError(t *testing.T) {
	_, err := ParseTranscript(filepath.Join(t.TempDir(), "agent-nope.jsonl"))
	require.Error(t, err)
}

// ── Identity ─────────────────────────────────────────────────

func TestParse_SubagentIDPrefersRecordOverFileName(t *testing.T) {
	path := writeTranscript(t, "agent-fromfile.jsonl",
		rec("assistant", "msg_1", "m", "2026-01-01T00:00:01Z", []int{1, 1, 0, 0}))
	tr, err := ParseTranscript(path)
	require.NoError(t, err)
	assert.Equal(t, "agentX", tr.SubagentID)
}

func TestParse_SubagentIDFallsBackToFileName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-fromfile.jsonl")
	line := `{"type":"assistant","timestamp":"2026-01-01T00:00:01Z","message":{"id":"msg_1","model":"m","usage":{"input_tokens":1,"output_tokens":1}}}`
	require.NoError(t, os.WriteFile(path, []byte(line+"\n"), 0o644))
	tr, err := ParseTranscript(path)
	require.NoError(t, err)
	assert.Equal(t, "fromfile", tr.SubagentID)
	assert.Equal(t, "", tr.ParentSessionID)
}

func TestIDFromPath(t *testing.T) {
	cases := map[string]string{
		"agent-aca6223d75309e7ca.jsonl":                "aca6223d75309e7ca",
		"/a/b/subagents/agent-aca6223d75309e7ca.jsonl": "aca6223d75309e7ca",
		"agent-aca6223d75309e7ca.meta.json":            "",
		"aca6223d75309e7ca.jsonl":                      "",
		"agent-.jsonl":                                 "",
		"notes.txt":                                    "",
		"":                                             "",
	}
	for in, want := range cases {
		assert.Equal(t, want, IDFromPath(in), "IDFromPath(%q)", in)
	}
}

// ── PrimaryModel ─────────────────────────────────────────────

func TestPrimaryModel_IsTheOneWithMostOutput(t *testing.T) {
	path := writeTranscript(t, "agent-x.jsonl",
		rec("assistant", "msg_1", "claude-haiku-4-5", "2026-01-01T00:00:01Z", []int{1, 10, 0, 0}),
		rec("assistant", "msg_2", "claude-opus-5", "2026-01-01T00:00:02Z", []int{1, 500, 0, 0}),
		rec("assistant", "msg_3", "claude-haiku-4-5", "2026-01-01T00:00:03Z", []int{1, 10, 0, 0}),
	)
	tr, err := ParseTranscript(path)
	require.NoError(t, err)
	assert.Equal(t, "claude-opus-5", tr.PrimaryModel())
	assert.Equal(t, "claude-haiku-4-5", tr.Calls[0].Model, "per-call models are preserved for pricing")
}

func TestPrimaryModel_TieGoesToFirstSeen(t *testing.T) {
	path := writeTranscript(t, "agent-x.jsonl",
		rec("assistant", "msg_1", "model-a", "2026-01-01T00:00:01Z", []int{1, 10, 0, 0}),
		rec("assistant", "msg_2", "model-b", "2026-01-01T00:00:02Z", []int{1, 10, 0, 0}),
	)
	tr, err := ParseTranscript(path)
	require.NoError(t, err)
	assert.Equal(t, "model-a", tr.PrimaryModel())
}

// ── Meta ─────────────────────────────────────────────────────

func TestReadMeta_Fixture(t *testing.T) {
	m, err := ReadMeta(fixtureMeta)
	require.NoError(t, err)
	assert.Equal(t, Meta{
		AgentType:   "Explore",
		Description: "Map Coral task-board UI code",
		ToolUseID:   "toolu_016f9gxoF7HsvZXEdWAyP9cR",
		SpawnDepth:  1,
	}, m, "unknown fields such as requestShape are ignored")
}

func TestReadMeta_MissingFileIsNotAnError(t *testing.T) {
	m, err := ReadMeta(filepath.Join(t.TempDir(), "agent-x.meta.json"))
	require.NoError(t, err)
	assert.Equal(t, Meta{}, m)
}

func TestReadMeta_InvalidJSONIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-x.meta.json")
	require.NoError(t, os.WriteFile(path, []byte("{not json"), 0o644))
	_, err := ReadMeta(path)
	require.Error(t, err)
}

// ── Discovery ────────────────────────────────────────────────

func TestDir(t *testing.T) {
	assert.Equal(t,
		filepath.Join("/p", "proj", "abc-123", "subagents"),
		Dir(filepath.Join("/p", "proj", "abc-123.jsonl")))
}

func TestDiscover_Fixture(t *testing.T) {
	files, err := Discover("testdata/session-dir.jsonl")
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, fixtureID, files[0].SubagentID)
	assert.Equal(t, filepath.FromSlash(fixtureTranscript), files[0].TranscriptPath)
	assert.Equal(t, filepath.FromSlash(fixtureMeta), files[0].MetaPath)
}

func TestDiscover_FindsTranscriptsOnlyAndSorts(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "sess.jsonl")
	dir := Dir(parent)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "agent-nested.jsonl"), 0o755)) // a directory, not a file
	for _, name := range []string{
		"agent-bbb.jsonl", "agent-bbb.meta.json",
		"agent-aaa.jsonl", // no meta sidecar
		"notes.txt", "other.jsonl",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("{}\n"), 0o644))
	}

	files, err := Discover(parent)
	require.NoError(t, err)
	require.Len(t, files, 2)
	assert.Equal(t, "aaa", files[0].SubagentID)
	assert.Equal(t, "bbb", files[1].SubagentID)
	assert.Equal(t, filepath.Join(dir, "agent-aaa.meta.json"), files[0].MetaPath, "MetaPath is set even when the file is absent")
}

func TestDiscover_SessionWithoutSubagents(t *testing.T) {
	files, err := Discover(filepath.Join(t.TempDir(), "never-launched-any.jsonl"))
	require.NoError(t, err)
	assert.Empty(t, files)
}

// ── Finished ─────────────────────────────────────────────────

// recStop builds an assistant (or user) record with a stop_reason.
func recStop(typ, msgID, stop, ts string) string {
	m := map[string]any{"role": typ, "id": msgID, "model": "m",
		"usage": map[string]int{"input_tokens": 1, "output_tokens": 1}}
	if stop != "" {
		m["stop_reason"] = stop
	} else {
		m["stop_reason"] = nil
	}
	b, _ := json.Marshal(map[string]any{"type": typ, "timestamp": ts, "agentId": "agentX", "message": m})
	return string(b)
}

func TestFinished_FixtureEndsItsTurn(t *testing.T) {
	tr, err := ParseTranscript(fixtureTranscript)
	require.NoError(t, err)
	assert.True(t, tr.Finished)
}

func TestFinished(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		want  bool
	}{
		{"final answer", []string{recStop("assistant", "m1", "end_turn", "t1")}, true},
		{"max_tokens also ends the run", []string{recStop("assistant", "m1", "max_tokens", "t1")}, true},
		{"waiting on a tool", []string{recStop("assistant", "m1", "tool_use", "t1")}, false},
		{"tool result came back, model has not replied", []string{
			recStop("assistant", "m1", "tool_use", "t1"), recStop("user", "", "", "t2")}, false},
		{"message still streaming", []string{recStop("assistant", "m1", "", "t1")}, false},
		{"streamed blocks then the closing one", []string{
			recStop("assistant", "m1", "", "t1"), recStop("assistant", "m1", "", "t2"),
			recStop("assistant", "m1", "end_turn", "t3")}, true},
		{"only the launch prompt so far", []string{recStop("user", "", "", "t1")}, false},
		{"finished, then resumed with a new prompt", []string{
			recStop("assistant", "m1", "end_turn", "t1"), recStop("user", "", "", "t2")}, false},
		{"resumed and finished again", []string{
			recStop("assistant", "m1", "end_turn", "t1"), recStop("user", "", "", "t2"),
			recStop("assistant", "m2", "end_turn", "t3")}, true},
		{"bookkeeping record after the final answer does not reopen it", []string{
			recStop("assistant", "m1", "end_turn", "t1"), rec("system", "", "", "t2", nil)}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr, err := ParseTranscript(writeTranscript(t, "agent-x.jsonl", c.lines...))
			require.NoError(t, err)
			assert.Equal(t, c.want, tr.Finished)
		})
	}
}

func TestFinished_EmptyTranscriptIsNotFinished(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-x.jsonl")
	require.NoError(t, os.WriteFile(path, nil, 0o644))
	tr, err := ParseTranscript(path)
	require.NoError(t, err)
	assert.False(t, tr.Finished)
}

// ── ReadConversation ─────────────────────────────────────────

// convRec builds a record whose message.content is `content` (a string or a
// list of blocks).
func convRec(typ, msgID, stop string, content any) string {
	m := map[string]any{"role": typ, "content": content}
	if msgID != "" {
		m["id"] = msgID
	}
	if stop != "" {
		m["stop_reason"] = stop
	}
	b, _ := json.Marshal(map[string]any{"type": typ, "message": m})
	return string(b)
}

func textBlock(s string) map[string]any { return map[string]any{"type": "text", "text": s} }

func TestReadConversation_Fixture(t *testing.T) {
	conv, err := ReadConversation(fixtureTranscript)
	require.NoError(t, err)
	assert.Equal(t, "Map the task-board UI code.", conv.Prompt)
	assert.Equal(t, "Found it in tasks.js.", conv.Result)
	assert.False(t, conv.Truncated)
}

func TestReadConversation_ResultIsOnlyTheFinalAnswer(t *testing.T) {
	path := writeTranscript(t, "agent-x.jsonl",
		convRec("user", "", "", "Find the bug."),
		convRec("assistant", "m1", "", []any{map[string]any{"type": "thinking", "thinking": "hmm"}}),
		convRec("assistant", "m1", "", []any{textBlock("Let me look around first.")}),
		convRec("assistant", "m1", "tool_use", []any{map[string]any{"type": "tool_use", "name": "Grep"}}),
		convRec("user", "", "", []any{map[string]any{"type": "tool_result", "content": "x.go:1"}}),
		convRec("assistant", "m2", "", []any{textBlock("The bug is in x.go.")}),
		convRec("assistant", "m2", "end_turn", []any{textBlock("Fix: check for nil.")}),
	)
	conv, err := ReadConversation(path)
	require.NoError(t, err)
	assert.Equal(t, "Find the bug.", conv.Prompt, "the tool result is a user record too, but not the prompt")
	assert.Equal(t, "The bug is in x.go.\n\nFix: check for nil.", conv.Result,
		"all text blocks of the final message, and none of the mid-run commentary, thinking or tool calls")
}

func TestReadConversation_NoResultWhileStillWorking(t *testing.T) {
	cases := map[string][]string{
		"waiting on a tool": {
			convRec("user", "", "", "Do it."),
			convRec("assistant", "m1", "tool_use", []any{textBlock("Checking...")}),
		},
		"tool result returned, no reply yet": {
			convRec("user", "", "", "Do it."),
			convRec("assistant", "m1", "tool_use", []any{textBlock("Checking...")}),
			convRec("user", "", "", []any{map[string]any{"type": "tool_result", "content": "ok"}}),
		},
		"final message still streaming": {
			convRec("user", "", "", "Do it."),
			convRec("assistant", "m1", "", []any{textBlock("The answer is")}),
		},
		"finished once, then resumed": {
			convRec("user", "", "", "Do it."),
			convRec("assistant", "m1", "end_turn", []any{textBlock("Done.")}),
			convRec("user", "", "", "Actually, one more thing."),
		},
	}
	for name, lines := range cases {
		t.Run(name, func(t *testing.T) {
			conv, err := ReadConversation(writeTranscript(t, "agent-x.jsonl", lines...))
			require.NoError(t, err)
			assert.Equal(t, "Do it.", conv.Prompt, "prompt stays the FIRST user message")
			assert.Empty(t, conv.Result)
		})
	}
}

func TestReadConversation_PromptAsContentBlocks(t *testing.T) {
	path := writeTranscript(t, "agent-x.jsonl",
		convRec("user", "", "", []any{textBlock("Part one."), textBlock("Part two.")}),
		convRec("assistant", "m1", "end_turn", "A plain string answer."),
	)
	conv, err := ReadConversation(path)
	require.NoError(t, err)
	assert.Equal(t, "Part one.\n\nPart two.", conv.Prompt)
	assert.Equal(t, "A plain string answer.", conv.Result)
}

func TestReadConversation_CapsHugeTextOnARuneBoundary(t *testing.T) {
	huge := strings.Repeat("é", maxConversationText) // 2 bytes each: twice the cap
	path := writeTranscript(t, "agent-x.jsonl",
		convRec("user", "", "", "Short prompt."),
		convRec("assistant", "m1", "end_turn", []any{textBlock(huge)}),
	)
	conv, err := ReadConversation(path)
	require.NoError(t, err)
	assert.True(t, conv.Truncated)
	assert.LessOrEqual(t, len(conv.Result), maxConversationText)
	assert.True(t, strings.HasSuffix(conv.Result, "é"), "must not split a multi-byte character")
	assert.Equal(t, "Short prompt.", conv.Prompt)
}

func TestReadConversation_EmptyAndMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-x.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("not json\n"), 0o644))
	conv, err := ReadConversation(path)
	require.NoError(t, err)
	assert.Equal(t, Conversation{}, *conv)

	_, err = ReadConversation(filepath.Join(t.TempDir(), "agent-gone.jsonl"))
	require.ErrorIs(t, err, os.ErrNotExist)
}
