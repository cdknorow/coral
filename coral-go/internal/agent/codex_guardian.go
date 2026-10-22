package agent

import (
	"bufio"
	"encoding/json"
	"io"
)

// IsCodexGuardianMetadata identifies Codex's automatic approval-review thread,
// never a real chat or another subagent. An explicit thread_source takes priority
// over the older source marker (Codex 0.163 writes both).
func IsCodexGuardianMetadata(entry map[string]any) bool {
	if entry["type"] != "session_meta" {
		return false
	}
	payload, _ := entry["payload"].(map[string]any)
	if source, present := payload["thread_source"]; present {
		return source == "guardian_review"
	}
	source, _ := payload["source"].(map[string]any)
	subagent, _ := source["subagent"].(map[string]any)
	return subagent["other"] == "guardian"
}

// IsCodexGuardianRollout checks the initial metadata without consuming the file.
// The bound matches the history scanner's maximum record size. Neither the
// rollout nor the terminal output is changed by this display/index filter.
func IsCodexGuardianRollout(f io.ReadSeeker) bool {
	pos, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return false
	}
	defer f.Seek(pos, io.SeekStart)
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return false
	}
	const limit = 1 << 20
	line, _ := bufio.NewReader(io.LimitReader(f, limit+1)).ReadBytes('\n')
	if len(line) > limit {
		return false
	}
	var entry map[string]any
	return json.Unmarshal(line, &entry) == nil && IsCodexGuardianMetadata(entry)
}
