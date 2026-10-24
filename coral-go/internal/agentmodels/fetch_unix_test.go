//go:build unix

package agentmodels

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/claudestub"
)

// script writes an executable shell script standing for a CLI. It answers the sign-in status command ("claude auth status", #5469) as signed in, so the body is only
// what the test is about.
func script(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cli")
	require.NoError(t, os.WriteFile(p, []byte("#!/bin/sh\nif [ \"$1\" = auth ]; then echo '{\"loggedIn\":true}'; exit 0; fi\n"+body), 0o755))
	return p
}

// A signed-out Claude is not started for its model list (#5469): the error is the sentence, and only the status command ran.
func TestFetchClaudeDoesNotStartASignedOutClaude(t *testing.T) {
	claudestub.Isolate(t)
	patientCheck(t) // a busy machine starts the script slowly: a status check past the production 6 s is "did not answer", not what this test is about (#7163)
	ran := filepath.Join(t.TempDir(), "ran")
	p := filepath.Join(t.TempDir(), "cli")
	require.NoError(t, os.WriteFile(p, []byte("#!/bin/sh\nif [ \"$1\" = auth ]; then echo '{\"loggedIn\":false}'; exit 1; fi\necho started > '"+ran+"'\n"), 0o755))
	_, _, err := FetchFromCLI(context.Background(), "claude", p)
	require.Error(t, err)
	assert.Equal(t, "Claude is not signed in", err.Error())
	_, statErr := os.Stat(ran)
	assert.True(t, os.IsNotExist(statErr), "the CLI was never started for the list")
}

// Claude's list comes from its initialize reply: the fake CLI checks it was asked exactly the
// initialize control request on stdin (no prompt), then answers with the recorded reply.
// Coral's own variables are not in the CLI's environment, and it runs in a scratch folder.
func TestFetchClaudeSendsOnlyInitialize(t *testing.T) {
	reply, err := filepath.Abs("testdata/claude-2.1.301-initialize.jsonl")
	require.NoError(t, err)
	seen := filepath.Join(t.TempDir(), "seen")
	t.Setenv("CORAL_SESSION_TOKEN", "must-not-pass")
	cli := script(t, `read line
printf '%s\n' "$line" > '`+seen+`'
env | grep -c '^CORAL_' >> '`+seen+`'
pwd >> '`+seen+`'
cat '`+reply+`'
sleep 30
`)
	models, source, err := FetchFromCLI(context.Background(), "claude", cli)
	require.NoError(t, err)
	assert.Equal(t, "account", source)
	assert.Equal(t, "opus", models[1].Value)
	b, _ := os.ReadFile(seen)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	require.Len(t, lines, 3)
	assert.Equal(t, `{"type":"control_request","request_id":"coral-models","request":{"subtype":"initialize"}}`, lines[0])
	assert.Equal(t, "0", lines[1], "no CORAL_ variable reaches the CLI")
	assert.Contains(t, lines[2], "coral-models-", "a scratch working folder")
	_, statErr := os.Stat(lines[2])
	assert.True(t, os.IsNotExist(statErr), "removed afterwards")
}

// Codex: the account's catalog, else (offline, signed out) the bundled one.
func TestFetchCodexFallsBackToTheBundledCatalog(t *testing.T) {
	cat, err := filepath.Abs("testdata/codex-0.155.1-debug-models.json")
	require.NoError(t, err)
	cli := script(t, `[ "$1 $2" = "debug models" ] || exit 9
[ "$3" = "--bundled" ] && { cat '`+cat+`'; exit 0; }
exit 1
`)
	models, source, err := FetchFromCLI(context.Background(), "codex", cli)
	require.NoError(t, err)
	assert.Equal(t, "bundled", source)
	assert.NotEmpty(t, models)
}

// A CLI that never answers is stopped at the deadline.
func TestFetchTimesOut(t *testing.T) {
	cli := script(t, "sleep 30\n")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err := FetchFromCLI(ctx, "agy", cli)
	require.Error(t, err)
	assert.Equal(t, "the CLI didn't answer in time", describe(err))
	assert.Less(t, time.Since(start), 5*time.Second)
}
