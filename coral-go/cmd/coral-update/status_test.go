package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/linktest"
	"github.com/subgentic/coral/internal/updater/runrecord"
	"github.com/subgentic/coral/internal/updater/verify"
)

func statusRun(t *testing.T, env map[string]string, args ...string) (int, string) {
	t.Helper()
	var buf bytes.Buffer
	code := run(args, func(k string) string { return env[k] }, &buf)
	return code, buf.String()
}

func statusJSON(t *testing.T, env map[string]string) map[string]any {
	t.Helper()
	code, out := statusRun(t, env, "status", "--json")
	require.Equal(t, 0, code)
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &m), out)
	return m
}

func withVersion(t *testing.T, v string) string {
	t.Helper()
	old := config.Version
	t.Cleanup(func() { config.Version = old })
	config.Version = v
	dir := t.TempDir()
	t.Setenv("CORAL_DATA_DIR", dir)
	return dir
}

func TestStatusArgvIsFixedToo(t *testing.T) {
	withVersion(t, "1.4.130")
	for _, args := range [][]string{{"status", "extra"}, {"status", "--url=http://x"}, {"--json", "status"}, {"status", "--json", "x"}, {"STATUS"}, {"status", ""}} {
		code, out := statusRun(t, nil, args...)
		assert.Equal(t, 2, code, "%v", args)
		assert.Empty(t, out, "%v", args)
	}
	for _, args := range [][]string{{"status"}, {"status", "--json"}} {
		code, out := statusRun(t, nil, args...)
		assert.Equal(t, 0, code, "%v", args)
		assert.NotEmpty(t, out)
	}
}

// A data folder where the updater never ran: it says so, in words and as JSON, and reads only: nothing is created (no updates folder, no trusted-root cache).
func TestStatusOfAFolderTheUpdaterNeverUsedWritesNothing(t *testing.T) {
	dir := withVersion(t, "1.4.130")
	code, out := statusRun(t, nil, "status")
	assert.Equal(t, 0, code)
	assert.Contains(t, out, "has not run yet")
	assert.Contains(t, out, "Installed version: 1.4.130")
	assert.Contains(t, out, "Last check: unknown")
	assert.Contains(t, out, "Last install attempt: none")
	assert.Contains(t, out, "not there")
	m := statusJSON(t, nil)
	assert.Equal(t, "missing", m["state_file"])
	assert.Equal(t, "missing", m["run_record"])
	assert.Equal(t, "1.4.130", m["installed"])
	assert.NotContains(t, m, "staged")
	assert.NotContains(t, m, "last_apply")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "status wrote nothing")
}

func writeState(t *testing.T, dir string, st verify.State) {
	t.Helper()
	require.NoError(t, verify.SaveState(filepath.Join(dir, "updates"), st))
}

