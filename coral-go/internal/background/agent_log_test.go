package background

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	at "github.com/cdknorow/coral/internal/agenttypes"
	"github.com/cdknorow/coral/internal/naming"
	"github.com/cdknorow/coral/internal/store"
)

// #3601 (weak site W17, Security's SHOULD-1): a job or scheduled run's agent log is created
// 0600 like every other agent's, through the same safe writer.
func TestJobRunLogIsPrivate(t *testing.T) {
	logs := t.TempDir()
	naming.SetDefaultLogDir(logs)
	t.Cleanup(func() { naming.SetDefaultLogDir("") }) // unset again: it follows the temp dir
	db := setupTestDB(t)
	l := NewAgentLauncher(&spawnRecorder{}, store.NewSessionStore(db), 8450)
	_, err := l.LaunchAgent(context.Background(), t.TempDir(), at.Claude, "job", "", nil, true, "p", "", "")
	require.NoError(t, err)
	matches, _ := filepath.Glob(filepath.Join(logs, "*_coral_*.log"))
	require.Len(t, matches, 1)
	fi, err := os.Stat(matches[0])
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
}

// No writer outside ptymanager creates an agent log by path: the job launcher and launch-coral
// go through ptymanager.ResetAgentLog (0600, never through a link). A new os.WriteFile,
// os.OpenFile or os.Create on a log variable fails this.
func TestAgentLogsAreWrittenOnlyThroughResetAgentLog(t *testing.T) {
	bad := regexp.MustCompile(`os\.(WriteFile|OpenFile|Create)\(\s*(logFile|logPath|newLogPath)\b`)
	var hits []string
	for _, dir := range []string{".", filepath.Join("..", "..", "cmd", "launch-coral"), filepath.Join("..", "server", "routes")} {
		files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			b, err := os.ReadFile(f)
			require.NoError(t, err)
			for i, line := range strings.Split(string(b), "\n") {
				if bad.MatchString(line) {
					hits = append(hits, f+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
				}
			}
		}
	}
	assert.Empty(t, hits, "agent logs must be created with ptymanager.ResetAgentLog")
}
