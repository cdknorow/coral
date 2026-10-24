//go:build !windows

package background

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/ptymanager"
)

// Both runtimes are what the gate is given as its terminal for a nudge: each must type a single key (#6974).
var (
	_ ptymanager.RawInputSender = (*PTYRuntime)(nil)
	_ ptymanager.RawInputSender = (*TmuxRuntime)(nil)
)

// A fake Claude Code on a real PTY: it draws the session survey above an empty input box (the layout seen on a stuck agent, #6974: the survey, a blank line, the box, the permission footer and an update notice), reads one key,
// and on a 0 redraws the screen without the survey and takes what is pasted next into a file.
const fakeSurveyClaude = `#!/bin/sh
rule='────────────────────────────────────────────────────────────────────────────────'
printf '\033[?2004h'
draw() {
  printf '\033[2J\033[H'
  printf '⏺ Nothing for me here. I have no open tasks.\n\n✻ Worked for 3s · done 11:28 PM\n\n'
  [ "$1" = survey ] && printf '● How is Claude doing this session? (optional)\n  1: Bad    2: Fine   3: Good   0: Dismiss\n\n'
  printf '%s\n❯ \n%s\n  ⏵⏵ bypass permissions on (shift+tab to cycle)\n                                        ✔ Update installed · Restart to update\n' "$rule" "$rule"
}
draw survey
stty raw -echo
k=$(dd bs=1 count=1 2>/dev/null)
if [ "$k" = "0" ]; then
  draw clear
  cat >> "$OUT"
fi
sleep 60
`

// The board's nudges are delivered with the background runtime as the gate's terminal (deliverAutomatedIfWanted). The runtime could read the screen but not type a single key, so the gate's dismissal of Claude's session survey
// (a 0, #5114) never ran for a nudge: the survey held every one of them behind it for as long as it stayed up (#6974).
func TestABoardNudgeBehindClaudesSessionSurveyDismissesItAndIsDelivered(t *testing.T) {
	skipIfNoPTY(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "typed.txt")
	script := filepath.Join(dir, "fake-claude.sh")
	require.NoError(t, os.WriteFile(script, []byte(fakeSurveyClaude), 0o755))
	t.Setenv("OUT", out)

	rt := newTestPTYRuntime()
	const sid = "11111111-2222-3333-4444-555555555555"
	name := "claude-" + sid
	require.NoError(t, rt.SpawnAgent(context.Background(), name, dir, "", script))
	t.Cleanup(func() { rt.KillAgent(context.Background(), name) })
	time.Sleep(500 * time.Millisecond) // sleep-ok: the fake draws its screen

	const text = "You have 1 unread message on the message board. Run 'coral-board read' to see them."
	done := make(chan error, 1)
	go func() {
		_, err := deliverAutomatedIfWanted(context.Background(), rt, slog.Default(), name, "claude", sid, "nudge-1", text, "board nudge", false, true, nil)
		done <- err
	}()
	require.Eventually(t, func() bool {
		b, _ := os.ReadFile(out)
		return strings.Contains(string(b), "unread message")
	}, 30*time.Second, 100*time.Millisecond, "the survey is dismissed with a 0 and the nudge is typed")
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
	}
}
