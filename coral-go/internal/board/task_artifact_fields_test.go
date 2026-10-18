package board

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4901: every field of a task's artifact is plain text of a bounded size: the Team Artifacts panel, the Inbox and a cross-team message
// show what the writer sent, and a message is typed into agents' terminals. A control character (an escape, a C1 code, a line
// separator), a direction override, a zero-width mark, invalid UTF-8 or an over-long value is refused naming the field.
func TestTaskArtifactFieldsArePlainBoundedText(t *testing.T) {
	good := func() TaskArtifact {
		return TaskArtifact{Name: "report 🙂.md", URI: "https://example.com/r", MediaType: "text/markdown; charset=utf-8", Revision: "abc123", Digest: "sha256:0123abcd",
			Kind: "test", Command: "go test\t./...", Runner: "ci-1", OutputDigest: "sha256:ffee", StartedAt: "2026-10-18T10:00:00Z", FinishedAt: "2026-10-18T10:01:00Z",
			Branch: "feat/x", Remote: "origin"}
	}
	_, err := validateTaskArtifacts([]TaskArtifact{good()})
	require.NoError(t, err, "a plain artifact, with an emoji, a tab in the command and a charset in the media type")

	bads := map[string]string{"escape": "a\x1b[31mred", "C1": "a\u0085b", "line separator": "a\u2028b", "paragraph separator": "a\u2029b",
		"direction override": "a\u202eb", "zero-width space": "a\u200bb", "newline": "a\nb", "invalid UTF-8": "a\xffb", "NUL": "a\x00b", "DEL": "a\x7fb"}
	set := map[string]func(*TaskArtifact, string){
		"name": func(a *TaskArtifact, v string) { a.Name = v }, "media_type": func(a *TaskArtifact, v string) { a.MediaType = v },
		"revision": func(a *TaskArtifact, v string) { a.Revision = v }, "digest": func(a *TaskArtifact, v string) { a.Digest = v },
		"kind": func(a *TaskArtifact, v string) { a.Kind = v }, "runner": func(a *TaskArtifact, v string) { a.Runner = v },
		"output_digest": func(a *TaskArtifact, v string) { a.OutputDigest = v }, "started_at": func(a *TaskArtifact, v string) { a.StartedAt = v },
		"finished_at": func(a *TaskArtifact, v string) { a.FinishedAt = v }, "branch": func(a *TaskArtifact, v string) { a.Branch = v },
		"remote": func(a *TaskArtifact, v string) { a.Remote = v },
	}
	for field, assign := range set {
		for why, v := range bads {
			a := good()
			assign(&a, v)
			_, err := validateTaskArtifacts([]TaskArtifact{a})
			require.Error(t, err, "%s with %s", field, why)
			if field != "name" { // a name with a bad character is named by the field; the name itself is the artifact's label
				assert.Contains(t, err.Error(), field, "%s with %s: %v", field, why, err)
			}
		}
	}
	for _, why := range []string{"newline", "escape", "C1", "line separator", "invalid UTF-8"} {
		a := good()
		a.Command = "go test" + bads[why]
		_, err := validateTaskArtifacts([]TaskArtifact{a})
		require.Error(t, err, "command with %s", why)
		assert.Contains(t, err.Error(), "command")
	}
	// sizes: the two that had none (media type, revision, digest) and the rest at their limits
	for field, limit := range map[string]int{"media_type": 127, "revision": 256, "digest": 256} {
		a := good()
		set[field](&a, strings.Repeat("x", limit))
		_, err := validateTaskArtifacts([]TaskArtifact{a})
		require.NoError(t, err, "%s at its limit", field)
		set[field](&a, strings.Repeat("x", limit+1))
		_, err = validateTaskArtifacts([]TaskArtifact{a})
		require.Error(t, err, "%s over its limit", field)
	}
}