func TestStatusSaysWhatTheUpdaterKnows(t *testing.T) {
	dir := withVersion(t, "1.4.130")
	future := time.Now().Add(40 * time.Minute).UTC().Format(time.RFC3339)
	writeState(t, dir, verify.State{
		Version: "1.4.129", Sequence: 7, Revoked: []string{"1.4.100", "1.4.101"},
		LastCheck:   "2026-10-20T11:00:00Z",
		LastFailure: &verify.FailureRecord{Version: "1.4.131", Stage: "sums_signature", Reason: "the release was not signed by the pinned identity", At: "2026-10-20T11:00:00Z"},
		Stage:       &verify.StageState{Staged: "1.4.131", StagedAt: "2026-10-20T10:00:00Z", Version: "1.4.132", Attempts: 2, NextAttempt: future, ErrorKind: "network"},
		History: []verify.HistoryEntry{
			{At: "2026-10-19T09:00:00Z", From: "1.4.128", To: "1.4.129", By: "local", Result: "relaunched"},
			{At: "2026-10-20T08:00:00Z", From: "1.4.129", To: "1.4.130", By: "local", Result: "failed_rolled_back", Reason: "not_healthy"},
			{At: "2026-10-20T09:00:00Z", From: "1.4.129", To: "1.4.131", By: "hub:hub-user-1234abcd", Result: "refused", Reason: "sums_signature"},
		},
	})
	require.NoError(t, os.WriteFile(filepath.Join(dir, "updates", runrecord.File), []byte(`{"argv":["/opt/coral/coral","--port","8420"],"cwd":"/srv"}`), 0o600))

	m := statusJSON(t, nil)
	wantFeature := "on"
	if !config.TierChecksForUpdates { // a dev, beta or dropboxers build says it is off, and still says everything else
		wantFeature = "off"
	}
	assert.Equal(t, wantFeature, m["feature"])
	assert.Equal(t, false, m["apply_enabled"])
	assert.Equal(t, "present", m["state_file"])
	assert.Equal(t, "present", m["run_record"])
	assert.Equal(t, map[string]any{"version": "1.4.129", "sequence": float64(7)}, m["accepted"])
	assert.EqualValues(t, 2, m["revoked"])
	assert.Equal(t, map[string]any{"version": "1.4.131", "staged_at": "2026-10-20T10:00:00Z"}, m["staged"])
	assert.Equal(t, "2026-10-20T11:00:00Z", m["last_check"])
	assert.Equal(t, map[string]any{"version": "1.4.131", "stage": "sums_signature", "at": "2026-10-20T11:00:00Z"}, m["last_failure"], "the JSON has the stage word, not the stored text")
	assert.Equal(t, map[string]any{"version": "1.4.132", "until": future, "waiting": true, "attempts": float64(2), "kind": "network"}, m["backoff"])
	assert.Equal(t, map[string]any{"at": "2026-10-20T09:00:00Z", "from": "1.4.129", "to": "1.4.131", "by": "hub:hub-user-1234abcd", "result": "refused", "reason": "sums_signature"}, m["last_apply"])
	assert.Equal(t, map[string]any{"at": "2026-10-20T08:00:00Z", "from": "1.4.129", "to": "1.4.130", "by": "local", "result": "failed_rolled_back", "reason": "not_healthy"}, m["last_rollback"])
	b, _ := json.Marshal(m)
	for _, bad := range []string{"/opt/coral", "8420", "/srv"} {
		assert.NotContains(t, string(b), bad, "the run record's content is not shown, only that it is there")
	}

	_, out := statusRun(t, nil, "status")
	for _, want := range []string{"Automatic updates: " + wantFeature, "not enabled yet", "Installed version: 1.4.130", "accepted: 1.4.129 (promote sequence 7)", "Versions revoked: 2",
		"Ready to install: 1.4.131", "Last check: 2026-10-20T11:00:00Z", "Last refusal: version 1.4.131 stopped at sums_signature", "the release was not signed by the pinned identity", "Waiting before trying version 1.4.132 again until " + future,
		"Last install attempt: 1.4.129 -> 1.4.131, refused", "Last roll-back: version 1.4.130 did not come up", "stopped at: not_healthy", ": present"} {
		assert.Contains(t, out, want)
	}
}

func TestStatusBackoffThatEndedIsSaidToHaveEnded(t *testing.T) {
	dir := withVersion(t, "1.4.130")
	writeState(t, dir, verify.State{Stage: &verify.StageState{Version: "1.4.131", Attempts: 1, NextAttempt: "2026-01-01T00:00:00Z", ErrorKind: "verify"}})
	m := statusJSON(t, nil)
	assert.Equal(t, false, m["backoff"].(map[string]any)["waiting"])
	_, out := statusRun(t, nil, "status")
	assert.Contains(t, out, "The wait before trying version 1.4.131 again ended at 2026-01-01T00:00:00Z")
}

// It answers with the feature off, and says why, for each way of being off.
func TestStatusAnswersWhenTheFeatureIsOff(t *testing.T) {
	withVersion(t, "1.4.130")
	// The build's tier is checked first: a dev, beta or dropboxers build is off for that reason whatever else is set.
	reason := func(other string) string {
		if !config.TierChecksForUpdates {
			return "build_tier"
		}
		return other
	}
	m := statusJSON(t, map[string]string{"CORAL_AUTO_UPDATE": "off"})
	assert.Equal(t, "off", m["feature"])
	assert.Equal(t, reason("env_switch"), m["off_reason"])
	_, out := statusRun(t, map[string]string{"CORAL_AUTO_UPDATE": "off"}, "status")
	assert.Contains(t, out, "Automatic updates: off (")
	if config.TierChecksForUpdates {
		assert.Contains(t, out, "CORAL_AUTO_UPDATE=off")
	}

	config.Version = ""
	m = statusJSON(t, nil)
	assert.Equal(t, "off", m["feature"])
	assert.Equal(t, reason("no_version"), m["off_reason"])
	assert.NotContains(t, m, "installed")
}

