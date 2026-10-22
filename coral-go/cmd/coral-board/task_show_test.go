package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The server's task (GET /tasks/<id>) as task detail prints it: a completed task with a linked and an inline artifact, and a
// terminal control in its message.
const taskShowReply = `{"id":279,"title":"Copy audit","status":"completed","priority":"medium","assigned_to":"Lead Developer",
"created_by":"Orchestrator","completed_by":"Lead Developer","body":"Audit the messages.","completion_message":"Posted\u001b[2J the table.",
"workflow":{"outcome":"success","artifacts":[{"name":"copy-audit-279.md","uri":"coral://artifacts/853f69e9","media_type":"text/markdown"},
{"name":"notes","content":"secret-looking inline text","landed":true}]}}`

// #285: task show prints a task's result for a person: status and outcome, who, the body, the result message and each artifact's
// link; an inline artifact by its size only (its content stays in task detail); no terminal control from the server's text.
func TestTaskShowForAPerson(t *testing.T) {
	ts, err := parseTaskShow([]byte(taskShowReply))
	require.NoError(t, err)
	var out bytes.Buffer
	printTaskShow(&out, ts)
	got := out.String()
	for _, want := range []string{
		"Task #279: Copy audit\n",
		"Status: completed (success)  Priority: medium\n",
		"Assigned to: Lead Developer  Created by: Orchestrator  Completed by: Lead Developer\n",
		"\nAudit the messages.\n",
		"\nResult: Posted the table.\n",
		"\nArtifacts (2):\n",
		"  copy-audit-279.md  coral://artifacts/853f69e9  [text/markdown]\n",
		"  notes  (inline, 26 bytes; task detail 279 shows it)  [landed]\n",
	} {
		assert.Contains(t, got, want)
	}
	assert.NotContains(t, got, "\x1b", "no terminal control from the server")
	assert.NotContains(t, got, "secret-looking", "an inline artifact's content is not printed")
}

// --json is the same fields as one object; artifacts is [] when there are none.
func TestTaskShowJSON(t *testing.T) {
	ts, err := parseTaskShow([]byte(taskShowReply))
	require.NoError(t, err)
	raw, err := json.Marshal(ts)
	require.NoError(t, err)
	var back map[string]any
	require.NoError(t, json.Unmarshal(raw, &back))
	assert.Equal(t, "success", back["outcome"])
	assert.Equal(t, "Posted the table.", back["message"])
	arts := back["artifacts"].([]any)
	require.Len(t, arts, 2)
	assert.Equal(t, "coral://artifacts/853f69e9", arts[0].(map[string]any)["uri"])
	assert.Nil(t, arts[1].(map[string]any)["content"], "no inline content in the JSON either")
	assert.EqualValues(t, 26, arts[1].(map[string]any)["inline_bytes"])

	empty, err := parseTaskShow([]byte(`{"id":1,"title":"t","status":"pending"}`))
	require.NoError(t, err)
	raw, _ = json.Marshal(empty)
	assert.True(t, strings.Contains(string(raw), `"artifacts":[]`), string(raw))
	var out bytes.Buffer
	printTaskShow(&out, empty)
	assert.Contains(t, out.String(), "Artifacts: none")
}
