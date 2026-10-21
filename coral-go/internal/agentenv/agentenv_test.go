package agentenv

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// #3389: what an agent may inherit from Coral's own environment.
func TestFilter(t *testing.T) {
	in := []string{
		"PATH=/usr/bin", "HOME=/home/u", "ANTHROPIC_API_KEY=sk-x", "TERM=xterm",
		"CORAL_SECRET_KEY=master", "CORAL_CLOUD_URL=https://app.coralhub.dev", "CORAL_TLS_KEY=/k.pem",
		"CORAL_DEBUG_INPUT=1", "CORAL_TRUSTED_PROXIES=10.0.0.0/8", "CORAL_WEBHOOK_ALLOW_PRIVATE=1",
		"CORAL_SOME_FUTURE_SETTING=x",
		"CORAL_URL=http://127.0.0.1:8420", "CORAL_PORT=8420", "CORAL_DATA_DIR=/d", "CORAL_SESSION_NAME=a",
		"CORAL_SUBSCRIBER_ID=Lead", "CORAL_WORKFLOW_RUN_ID=7", "CORAL_PREV_STDOUT=/tmp/o", "CORAL_STEP_STATUS=ok",
		"MALFORMED",
	}
	got := Filter(in)
	joined := strings.Join(got, "\n")
	for _, gone := range []string{"CORAL_SECRET_KEY", "CORAL_CLOUD_URL", "CORAL_TLS_KEY", "CORAL_DEBUG_INPUT",
		"CORAL_TRUSTED_PROXIES", "CORAL_WEBHOOK_ALLOW_PRIVATE", "CORAL_SOME_FUTURE_SETTING"} {
		assert.NotContains(t, joined, gone+"=", "a server-only variable is removed: "+gone)
	}
	for _, kept := range []string{"PATH=/usr/bin", "HOME=/home/u", "ANTHROPIC_API_KEY=sk-x", "TERM=xterm",
		"CORAL_URL=http://127.0.0.1:8420", "CORAL_PORT=8420", "CORAL_DATA_DIR=/d", "CORAL_SESSION_NAME=a",
		"CORAL_SUBSCRIBER_ID=Lead", "CORAL_WORKFLOW_RUN_ID=7", "CORAL_PREV_STDOUT=/tmp/o", "CORAL_STEP_STATUS=ok", "MALFORMED"} {
		assert.Contains(t, got, kept)
	}
	assert.Len(t, in, 20, "the input is not changed")
}

// Every server setting Coral reads today is removed: the default is to remove, so this
// only guards against one of them being added to Allowed by mistake.
func TestServerSettingsAreNeverKept(t *testing.T) {
	for _, n := range []string{"CORAL_SECRET_KEY", "CORAL_CLOUD_URL", "CORAL_HUB", "CORAL_TLS_CERT", "CORAL_TLS_KEY",
		"CORAL_DEBUG_INPUT", "CORAL_TRUSTED_PROXIES", "CORAL_WEBHOOK_ALLOW_PRIVATE", "CORAL_NO_WELCOME",
		"CORAL_DB_ENCRYPTION", "CORAL_PPROF_ADDR", "CORAL_PROJECT_ROOTS", "CORAL_ROOT", "CORAL_TMUX_SOCKET",
		"CORAL_SESSION_TOKEN", "CORAL_STRICT_LOCAL_AUTH", "CORAL_REQUIRE_AUTH_ON_LOOPBACK", "CORAL_ALLOW_SHARED_DATA_DIR"} {
		assert.False(t, Keep(n), n)
	}
}

// #3717: the hub access cap given at launch is the server's: an agent (and a Coral an agent
// starts) never inherits it.
func TestAgentsNeverInheritTheCloudMaxMode(t *testing.T) {
	if Keep("CORAL_CLOUD_MAX_MODE") {
		t.Fatal("CORAL_CLOUD_MAX_MODE reaches agents")
	}
}

// #19 (Security's #20 F1): Windows reads a variable whatever case it was set in, so the filter
// compares names without case: a server setting spelled Coral_ or coral_ is still removed, and
// an allowed one is still kept.
func TestKeepIgnoresCase(t *testing.T) {
	for name, want := range map[string]bool{
		"CORAL_SECRET_KEY":    false,
		"Coral_Secret_Key":    false,
		"coral_secret_key":    false,
		"cOrAl_CLOUD_URL":     false,
		"CORAL_URL":           true,
		"coral_url":           true,
		"Coral_Session_Name":  true,
		"coral_workflow_step": true,
		"Coral_Step_Name":     true,
		"coral_prev_output":   true,
		"PATH":                true,
		"Path":                true,
		"CORAL":               true, // not a CORAL_ variable
		"CORALX_Y":            true,
	} {
		assert.Equal(t, want, Keep(name), name)
	}
	assert.Equal(t, []string{"Path=C:/x", "coral_url=http://h"},
		Filter([]string{"Path=C:/x", "Coral_Secret_Key=leak", "coral_url=http://h", "coral_secret_key=leak"}))
	assert.True(t, IsCoral("coral_x"))
	assert.False(t, IsCoral("CORA"))
}

// #13: the name a Codex agent's tools find their token file under passes Codex's default
// excludes (KEY, SECRET, TOKEN, case-insensitive), and the names a later Codex might add (PASS,
// CRED); it reaches agents and is an agent's identity, never the server's.
func TestSessionAuthFileEnvPassesCodexExcludes(t *testing.T) {
	for _, word := range []string{"KEY", "SECRET", "TOKEN", "PASS", "CRED"} {
		assert.NotContains(t, strings.ToUpper(SessionAuthFileEnv), word)
	}
	assert.True(t, Keep(SessionAuthFileEnv))
	assert.Contains(t, IdentityVars, SessionAuthFileEnv)
}
