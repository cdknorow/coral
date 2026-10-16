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
		"CORAL_SESSION_TOKEN"} {
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