func TestStatusOfAnUnreadableRecordClaimsNothing(t *testing.T) {
	dir := withVersion(t, "1.4.130")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "updates"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "updates", "state.json"), []byte("{ not json"), 0o600))
	m := statusJSON(t, nil)
	assert.Equal(t, "unreadable", m["state_file"])
	assert.NotContains(t, m, "accepted")
	assert.NotContains(t, m, "last_apply")
	_, out := statusRun(t, nil, "status")
	assert.Contains(t, out, "cannot be read")

	// a run record that is not a regular file (a folder, a link) is unreadable, never followed
	require.NoError(t, os.Mkdir(filepath.Join(dir, "updates", "run.json"), 0o700))
	assert.Equal(t, "unreadable", statusJSON(t, nil)["run_record"])
	require.NoError(t, os.Remove(filepath.Join(dir, "updates", "run.json")))
	linktest.Symlink(t, filepath.Join(dir, "elsewhere"), filepath.Join(dir, "updates", "run.json"))
	assert.Equal(t, "unreadable", statusJSON(t, nil)["run_record"])
}

// What the state holds is shown as bounded printable text: no escape sequence, no line break, no long text, no time that is not a time.
func TestStatusShowsTextFromTheStateOnlyAsBoundedPrintableText(t *testing.T) {
	dir := withVersion(t, "1.4.130")
	writeState(t, dir, verify.State{
		LastCheck:   "yesterday",
		LastFailure: &verify.FailureRecord{Version: "1.4.131", Stage: "x\x1b[31mred\x1b[0m", Reason: "line one\nline two\u202e" + strings.Repeat("a", 500), At: "not a time"},
	})
	m := statusJSON(t, nil)
	assert.NotContains(t, m, "last_check", "a time that is not RFC 3339 is not shown")
	f := m["last_failure"].(map[string]any)
	assert.Equal(t, "other", f["stage"], "a stage that is not a fixed word is shown as other")
	assert.NotContains(t, f, "reason")
	assert.NotContains(t, f, "at")
	_, out := statusRun(t, nil, "status")
	assert.NotContains(t, out, "\x1b")
	assert.Equal(t, 1, strings.Count(out, "Last refusal:"), "one line")
}

// It makes no request: with the network refused outright it still answers at once (the trusted root is not loaded, nothing is fetched).
func TestStatusDoesNotTouchTheNetwork(t *testing.T) {
	dir := withVersion(t, "1.4.130")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	start := time.Now()
	statusRun(t, nil, "status")
	statusRun(t, nil, "status", "--json")
	assert.Less(t, time.Since(start), 2*time.Second)
	_, err := os.Stat(filepath.Join(dir, "updates", "tuf"))
	assert.ErrorIs(t, err, os.ErrNotExist, "no trusted-root cache was made")
}

// #5419: a roll-back whose restart failed is a roll-back too, and says Coral was not running afterwards.
func TestStatusShowsARollbackWhoseRestartFailed(t *testing.T) {
	dir := withVersion(t, "1.4.130")
	writeState(t, dir, verify.State{History: []verify.HistoryEntry{
		{At: "2026-10-20T08:00:00Z", From: "1.4.129", To: "1.4.130", By: "local", Result: "failed_rolled_back", Reason: "not_healthy"},
		{At: "2026-10-20T09:00:00Z", From: "1.4.129", To: "1.4.131", By: "local", Result: "restart_failed", Reason: "not_healthy"},
	}})
	m := statusJSON(t, nil)
	assert.Equal(t, "restart_failed", m["last_rollback"].(map[string]any)["result"])
	assert.Equal(t, "restart_failed", m["last_apply"].(map[string]any)["result"])
	_, out := statusRun(t, nil, "status")
	assert.Contains(t, out, "Coral could NOT be started again")
	assert.Contains(t, out, "version 1.4.131 did not come up")
}

