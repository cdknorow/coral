package ptymanager

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Every call that types into an agent's terminal, outside tests, comes from a
// reviewed place (#3431): the automated-input gate's own send, a person's input,
// the opt-in auto-accept exemption, a launch command typed into a fresh shell, or
// a send primitive's own implementation. A new automated sender that calls a raw
// send directly fails this test: route it through ptymanager.DeliverAutomated
// (routes.deliverAutomated, background.deliverAutomated) instead.
var sendCalls = map[string]bool{
	"SendInput": true, "SendRawInput": true, "SendPrompt": true, "SendToTarget": true,
	"SendKeysToTarget": true, "SendKeys": true, "SendTerminalInput": true, "SendLine": true,
	"SendTerminalInputToTarget": true, "SendRawKeys": true, "pasteToTarget": true,
	"AutoAcceptSend": true, "SendEnter": true, "SendArrow": true,
}

// allowedSenders is "file<TAB>function<TAB>call" -> why it may call it.
var allowedSenders = map[string]string{
	// The gate's send functions: they run only when nothing waits on the user.
	"internal/background/agent_exited.go\tdeliverAutomatedIfWanted\tSendInput":               "gate",
	"internal/server/routes/agent_exited.go\tdeliverAutomated\tSendInput":                    "gate",
	"internal/server/routes/agent_ui_request.go\tSessionsHandler.agentUIRequest\tSendPrompt": "gate (its send closure)",
	"internal/ptymanager/automated_input_deliver.go\tinputGate.holdEnter\tSendEnter":         "gate (a withheld Enter, sent once the prompt is gone)",
	// AgentLauncher.SendPrompt, which goes through the gate itself.
	"internal/background/launcher.go\tAgentLauncher.startPromptDelivery\tSendPrompt":            "gated launcher prompt",
	"internal/background/workflow_steps.go\tWorkflowRunner.executeAgentInteractive\tSendPrompt": "gated launcher prompt",
	// The opt-in auto-accept feature, through its named exemption.
	"internal/server/routes/auto_accept.go\tAutoAcceptor.check\tAutoAcceptSend": "auto-accept exemption",
	"internal/server/routes/auto_accept.go\tAutoAcceptor.check\tSendRawInput":   "inside AutoAcceptSend",
	// A person's input. /send and /keys gate any caller that isn't a person.
	"internal/server/routes/websocket.go\tterminalStream.handleFrame\tSendInput":           "human: terminal typing",
	"internal/server/routes/prompt_answer.go\tSessionsHandler.AnswerPrompt\tSendInput":     "human: picks an option",
	"internal/server/routes/prompt_answer.go\tSessionsHandler.AnswerPrompt\tSendRawInput":  "human: picks an option",
	"internal/server/routes/prompt_answer.go\tSessionsHandler.selectUnnumbered\tSendArrow": "human: picks an option (a menu without numbers, #4543)",
	"internal/server/routes/prompt_answer.go\tSessionsHandler.selectUnnumbered\tSendEnter": "human: picks an option (once the arrows have selected it)",
	"internal/server/routes/prompt_answer.go\tSessionsHandler.toggleMulti\tSendRawInput":   "human: ticks a box of a multi-select question (#5232)",
	"internal/server/routes/prompt_answer.go\tSessionsHandler.submitMulti\tSendArrow":      "human: takes Submit of a multi-select question (#5232)",
	"internal/server/routes/prompt_answer.go\tSessionsHandler.submitMulti\tSendEnter":      "human: takes Submit of a multi-select question (once the arrows have selected it)",
	"internal/server/routes/prompt_answer.go\tSessionsHandler.confirmAnswer\tSendEnter":    "human: picks an option (the Enter a Codex menu needs once the digit has selected it, #4543)",
	"internal/server/routes/sessions_input.go\tSessionsHandler.Send\tSendInput":            "human, else gated",
	"internal/server/routes/human_send.go\tSessionsHandler.deliverHumanSend\tSendInput":    "human: a person's message from Chat (#4587)",
	"internal/server/routes/human_send.go\tSessionsHandler.deliverHumanSend\tSendEnter":    "human: the same message's Enter again, while exactly its text is still in the box (#4587)",
	"internal/server/routes/sessions_input.go\tSessionsHandler.Keys\tSendRawInput":         "human, else gated",
	// Launch commands typed into a fresh shell, before any agent CLI runs.
	"internal/server/routes/sessions_lifecycle.go\tsendLaunchCommand\tSendToTarget": "launch command",
	"internal/background/runtime_tmux.go\tTmuxRuntime.SpawnAgent\tSendKeysToTarget": "launch command",
	"cmd/launch-coral/main.go\tlaunchAgentSessions\tSendKeysToTarget":               "launch command",
	"internal/ptymanager/tmux_backend.go\tTmuxBackend.Spawn\tSendKeysToTarget":      "launch command",
	"internal/ptymanager/tmux_backend.go\tTmuxBackend.Restart\tSendKeysToTarget":    "launch command",
	// The primitives' own implementations, one layer calling the next.
	"internal/background/runtime_pty.go\tPTYRuntime.SendInput\tSendLine":                                             "primitive",
	"internal/background/runtime_tmux.go\tTmuxRuntime.SendInput\tSendKeys":                                           "primitive",
	"internal/ptymanager/manager.go\tPTYBackend.SendLine\tSendInput":                                                 "primitive",
	"internal/ptymanager/manager.go\tPTYBackend.SendHumanInput\tSendInput":                                           "a person's keystrokes (the dashboard terminal), recorded for the gate's PTY rule (#3482)",
	"internal/ptymanager/session_terminal_pty.go\tPTYSessionTerminal.SendInput\tSendLine":                            "primitive",
	"internal/ptymanager/session_terminal_pty.go\tPTYSessionTerminal.SendPrompt\tSendPrompt":                         "primitive",
	"internal/ptymanager/session_terminal_pty.go\tPTYSessionTerminal.SendRawInput\tSendInput":                        "primitive",
	"internal/ptymanager/session_terminal_pty.go\tPTYSessionTerminal.SendTerminalInput\tSendInput":                   "primitive",
	"internal/ptymanager/session_terminal_pty.go\tPTYSessionTerminal.SendToTarget\tSendLine":                         "primitive",
	"internal/ptymanager/session_terminal_tmux.go\tTmuxSessionTerminal.SendInput\tSendKeys":                          "primitive",
	"internal/ptymanager/session_terminal_tmux.go\tTmuxSessionTerminal.SendPrompt\tSendPrompt":                       "primitive",
	"internal/ptymanager/session_terminal_tmux.go\tTmuxSessionTerminal.SendRawInput\tSendRawKeys":                    "primitive",
	"internal/ptymanager/session_terminal_tmux.go\tTmuxSessionTerminal.SendTerminalInput\tSendTerminalInputToTarget": "primitive",
	"internal/ptymanager/session_terminal_tmux.go\tTmuxSessionTerminal.SendToTarget\tSendKeysToTarget":               "primitive",
	"internal/ptymanager/tmux_backend.go\tTmuxBackend.SendInput\tSendTerminalInputToTarget":                          "primitive",
	"internal/ptymanager/prompt_screen.go\tTmuxSessionTerminal.SendEnter\tSendRawKeys":                               "primitive",
	"internal/background/runtime_tmux.go\tTmuxRuntime.SendEnter\tSendRawKeys":                                        "primitive",
	// #5114: the one 0 that dismisses Claude's session survey (never a rating), only when it is the only thing open.
	"internal/ptymanager/survey_dismiss.go\tinputGate.dismissSurvey\tSendRawInput": "gate (a single 0, only for the exact session survey directly above the input box; waits for it to go; at most once per 30 s; #5114)",
	// #3457: automated text as a bracketed paste.
	"internal/ptymanager/automated_input_deliver.go\tinputGate.pasteAndEnter\tSendPrompt":     "gate (the paste, Enter only after the re-check)",
	"internal/ptymanager/paste_check.go\tinputGate.enterAndConfirm\tSendEnter":                "gate (a held paste's Enter, once its text is in the box and no prompt is open; confirmed, #4104)",
	"internal/ptymanager/paste_check.go\tinputGate.submitLeftover\tSendEnter":                 "gate (the Enter of Coral's own earlier paste left in the box, an exact match; never a person's draft, #4104)",
	"internal/background/runtime_tmux.go\tTmuxRuntime.SendPrompt\tSendPrompt":                 "primitive",
	"internal/background/runtime_pty.go\tPTYRuntime.SendPrompt\tSendPrompt":                   "primitive",
	"internal/background/runtime_pty.go\tPTYRuntime.SendEnter\tSendInput":                     "primitive",
	"internal/ptymanager/session_terminal_pty.go\tPTYSessionTerminal.SendEnter\tSendInput":    "primitive",
	"internal/ptymanager/session_terminal_pty.go\tPTYSessionTerminal.SendArrow\tSendInput":    "primitive",
	"internal/ptymanager/prompt_screen.go\tTmuxSessionTerminal.SendArrow\tSendRawKeys":        "primitive",
	"internal/tmux/send_keys.go\tClient.typeToTarget\tpasteToTarget":                          "primitive", // SendKeys (text) and SendKeysToTarget (a launch command) type through it (#4361)
	"internal/tmux/send_keys.go\tClient.SendTerminalInputToTarget\tpasteToTarget":             "primitive",
	"internal/tmux/send_keys.go\tClient.SendTerminalInputToTarget\tSendTerminalInputToTarget": "primitive",
}

