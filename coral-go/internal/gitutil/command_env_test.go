package gitutil

import (
	"context"
	"strings"
	"testing"
)

// #25 H3: git runs without the server's own CORAL_* settings, and with the rest of the environment.
func TestCommandEnvironmentHasNoServerSettings(t *testing.T) {
	t.Setenv("CORAL_SECRET_KEY", "coral-sentinel-secret")
	t.Setenv("GIT_ASKPASS_SENTINEL", "kept")
	var hasKept, hasPath bool
	for _, kv := range Command(context.Background(), "", "status").Env {
		if strings.HasPrefix(kv, "CORAL_SECRET_KEY=") {
			t.Errorf("git got %q", kv)
		}
		hasKept = hasKept || kv == "GIT_ASKPASS_SENTINEL=kept"
		hasPath = hasPath || strings.HasPrefix(strings.ToUpper(kv), "PATH=")
	}
	if !hasKept || !hasPath {
		t.Errorf("the ordinary environment was dropped (kept=%v path=%v)", hasKept, hasPath)
	}
}
