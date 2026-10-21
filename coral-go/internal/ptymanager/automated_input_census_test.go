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
	"SendKeysToTarget": true, "SendKeys": true, "SendTerminalInput": true, "SendLine": true, "SendLineCtx": true,
	"SendTerminalInputToTarget": true, "SendRawKeys": true, "pasteToTarget": true,
	"AutoAcceptSend": true, "SendEnter": true, "SendArrow": true,
}

// A sender declares the guard that decides when it may type (#5254). guard is one of:
//   - guardGate: it types only through the automated-input gate, which waits for nothing to be open on the screen and no draft in the box
//   - guardHuman: a person's own action; the route is operator-only (no agent token), or it is the dashboard terminal
//   - guardScreen: it reads the agent's screen first and types nothing unless the screen is what it expects; proof names a test that
//     shows it sends nothing for a draft in the box or a look-alike screen
//   - guardLaunch: a launch command typed into a fresh shell, before any agent CLI runs (and the clearing of such a line)
//   - guardAutoAccept: the opt-in auto-accept feature's own exemption
//   - guardPrimitive: the implementation of a send primitive, called by a sender that is itself listed
type guard string

const (
	guardGate       guard = "gate"
	guardHuman      guard = "human"
	guardScreen     guard = "screen"
	guardLaunch     guard = "launch"
	guardAutoAccept guard = "auto-accept"
	guardPrimitive  guard = "primitive"
)

type sender struct {
	guard guard
	why   string
	proof string // guardScreen: the test that proves it
}

