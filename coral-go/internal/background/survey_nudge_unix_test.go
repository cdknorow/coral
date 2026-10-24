//go:build !windows

package background

import (
	"bufio"
	"context"
	"github.com/google/uuid"
	"golang.org/x/sys/unix"
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
# Input must be ready before the screen advertises a dismissible survey.
stty raw -echo
draw survey
k=$(dd bs=1 count=1 2>/dev/null)
printf '%s\n' "$k" > "$KEY"
if [ "$k" = "0" ]; then
  draw clear
  exec cat > "$OUT"
fi
`

// The board's nudges are delivered with the background runtime as the gate's terminal (deliverAutomatedIfWanted). The runtime could read the screen but not type a single key, so the gate's dismissal of Claude's session survey
// (a 0, #5114) never ran for a nudge: the survey held every one of them behind it for as long as it stayed up (#6974).
func TestABoardNudgeBehindClaudesSessionSurveyDismissesItAndIsDelivered(t *testing.T) {
	skipIfNoPTY(t)
	dir := t.TempDir()
	// FIFOs acknowledge what the fake CLI actually read, instead of polling a
	// file and assuming a delivery goroutine finished within a separate 5 s wait.
	openPipe := func(name string) *os.File {
		path := filepath.Join(dir, name)
		require.NoError(t, unix.Mkfifo(path, 0o600))
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		require.NoError(t, err)
		t.Cleanup(func() { f.Close() })
		return f
	}
	output, key := openPipe("typed.pipe"), openPipe("key.pipe")
	script := filepath.Join(dir, "fake-claude.sh")
	require.NoError(t, os.WriteFile(script, []byte(fakeSurveyClaude), 0o755))
	t.Setenv("OUT", output.Name())
	t.Setenv("KEY", key.Name())
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute) // deadlock guard, not a speed assertion
	defer cancel()
	rt := newTestPTYRuntime()
	sid := uuid.NewString() // fresh gate identity on every -count run
	name := "claude-" + sid
	require.NoError(t, rt.SpawnAgent(ctx, name, dir, "", script))
	t.Cleanup(func() { cancel(); rt.KillAgent(context.Background(), name) })
	require.Eventually(t, func() bool {
		screen, err := rt.AgentScreen(ctx, name, "claude", sid)
		return err == nil && strings.Contains(screen, "How is Claude doing this session?")
	}, 20*time.Second, 50*time.Millisecond, "the input-ready fake drew the survey")
	type readResult struct {
		text string
		err  error
	}
	readUntil := func(f *os.File, marker string) <-chan readResult {
		result := make(chan readResult, 1)
		done := make(chan struct{})
		go func() {
			defer close(done)
			reader := bufio.NewReader(f)
			var text strings.Builder
			for {
				b, err := reader.ReadByte()
				if err != nil {
					result <- readResult{text.String(), err}
					return
				}
				text.WriteByte(b)
				if strings.HasSuffix(text.String(), marker) {
					result <- readResult{text.String(), nil}
					return
				}
			}
		}()
		t.Cleanup(func() {
			// macOS FIFOs are not pollable by Go: closing the descriptor alone
			// does not interrupt a blocked read. Wake this reader explicitly
			// after assertions finish, then join it before closing the file.
			_, _ = f.Write([]byte(marker))
			<-done
			f.Close()
		})
		return result
	}
	const text = "You have 1 unread message on the message board. Run 'coral-board read' to see them."
	keyRead, textRead := readUntil(key, "\n"), readUntil(output, text)
	delivery := make(chan error, 1)
	deliveryDone := make(chan struct{})
	go func() {
		defer close(deliveryDone)
		_, err := deliverAutomatedIfWanted(ctx, rt, slog.Default(), name, "claude", sid, "nudge-1", text, "board nudge", false, true, nil)
		delivery <- err
	}()
	t.Cleanup(func() { cancel(); rt.KillAgent(context.Background(), name); <-deliveryDone })
	receive := func(ch <-chan readResult, stage string) string {
		select {
		case result := <-ch:
			require.NoError(t, result.err, stage)
			return result.text
		case <-ctx.Done():
			t.Fatalf("%s: %v", stage, ctx.Err())
			return ""
		}
	}
	require.Equal(t, "0\n", receive(keyRead, "fake CLI read the dismiss key"), "dismissal never rates the session")
	typed := receive(textRead, "fake CLI read the nudge")
	require.Contains(t, typed, text)
	require.Equal(t, 1, strings.Count(typed, text))
	select {
	case err := <-delivery:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatalf("delivery did not return: %v", ctx.Err())
	}
}
