package tmux

import "testing"

func TestParsePaneList_Basic(t *testing.T) {
	out := "codex-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee|codex-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee:0.0|/Users/me/coral-go|coral\n"
	panes := parsePaneList(out, "/tmp/sock")
	if len(panes) != 1 {
		t.Fatalf("got %d panes, want 1", len(panes))
	}
	p := panes[0]
	if p.SessionName != "codex-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" {
		t.Errorf("SessionName = %q", p.SessionName)
	}
	if p.Target != "codex-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee:0.0" {
		t.Errorf("Target = %q", p.Target)
	}
	if p.CurrentPath != "/Users/me/coral-go" {
		t.Errorf("CurrentPath = %q", p.CurrentPath)
	}
	if p.PaneTitle != "coral" {
		t.Errorf("PaneTitle = %q", p.PaneTitle)
	}
	if p.SocketPath != "/tmp/sock" {
		t.Errorf("SocketPath = %q", p.SocketPath)
	}
}

// Regression: Codex sets its pane title to "[ ! ] Action Required | coral"
// while waiting for approval. The '|' inside the title must not shift the
// other fields — previously the title came first and this corrupted the
// session name, making the agent look dead to the reconciler.
func TestParsePaneList_PipeInTitle(t *testing.T) {
	out := "codex-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee|codex-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee:0.0|/Users/me/coral-go|[ ! ] Action Required | coral\n" +
		"claude-11111111-2222-3333-4444-555555555555|claude-11111111-2222-3333-4444-555555555555:0.0|/Users/me/other|✳ a | b | c\n"
	panes := parsePaneList(out, "")
	if len(panes) != 2 {
		t.Fatalf("got %d panes, want 2", len(panes))
	}
	if panes[0].SessionName != "codex-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" {
		t.Errorf("SessionName = %q", panes[0].SessionName)
	}
	if panes[0].PaneTitle != "[ ! ] Action Required | coral" {
		t.Errorf("PaneTitle = %q", panes[0].PaneTitle)
	}
	if panes[1].SessionName != "claude-11111111-2222-3333-4444-555555555555" {
		t.Errorf("SessionName = %q", panes[1].SessionName)
	}
	if panes[1].PaneTitle != "✳ a | b | c" {
		t.Errorf("PaneTitle = %q", panes[1].PaneTitle)
	}
}

func TestParsePaneList_SkipsMalformedAndBlank(t *testing.T) {
	out := "\n\nonly|three|fields\n\nsess|sess:0.0|/path|\n"
	panes := parsePaneList(out, "")
	if len(panes) != 1 {
		t.Fatalf("got %d panes, want 1", len(panes))
	}
	if panes[0].SessionName != "sess" || panes[0].PaneTitle != "" {
		t.Errorf("unexpected pane %+v", panes[0])
	}
}
