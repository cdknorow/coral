//go:build windows

package background

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Windows proof of #6974: a fake Claude Code on a ConPTY draws the session survey above an input box, and a board nudge handed over with the background runtime gets the survey dismissed with one 0 and is delivered;
// over a draft in the box nothing is typed. The fake is this test binary run again (-fake-claude=<mode>): the agent's environment is filtered, so a mode cannot travel in it.
var (
	fakeClaudeFlag = flag.String("fake-claude", "", "internal: act as the fake Claude Code for the survey tests (survey or draft); it writes what it receives to typed.txt in its working folder")
)

func TestFakeSurveyClaudeHelper(t *testing.T) {
	if *fakeClaudeFlag == "" {
		t.Skip("not the fake Claude")
	}
	wd, werr := os.Getwd()
	require.NoError(t, werr)
	mode, out := *fakeClaudeFlag, filepath.Join(wd, "typed.txt")
	k32 := syscall.NewLazyDLL("kernel32.dll")
	getMode, setMode := k32.NewProc("GetConsoleMode"), k32.NewProc("SetConsoleMode")
	inH, outH := syscall.Handle(os.Stdin.Fd()), syscall.Handle(os.Stdout.Fd())
	var m uint32
	getMode.Call(uintptr(inH), uintptr(unsafePtr(&m)))
	// raw keys as bytes: no line input, no echo, no Ctrl-C processing, virtual-terminal input
	setMode.Call(uintptr(inH), uintptr((m&^(0x1|0x2|0x4))|0x200))
	getMode.Call(uintptr(outH), uintptr(unsafePtr(&m)))
	setMode.Call(uintptr(outH), uintptr(m|0x4|0x1)) // virtual-terminal output
	draw := func(survey bool, box string) {
		rule := strings.Repeat("\u2500", 80) // the characters Claude Code draws: the gate finds the input box by them
		fmt.Print("\x1b[2J\x1b[H")
		fmt.Print("Nothing for me here. I have no open tasks.\r\n\r\nWorked for 3s\r\n\r\n")
		if survey {
			fmt.Print("● How is Claude doing this session? (optional)\r\n  1: Bad    2: Fine   3: Good   0: Dismiss\r\n\r\n")
		}
		fmt.Printf("%s\r\n\u276f %s\r\n%s\r\n  \u23f5\u23f5 bypass permissions on (shift+tab to cycle)\r\n", rule, box, rule)
	}
	fmt.Print("\x1b[?2004h")
	draw(true, "")
	if mode == "draft" {
		draw(true, "half-typed note")
	}
	f, err := os.OpenFile(out, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	defer f.Close()
	diag, _ := os.OpenFile(out+".diag", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	defer diag.Close()
	fmt.Fprintf(diag, "fake started, mode %s\n", mode)
	buf := make([]byte, 4096)
	first := true
	for {
		n, err := os.Stdin.Read(buf)
		fmt.Fprintf(diag, "read n=%d err=%v bytes=%q\n", n, err, buf[:n])
		diag.Sync()
		if n > 0 {
			if first && mode == "survey" {
				first = false
				if buf[0] == '0' {
					f.WriteString("DISMISSED\n")
					draw(false, "")
					f.Write(buf[1:n])
				} else {
					f.WriteString("FIRST-KEY-WAS-NOT-0\n")
					f.Write(buf[:n])
				}
			} else {
				f.Write(buf[:n])
			}
			f.Sync()
		}
		if err != nil {
			return
		}
	}
}

func unsafePtr(m *uint32) unsafe.Pointer { return unsafe.Pointer(m) }

func startFakeSurveyClaude(t *testing.T, mode string) (rt *PTYRuntime, name, sid, out string) {
	t.Helper()
	exe, err := os.Executable()
	require.NoError(t, err)
	dir, err := os.MkdirTemp("", "coralsurvey")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	out = filepath.Join(dir, "typed.txt")
	rt = newTestPTYRuntime()
	sid = fmt.Sprintf("11111111-2222-3333-4444-%012d", surveySessions.Add(1)) // a session of its own every run: the gate keeps its 30 s retry gap per session name for the whole process
	name = "claude-" + sid
	// the start command runs in PowerShell: call operator, quoted paths, no pipe character
	cmd := fmt.Sprintf("& '%s' '-test.run=TestFakeSurveyClaudeHelper' '-fake-claude=%s'", exe, mode) // single-quoted: PowerShell splits an unquoted -test.run=X at the dot
	require.NoError(t, rt.SpawnAgent(context.Background(), name, dir, "", cmd), "the fake Claude starts on a ConPTY")
	t.Cleanup(func() { rt.KillAgent(context.Background(), name) })
	// wait until the fake has drawn the survey (a nudge typed before it is drawn lands on it as the first key)
	if !assert.Eventually(t, func() bool {
		scr, serr := rt.AgentScreen(context.Background(), name, "claude", sid)
		return serr == nil && strings.Contains(scr, "How is Claude doing this session?")
	}, 30*time.Second, 100*time.Millisecond, "the fake Claude draws the survey") {
		d, _ := os.ReadFile(out + ".diag")
		scr, serr := rt.AgentScreen(context.Background(), name, "claude", sid)
		t.Logf("DIAG command: %s", cmd)
		t.Logf("DIAG fake's log: %q", string(d))
		t.Logf("DIAG screen (err %v): %.1500q", serr, scr)
		t.FailNow()
	}
	return
}

var surveySessions atomic.Int64

const surveyNudge = "You have 1 unread message on the message board. Run 'coral-board read' to see them."

func TestWindowsABoardNudgeBehindTheSurveyDismissesItWithOneZeroAndIsDelivered(t *testing.T) {
	rt, name, sid, out := startFakeSurveyClaude(t, "survey")
	done := make(chan error, 1)
	var logs strings.Builder
	lg := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	go func() {
		_, err := deliverAutomatedIfWanted(context.Background(), rt, lg, name, "claude", sid, "nudge-1", surveyNudge, "board nudge", false, true, nil)
		done <- err
	}()
	ok := assert.Eventually(t, func() bool {
		b, _ := os.ReadFile(out)
		return strings.Contains(string(b), "unread message")
	}, 45*time.Second, 200*time.Millisecond, "the survey is dismissed and the nudge is typed")
	if !ok {
		d, _ := os.ReadFile(out + ".diag")
		scr, serr := rt.AgentScreen(context.Background(), name, "claude", sid)
		t.Logf("DIAG fake's log: %q", string(d))
		t.Logf("DIAG screen (err %v): %q", serr, scr)
		t.Logf("DIAG gate logs: %s", logs.String())
		t.FailNow()
	}
	b, _ := os.ReadFile(out)
	got := string(b)
	t.Logf("what the fake Claude received (after its marker): %q", strings.ReplaceAll(got, "\x1b", "<ESC>"))
	require.True(t, strings.HasPrefix(got, "DISMISSED\n"), "the first key typed was one 0 (not a rating): %q", got)
	require.Contains(t, got, "\x1b[200~"+surveyNudge+"\x1b[201~", "the nudge arrived as one bracketed paste: %q", got)
	rest := strings.NewReplacer("\x1b[200~", "", "\x1b[201~", "").Replace(strings.TrimPrefix(got, "DISMISSED\n")) // the bracketed-paste markers hold a 0 of their own
	require.Zero(t, strings.Count(rest, "0"), "no second 0 and no other digit key reached the agent: %q", got)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
	}
}

func TestWindowsNothingIsTypedOverADraftBehindTheSurvey(t *testing.T) {
	rt, name, sid, out := startFakeSurveyClaude(t, "draft")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := deliverAutomatedIfWanted(ctx, rt, slog.Default(), name, "claude", sid, "nudge-2", surveyNudge, "board nudge", false, true, nil)
		done <- err
	}()
	time.Sleep(15 * time.Second) // sleep-ok: long enough for the gate to look, wait and retry
	b, _ := os.ReadFile(out)
	t.Logf("what the fake Claude received over a draft: %q", string(b))
	require.Empty(t, string(b), "no 0 and no nudge is typed over a draft in the input box")
	scr, serr := rt.AgentScreen(context.Background(), name, "claude", sid)
	require.NoError(t, serr)
	require.Contains(t, scr, "How is Claude doing this session?", "the survey is still up: the gate looked at it and left it, with the draft in the box")
	require.Contains(t, scr, "half-typed note", "and the draft is untouched")
	cancel()
	<-done
}
