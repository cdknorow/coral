package board

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

// #3920 R4: the check runner's environment stays minimal (no provider key or task variable gets
// through) and, on Windows, has what go needs to find a build cache.
func TestCheckRunnerEnvIsMinimal(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-should-not-pass")
	t.Setenv("CORAL_SESSION_TOKEN", "should-not-pass")
	env := checkRunnerEnv()
	keys := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		keys[strings.ToUpper(k)] = v
	}
	for _, k := range []string{"ANTHROPIC_API_KEY", "CORAL_SESSION_TOKEN"} {
		if _, ok := keys[k]; ok {
			t.Errorf("%s reached the check's environment", k)
		}
	}
	if keys["HOME"] != os.TempDir() || keys["GOPROXY"] != "off" {
		t.Errorf("env = %v", env)
	}
	if runtime.GOOS == "windows" {
		for _, k := range []string{"LOCALAPPDATA", "USERPROFILE", "SYSTEMROOT", "TEMP"} {
			if keys[k] == "" {
				t.Errorf("%s missing on Windows: env = %v", k, env)
			}
		}
	}
}