func TestEveryTerminalSendIsReviewed(t *testing.T) {
	root := filepath.Join("..", "..") // coral-go
	seen := map[string]bool{}
	var unexpected []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".")) && p != root {
			return filepath.SkipDir
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, 0)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			fn := funcName(fd)
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				c, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := c.Fun.(*ast.SelectorExpr)
				if !ok || !sendCalls[sel.Sel.Name] {
					return true
				}
				key := rel + "\t" + fn + "\t" + sel.Sel.Name
				seen[key] = true
				if _, ok := allowedSenders[key]; !ok {
					unexpected = append(unexpected, strings.ReplaceAll(key, "\t", " "))
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(unexpected)
	for _, u := range unexpected {
		t.Errorf("unreviewed terminal send: %s (an automated sender must use ptymanager.DeliverAutomated)", u)
	}
	// A stale entry would let a future sender reuse a reviewed name unnoticed.
	for key := range allowedSenders {
		if !seen[key] {
			t.Errorf("allowlist entry no longer matches any call (remove it): %s", strings.ReplaceAll(key, "\t", " "))
		}
	}
}

func funcName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return fd.Name.Name
	}
	t := fd.Recv.List[0].Type
	if s, ok := t.(*ast.StarExpr); ok {
		t = s.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name + "." + fd.Name.Name
	}
	return fd.Name.Name
}