// allowedSenders is "file<TAB>function<TAB>call" -> its guard. A call is a send function above, or a direct tmux "send-keys" or
// "paste-buffer" command ("tmux send-keys"): the Ctrl-U that clears a launch line is one (#5171).
var allowedSenders = map[string]sender{
	// The gate's send functions: they run only when nothing waits on the user.
	"internal/background/agent_exited.go\tdeliverAutomatedIfWanted\tSendInput":               {guardGate, "gate", ""},
	"internal/server/routes/agent_exited.go\tdeliverAutomated\tSendInput":                    {guardGate, "gate", ""},
	"internal/server/routes/agent_ui_request.go\tSessionsHandler.agentUIRequest\tSendPrompt": {guardGate, "gate (its send closure)", ""},
	"internal/ptymanager/automated_input_deliver.go\tinputGate.holdEnter\tSendEnter":         {guardGate, "gate (a withheld Enter, sent once the prompt is gone)", ""},
	// AgentLauncher.SendPrompt, which goes through the gate itself.
	"internal/background/launcher.go\tAgentLauncher.startPromptDelivery\tSendPrompt":            {guardGate, "gated launcher prompt", ""},
	"internal/background/workflow_steps.go\tWorkflowRunner.executeAgentInteractive\tSendPrompt": {guardGate, "gated launcher prompt", ""},
	// The opt-in auto-accept feature, through its named exemption.
	"internal/server/routes/auto_accept.go\tAutoAcceptor.check\tAutoAcceptSend": {guardAutoAccept, "auto-accept exemption", ""},
	"internal/server/routes/auto_accept.go\tAutoAcceptor.check\tSendRawInput":   {guardAutoAccept, "inside AutoAcceptSend", ""},
	// A person's input. /send and /keys gate any caller that isn't a person.
	"internal/server/routes/websocket.go\tterminalStream.handleFrame\tSendInput":           {guardHuman, "human: terminal typing", ""},
	"internal/server/routes/prompt_answer.go\tSessionsHandler.AnswerPrompt\tSendInput":     {guardScreen, "human: picks an option", "TestAnswerPrompt_ClaudeStillOpen"},
	"internal/server/routes/prompt_answer.go\tSessionsHandler.AnswerPrompt\tSendRawInput":  {guardScreen, "human: picks an option", "TestAnswerPromptSendsNoKeyOverADraft"},
	"internal/server/routes/prompt_answer.go\tSessionsHandler.selectUnnumbered\tSendArrow": {guardScreen, "human: picks an option (a menu without numbers, #4543)", "TestAnswerPrompt_AgyNavigatesWithArrows"},
	"internal/server/routes/prompt_answer.go\tSessionsHandler.selectUnnumbered\tSendEnter": {guardScreen, "human: picks an option (once the arrows have selected it)", "TestAnswerPrompt_AgyNavigatesWithArrows"},
	"internal/server/routes/prompt_answer.go\tSessionsHandler.toggleMulti\tSendRawInput":   {guardScreen, "human: ticks a box of a multi-select question (#5232)", "TestAnswerPrompt_MultiSelectToggleAndSubmit"},
	"internal/server/routes/prompt_answer.go\tSessionsHandler.submitMulti\tSendArrow":      {guardScreen, "human: takes Submit of a multi-select question (#5232)", "TestAnswerPrompt_MultiSelectSubmitRefusals"},
	"internal/server/routes/prompt_answer.go\tSessionsHandler.submitMulti\tSendEnter":      {guardScreen, "human: takes Submit of a multi-select question (once the arrows have selected it)", "TestAnswerPrompt_MultiSelectSubmitRefusals"},
	"internal/server/routes/prompt_answer.go\tSessionsHandler.confirmAnswer\tSendEnter":    {guardScreen, "human: picks an option (the Enter a Codex menu needs once the digit has selected it, #4543)", "TestAnswerPrompt_CodexHookTrust"},
	"internal/server/routes/sessions_input.go\tSessionsHandler.Send\tSendInput":            {guardHuman, "human, else gated", ""},
	"internal/server/routes/human_send.go\tSessionsHandler.deliverHumanSend\tSendInput":    {guardHuman, "human: a person's message from Chat (#4587)", ""},
	"internal/server/routes/human_send.go\tSessionsHandler.deliverHumanSend\tSendEnter":    {guardHuman, "human: the same message's Enter again, while exactly its text is still in the box (#4587)", ""},
	"internal/server/routes/sessions_input.go\tSessionsHandler.Keys\tSendRawInput":         {guardHuman, "human, else gated", ""},
	// Launch commands typed into a fresh shell, before any agent CLI runs.
	"internal/server/routes/sessions_lifecycle.go\tsendLaunchCommand\tSendToTarget": {guardLaunch, "launch command", ""},
	"internal/background/runtime_tmux.go\tTmuxRuntime.SpawnAgent\tSendKeysToTarget": {guardLaunch, "launch command", ""},
	"cmd/launch-coral/main.go\tlaunchAgentSessions\tSendKeysToTarget":               {guardLaunch, "launch command", ""},
	"internal/ptymanager/tmux_backend.go\tTmuxBackend.Spawn\tSendKeysToTarget":      {guardLaunch, "launch command", ""},
	"internal/ptymanager/tmux_backend.go\tTmuxBackend.Restart\tSendKeysToTarget":    {guardLaunch, "launch command", ""},
	// The tmux commands themselves (#5254): every key or paste that reaches a pane is one of these.
	"internal/tmux/prompt.go\tClient.SendPrompt\ttmux paste-buffer":                {guardPrimitive, "primitive: the paste of SendPrompt, whose callers are listed", ""},
	"internal/tmux/prompt.go\tClient.SendPrompt\ttmux send-keys":                   {guardPrimitive, "primitive: the Enter of SendPrompt, withheld by enterAllowed (the gate's re-check)", ""},
	"internal/tmux/send_keys.go\tClient.SendRawKeys\ttmux send-keys":               {guardPrimitive, "primitive: named keys, one at a time", ""},
	"internal/tmux/send_keys.go\tClient.SendTerminalInputToTarget\ttmux send-keys": {guardPrimitive, "primitive: a person's terminal input", ""},
	"internal/tmux/send_keys.go\tClient.pasteToTarget\ttmux paste-buffer":          {guardPrimitive, "primitive: a private named buffer, deleted by the paste", ""},
	"internal/tmux/send_keys.go\tClient.typeToTarget\ttmux send-keys":              {guardPrimitive, "primitive: text then Enter, no Enter after a failed send or when enterAllowed says no", ""},
	"internal/tmux/send_keys.go\tclearInputLine\ttmux send-keys":                   {guardLaunch, "launch: the Ctrl-U that clears a launch command typed into a fresh shell whose Enter did not go (#5171); never reached for text typed to an agent", "TestLaunchLineIsClearedWhenItsEnterIsWithheld"},
	// The PTY master itself: every byte typed to a PTY agent goes through session.writeFull.
	"internal/ptymanager/session.go\tsession.writeFullCtx\tpty Write":     {guardPrimitive, "primitive: the PTY's write, reached by the senders above and by PTYBackend.SendHumanInput (a person's keystrokes, the gate's PTY rule #3482, logged by length only #60)", ""},
	"internal/ptymanager/pty_unix.go\tunixPTY.Write\tpty Write":           {guardPrimitive, "primitive: the PTY file", ""},
	"internal/ptymanager/pty_windows.go\twindowsPTY.Write\tpty Write":     {guardPrimitive, "primitive: the ConPTY", ""},
	"internal/ptymanager/pty_windows.go\twindowsPTY.Terminate\tpty Write": {guardPrimitive, "primitive: the Ctrl-C that starts a session's end on Windows (session.killWithin, recorded in the input log); nothing is typed after it", ""},
	// The primitives' own implementations, one layer calling the next.
	"internal/background/runtime_pty.go\tPTYRuntime.SendInput\tSendLine":                                             {guardPrimitive, "primitive", ""},
	"internal/background/runtime_tmux.go\tTmuxRuntime.SendInput\tSendKeys":                                           {guardPrimitive, "primitive", ""},
	"internal/ptymanager/manager.go\tPTYBackend.SendLine\tSendLineCtx":                                               {guardPrimitive, "primitive", ""},
	"internal/ptymanager/session_terminal_pty.go\tPTYSessionTerminal.SendInput\tSendLineCtx":                         {guardPrimitive, "primitive: carries ctx, so a person's send (inputlog.Human) is logged by length (#60 N3)", ""},
	"internal/ptymanager/session_terminal_pty.go\tPTYSessionTerminal.SendPrompt\tSendPrompt":                         {guardPrimitive, "primitive", ""},
	"internal/ptymanager/session_terminal_pty.go\tPTYSessionTerminal.SendRawInput\tSendInput":                        {guardPrimitive, "primitive", ""},
	"internal/ptymanager/session_terminal_pty.go\tPTYSessionTerminal.SendTerminalInput\tSendInput":                   {guardPrimitive, "primitive", ""},
	"internal/ptymanager/session_terminal_pty.go\tPTYSessionTerminal.SendToTarget\tSendLine":                         {guardPrimitive, "primitive", ""},
	"internal/ptymanager/session_terminal_tmux.go\tTmuxSessionTerminal.SendInput\tSendKeys":                          {guardPrimitive, "primitive", ""},
	"internal/ptymanager/session_terminal_tmux.go\tTmuxSessionTerminal.SendPrompt\tSendPrompt":                       {guardPrimitive, "primitive", ""},
	"internal/ptymanager/session_terminal_tmux.go\tTmuxSessionTerminal.SendRawInput\tSendRawKeys":                    {guardPrimitive, "primitive", ""},
	"internal/ptymanager/session_terminal_tmux.go\tTmuxSessionTerminal.SendTerminalInput\tSendTerminalInputToTarget": {guardPrimitive, "primitive", ""},
	"internal/ptymanager/session_terminal_tmux.go\tTmuxSessionTerminal.SendToTarget\tSendKeysToTarget":               {guardPrimitive, "primitive", ""},
	"internal/ptymanager/tmux_backend.go\tTmuxBackend.sendInput\tSendTerminalInputToTarget":                          {guardPrimitive, "primitive: SendInput, and SendHumanInput for a person's keystrokes (#60)", ""},
	"internal/ptymanager/prompt_screen.go\tTmuxSessionTerminal.SendEnter\tSendRawKeys":                               {guardPrimitive, "primitive", ""},
	"internal/background/runtime_tmux.go\tTmuxRuntime.SendEnter\tSendRawKeys":                                        {guardPrimitive, "primitive", ""},
	// #5114: the one 0 that dismisses Claude's session survey (never a rating), only when it is the only thing open.
	"internal/ptymanager/survey_dismiss.go\tinputGate.dismissSurvey\tSendRawInput": {guardScreen, "gate (a single 0, only for the exact session survey directly above the input box; waits for it to go; at most once per 30 s; #5114)", "TestSurveyWithADraftInTheInputBoxGetsNoZero"},
	// #3457: automated text as a bracketed paste.
	"internal/ptymanager/automated_input_deliver.go\tinputGate.pasteAndEnter\tSendPrompt":     {guardGate, "gate (the paste, Enter only after the re-check)", ""},
	"internal/ptymanager/paste_check.go\tinputGate.enterAndConfirm\tSendEnter":                {guardGate, "gate (a held paste's Enter, once its text is in the box and no prompt is open; confirmed, #4104)", ""},
	"internal/ptymanager/paste_check.go\tinputGate.submitLeftover\tSendEnter":                 {guardGate, "gate (the Enter of Coral's own earlier paste left in the box, an exact match; never a person's draft, #4104)", ""},
	"internal/background/runtime_tmux.go\tTmuxRuntime.SendPrompt\tSendPrompt":                 {guardPrimitive, "primitive", ""},
	"internal/background/runtime_pty.go\tPTYRuntime.SendPrompt\tSendPrompt":                   {guardPrimitive, "primitive", ""},
	"internal/background/runtime_pty.go\tPTYRuntime.SendEnter\tSendInput":                     {guardPrimitive, "primitive", ""},
	"internal/ptymanager/session_terminal_pty.go\tPTYSessionTerminal.SendEnter\tSendInput":    {guardPrimitive, "primitive", ""},
	"internal/ptymanager/session_terminal_pty.go\tPTYSessionTerminal.SendArrow\tSendInput":    {guardPrimitive, "primitive", ""},
	"internal/ptymanager/prompt_screen.go\tTmuxSessionTerminal.SendArrow\tSendRawKeys":        {guardPrimitive, "primitive", ""},
	"internal/tmux/send_keys.go\tClient.typeToTarget\tpasteToTarget":                          {guardPrimitive, "primitive", ""}, // SendKeys (text) and SendKeysToTarget (a launch command) type through it (#4361)
	"internal/tmux/send_keys.go\tClient.SendTerminalInputToTarget\tpasteToTarget":             {guardPrimitive, "primitive", ""},
	"internal/tmux/send_keys.go\tClient.SendTerminalInputToTarget\tSendTerminalInputToTarget": {guardPrimitive, "primitive", ""},
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
				call := ""
				if sel, ok := c.Fun.(*ast.SelectorExpr); ok && sendCalls[sel.Sel.Name] {
					call = sel.Sel.Name
				}
				if sel, ok := c.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Write" {
					if x, ok := sel.X.(*ast.SelectorExpr); ok && (x.Sel.Name == "proc" || x.Sel.Name == "cpty" || x.Sel.Name == "ptyFile") {
						call = "pty Write" // bytes to a PTY's master
					}
				}
				for _, arg := range c.Args {
					if lit, ok := arg.(*ast.BasicLit); ok && (lit.Value == `"send-keys"` || lit.Value == `"paste-buffer"`) {
						call = "tmux " + strings.Trim(lit.Value, `"`)
					}
				}
				if call == "" {
					return true
				}
				key := rel + "\t" + fn + "\t" + call
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
		t.Errorf("unreviewed terminal send: %s (an automated sender must use ptymanager.DeliverAutomated; any sender is added to allowedSenders with its guard, #5254)", u)
	}
	// A stale entry would let a future sender reuse a reviewed name unnoticed.
	for key := range allowedSenders {
		if !seen[key] {
			t.Errorf("allowlist entry no longer matches any call (remove it): %s", strings.ReplaceAll(key, "\t", " "))
		}
	}
	// Every sender declares a guard, and one that reads the screen names the test that proves it.
	tests := testFuncNames(t, root)
	for key, s := range allowedSenders {
		name := strings.ReplaceAll(key, "\t", " ")
		switch s.guard {
		case guardGate, guardHuman, guardLaunch, guardAutoAccept, guardPrimitive:
			if s.proof != "" && !tests[s.proof] {
				t.Errorf("%s: proof %s is not a test", name, s.proof)
			}
		case guardScreen:
			if !tests[s.proof] {
				t.Errorf("%s: a screen-guarded sender names a test that proves it (a draft in the box, a look-alike screen); %q is not one", name, s.proof)
			}
		default:
			t.Errorf("%s: no guard declared (%q)", name, s.guard)
		}
		if s.why == "" {
			t.Errorf("%s: say what its guard is", name)
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

// testFuncNames is every Test function declared in a _test.go file under root.
func testFuncNames(t *testing.T, root string) map[string]bool {
	names := map[string]bool{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".")) && p != root {
			return filepath.SkipDir
		}
		if !strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, 0)
		if err != nil {
			return err
		}
		for _, decl := range f.Decls {
			if fd, ok := decl.(*ast.FuncDecl); ok && fd.Recv == nil && strings.HasPrefix(fd.Name.Name, "Test") {
				names[fd.Name.Name] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return names
}