// #5424: the stored text of a refusal (a Go error: a home path, a hash, a URL) never reaches the JSON; the plain text, for the person's own terminal, may show it.
func TestStatusJSONNeverCarriesTheStoredErrorText(t *testing.T) {
	dir := withVersion(t, "1.4.130")
	hash := strings.Repeat("ab", 32)
	text := "open /home/someone/.coral/updates/staging/x: permission denied; sha256 " + hash + " from https://github.com/cdknorow/coral/releases/download/v1.4.131/SHA256SUMS.txt"
	writeState(t, dir, verify.State{
		LastFailure: &verify.FailureRecord{Version: "1.4.131", Stage: "package", Reason: text, At: "2026-10-20T11:00:00Z"},
		History: []verify.HistoryEntry{
			{At: "2026-10-20T11:00:00Z", From: "1.4.130", To: "1.4.131", By: "local", Result: "failed_rolled_back", Reason: "/home/someone/secret"},
			{At: "2026-10-20T12:00:00Z", From: "1.4.130", To: "1.4.131", By: "local", Result: "https://evil.example/x", Reason: "not_healthy"},
		},
		Stage: &verify.StageState{Version: "1.4.131", Attempts: 1, NextAttempt: "2999-01-01T00:00:00Z", ErrorKind: "C:\\Users\\x"},
	})
	_, js := statusRun(t, nil, "status", "--json")
	for _, bad := range []string{"/home/", "someone", hash, "https://", "github.com", "evil.example", "permission denied", "C:", "Users"} {
		assert.NotContains(t, js, bad, "the JSON holds words, versions and times only")
	}
	m := statusJSON(t, nil)
	assert.Equal(t, "package", m["last_failure"].(map[string]any)["stage"])
	assert.NotContains(t, m["last_failure"], "reason")
	assert.Equal(t, "other", m["last_rollback"].(map[string]any)["reason"])
	assert.Equal(t, "other", m["last_apply"].(map[string]any)["result"])
	assert.Equal(t, "other", m["backoff"].(map[string]any)["kind"])
	_, plain := statusRun(t, nil, "status")
	assert.Contains(t, plain, "permission denied", "the plain text keeps the detail for the person at their terminal")
	assert.Equal(t, 1, strings.Count(plain, "Last refusal:"))
}

// #5428: a version field is a version or "unknown", and who started an attempt is "local", a hub pseudonym or "other": a hand-edited state puts no text into the JSON.
func TestStatusJSONShowsOnlyVersionsInVersionFields(t *testing.T) {
	dir := withVersion(t, "1.4.130")
	evil := "/home/someone/.ssh/id_rsa"
	writeState(t, dir, verify.State{
		LastFailure: &verify.FailureRecord{Version: evil, Stage: "package", At: "2026-10-20T11:00:00Z"},
		Stage:       &verify.StageState{Staged: "https://evil.example/x", StagedAt: "2026-10-20T10:00:00Z", Version: "1.4.131-rc1", Attempts: 1, NextAttempt: "2999-01-01T00:00:00Z"},
		History: []verify.HistoryEntry{
			{At: "2026-10-20T08:00:00Z", From: "1.4.129", To: evil, By: "hub:someone@example.invalid", Result: "failed_rolled_back", Reason: "not_healthy"},
			{At: "2026-10-20T09:00:00Z", From: evil, To: "1.4.131", By: evil, Result: "refused", Reason: "package"},
			{At: "2026-10-20T10:00:00Z", From: "1.4.129", To: "1.4.131", By: "hub:hub-user-1234abcd", Result: "relaunched"},
		},
	})
	_, js := statusRun(t, nil, "status", "--json")
	for _, bad := range []string{"/home/", "someone", "id_rsa", "https://", "evil", "rc1", "@"} {
		assert.NotContains(t, js, bad)
	}
	m := statusJSON(t, nil)
	assert.Equal(t, "unknown", m["last_failure"].(map[string]any)["version"])
	assert.Equal(t, "unknown", m["staged"].(map[string]any)["version"])
	assert.Equal(t, "unknown", m["backoff"].(map[string]any)["version"])
	rb := m["last_rollback"].(map[string]any)
	assert.Equal(t, "1.4.129", rb["from"], "a real version is kept")
	assert.Equal(t, "unknown", rb["to"])
	assert.Equal(t, "other", rb["by"])
	la := m["last_apply"].(map[string]any)
	assert.Equal(t, "hub:hub-user-1234abcd", la["by"], "a hub pseudonym is kept")
	assert.Equal(t, "1.4.131", la["to"])
	_, plain := statusRun(t, nil, "status")
	assert.NotContains(t, plain, "id_rsa", "the plain text shows the same fields")
	assert.NotContains(t, plain, "evil")
}
