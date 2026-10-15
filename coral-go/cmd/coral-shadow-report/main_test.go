package main

import (
	"bytes"
	"strings"
	"testing"
)

// Lines as Coral writes them (slog's text form), from the #3481 and #3482 live runs.
const sampleLog = `2026/10/14 19:03:54.100000 INFO launch request received working_dir=/w agent_type=claude
2026/10/14 19:04:19.152583 INFO automated input decision session=claude-80f7a3e0-fe4f-a309-acdd-fcd06ff9f5e8 sender="board nudge" key=board-unread:t1b old=send new="hold: signals: in turn" agree=false mode=signals
2026/10/14 19:05:09.535082 INFO automated input decision session=claude-80f7a3e0-fe4f-a309-acdd-fcd06ff9f5e8 sender="board nudge" key=board-unread:t1b old=send new=send agree=true mode=signals
2026/10/14 19:06:00.000001 INFO automated input decision session=agy-11111111-2222-3333-4444-555555555555 sender="board message" key="SECRET message text the key carries" old="hold: prompt question: Allow rm -rf / ?" new=send agree=false mode=screen cli_version=1.3.5
2026/10/14 19:06:01.000001 INFO automated input decision session=claude-22222222-2222-3333-4444-555555555555 sender="board nudge" key=board-unread:t1b old="hold: selection menu" new="hold: signals: blocked (permission)" agree=true mode=screen cli_version=2.1.300 agent_type=claude state=blocked kind=permission
2026/10/14 19:06:02.000001 WARN automated input: state_disagreement: the signals say idle but the screen shows a prompt session=agy-11111111-2222-3333-4444-555555555555 why="prompt question: Allow rm -rf / ?" cli_version=1.3.5
2026/10/15 08:00:00.000001 INFO automated input decision session=codex-33333333-2222-3333-4444-555555555555 sender="task notification" key=x old=send new=send agree=true mode=screen agent_type=codex state=idle kind=
`

func TestReport(t *testing.T) {
	r := newReport("")
	r.read(strings.NewReader(sampleLog))
	var out bytes.Buffer
	r.print(&out, 10)
	got := out.String()
	for _, want := range []string{
		"5 decisions",
		"Sessions: 4",
		"claude ?",             // no cli_version on those lines
		"agy 1.3.5",            // agent type from the session name, version from the line
		"old=send new=hold: 1", // the signal rule holds what the screen rule sent
		"claude: send  ->  hold: signals: in turn",
		"old=hold new=send: 1", // the reverse, to check one by one
		"agy: hold: prompt question  ->  send",
		"claude: hold: selection menu  ->  hold: signals: blocked (permission)",
		"Agreement by state",
		"claude blocked (permission)", // state and kind from the line
		"codex idle ",                 // an empty kind is left out
		"claude ?  ",                  // lines before delta 3 have no state
		"state_disagreement (signals idle, a prompt on screen): 1",
		"prompt question (CLI 1.3.5)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	// No message text: a send's key is never printed, and screen text is cut to its class.
	for _, leak := range []string{"SECRET", "rm -rf", "board-unread:t1b"} {
		if strings.Contains(got, leak) {
			t.Errorf("printed %q:\n%s", leak, got)
		}
	}

	since := newReport("2026/10/15")
	since.read(strings.NewReader(sampleLog))
	if len(since.decisions) != 1 {
		t.Errorf("-since: %d decisions, want 1", len(since.decisions))
	}
}

func TestParseKV(t *testing.T) {
	kv := parseKV(`state_disagreement: the signals say idle session=s-1 why="a \"quoted\" why" cli_version=2.1.300`)
	if kv["session"] != "s-1" || kv["why"] != `a "quoted" why` || kv["cli_version"] != "2.1.300" {
		t.Errorf("%v", kv)
	}
}
