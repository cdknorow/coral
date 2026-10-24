package terminalinput

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// #5706's census: the guarantees of the single input path, as tests over the module's source (outside tests). A new write path, a
// recorder called from a new place, a sender without an Origin, or a caller that passes an origin nobody reviewed fails here, with its
// file and line.

type srcFile struct {
	rel  string
	fset *token.FileSet
	file *ast.File
}

func moduleSources(t *testing.T) []srcFile {
	t.Helper()
	root := filepath.Join("..", "..") // coral-go
	var out []srcFile
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
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		out = append(out, srcFile{filepath.ToSlash(rel), fset, f})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (s srcFile) pos(n ast.Node) string {
	return fmt.Sprintf("%s:%d", s.rel, s.fset.Position(n.Pos()).Line)
}

func funcName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return fd.Name.Name
	}
	typ := fd.Recv.List[0].Type
	if star, ok := typ.(*ast.StarExpr); ok {
		typ = star.X
	}
	if id, ok := typ.(*ast.Ident); ok {
		return id.Name + "." + fd.Name.Name
	}
	return fd.Name.Name
}

// calls of pkg.name in file f, with the enclosing function.
func callsOf(f srcFile, pkg, name string, visit func(call *ast.CallExpr, fn string)) {
	for _, decl := range f.file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := c.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == name {
				if x, ok := sel.X.(*ast.Ident); ok && x.Name == pkg {
					visit(c, funcName(fd))
				}
			}
			return true
		})
	}
}

// 1. inputlog.Write is called only by this package's Record; inputlog.Note only by the gate's own annotations.
func TestOnlyRecordWritesTheInputLog(t *testing.T) {
	noteFiles := map[string]bool{
		"internal/ptymanager/automated_input_deliver.go": true, // the auto-accept note
		"internal/ptymanager/automated_input_queue.go":   true, // held and dropped
		"internal/ptymanager/paste_check.go":             true, // gave up
	}
	for _, f := range moduleSources(t) {
		if f.rel == "internal/inputlog/inputlog.go" {
			continue
		}
		callsOf(f, "inputlog", "Write", func(c *ast.CallExpr, fn string) {
			if f.rel != "internal/terminalinput/terminalinput.go" || fn != "Record" {
				t.Errorf("%s: %s calls inputlog.Write: only terminalinput.Record records input (a person's text is hidden there)", f.pos(c), fn)
			}
		})
		callsOf(f, "inputlog", "Note", func(c *ast.CallExpr, fn string) {
			if !noteFiles[f.rel] {
				t.Errorf("%s: %s calls inputlog.Note: only the gate's own annotations are notes, input goes through terminalinput.Record", f.pos(c), fn)
			}
		})
	}
}

// 2. The wire: a "send-keys", "paste-buffer" or "load-buffer" string, or a write to a PTY's process, sits only in the choke points.
func TestEveryWireWriteIsInAChokePoint(t *testing.T) {
	allowed := map[string]map[string]bool{
		"send-keys":    {"internal/tmux/inputlog.go Client.sendKeys": true},
		"paste-buffer": {"internal/tmux/send_keys.go Client.pasteToTarget": true, "internal/tmux/prompt.go Client.SendPrompt": true},
		"load-buffer":  {"internal/tmux/prompt.go Client.loadPromptBuffer": true, "internal/tmux/send_keys.go Client.pasteToTarget": true},
	}
	for _, f := range moduleSources(t) {
		if !strings.HasPrefix(f.rel, "internal/tmux/") && !strings.HasPrefix(f.rel, "internal/ptymanager/") {
			continue
		}
		for _, decl := range f.file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			where := f.rel + " " + funcName(fd)
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.BasicLit:
					for word, ok := range allowed {
						if x.Value == `"`+word+`"` && !ok[where] {
							t.Errorf("%s: the tmux word %q in %s: a pane is written only through its choke point (Client.sendKeys, recordedBuffer's callers)", f.pos(x), word, funcName(fd))
						}
					}
				case *ast.CallExpr:
					sel, ok := x.Fun.(*ast.SelectorExpr)
					if !ok || sel.Sel.Name != "Write" {
						return true
					}
					recv, ok := sel.X.(*ast.SelectorExpr)
					if !ok || (recv.Sel.Name != "proc" && recv.Sel.Name != "cpty" && recv.Sel.Name != "ptyFile") {
						return true
					}
					okFn := map[string]bool{
						"internal/ptymanager/session.go session.writeInput":       true, // the ONE write of input
						"internal/ptymanager/pty_unix.go unixPTY.Write":           true, // the process itself
						"internal/ptymanager/pty_windows.go windowsPTY.Write":     true,
						"internal/ptymanager/pty_windows.go windowsPTY.Terminate": true, // the stop's Ctrl-C, recorded by session.killWithin and noteStopWrite
					}
					if !okFn[where] {
						t.Errorf("%s: %s writes to a PTY's process: input is written only by session.writeInput", f.pos(x), funcName(fd))
					}
				}
				return true
			})
		}
	}
}

// 3. Every exported sender and every choke point takes the Origin as an argument: a send that forgets it does not compile, and a new
// sender without one fails here. SendEnter and SendArrow carry no text (Enter and an arrow key are Coral's own control input), and
// SendHumanInput is a person's by its name.
func TestEverySenderAndChokePointTakesAnOrigin(t *testing.T) {
	senders := map[string]bool{
		"SendText": true, "SendPrompt": true, "SendLine": true, "SendLineCtx": true, "SendInput": true, "SendKeys": true,
		"SendKeysToTarget": true, "SendTerminalInputToTarget": true, "SendRawKeys": true, "SendRawInput": true,
		"SendToTarget": true, "SendTerminalInput": true,
		"writeInput": true, "sendKeys": true, "recordedBuffer": true, "pasteToTarget": true, "typeToTarget": true, "sendInput": true,
	}
	seen := map[string]bool{}
	check := func(f srcFile, name string, params *ast.FieldList, pos ast.Node) {
		if !senders[name] {
			return
		}
		if f.rel == "internal/background/launcher.go" && name == "SendPrompt" {
			seen[name] = true // AgentLauncher.SendPrompt hands its prompt to the automated-input gate (Coral's text); it sends nothing itself
			return
		}
		seen[name] = true
		ok := false
		if params != nil {
			for _, p := range params.List {
				switch x := p.Type.(type) {
				case *ast.SelectorExpr:
					ok = ok || x.Sel.Name == "Origin"
				case *ast.Ident:
					ok = ok || x.Name == "Origin"
				}
			}
		}
		if !ok {
			t.Errorf("%s: %s takes no terminalinput.Origin: every sender names whose input it is", f.pos(pos), name)
		}
	}
	for _, f := range moduleSources(t) {
		if !strings.HasPrefix(f.rel, "internal/") && !strings.HasPrefix(f.rel, "cmd/") {
			continue
		}
		for _, decl := range f.file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				check(f, d.Name.Name, d.Type.Params, d)
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}
					it, ok := ts.Type.(*ast.InterfaceType)
					if !ok {
						continue
					}
					for _, m := range it.Methods.List {
						ft, ok := m.Type.(*ast.FuncType)
						if !ok || len(m.Names) == 0 {
							continue
						}
						check(f, m.Names[0].Name, ft.Params, m)
					}
				}
			}
		}
	}
	for name := range senders {
		if !seen[name] {
			t.Errorf("the census lists %s, which no longer exists: remove it", name)
		}
	}
}

// 4. Who calls the exported senders, and the origin each passes: the reviewed list. Marking the dashboard's answer-prompt, /send and /keys
// as a person's is a one-line change here and at the call. "o" is a layer passing its caller's origin on.
func TestEveryCallOfASenderPassesAReviewedOrigin(t *testing.T) {
	senders := map[string]bool{
		"SendText": true, "SendPrompt": true, "SendLine": true, "SendLineCtx": true, "SendInput": true, "SendKeys": true,
		"SendKeysToTarget": true, "SendTerminalInputToTarget": true, "SendRawKeys": true, "SendRawInput": true,
		"SendToTarget": true, "SendTerminalInput": true,
	}
	got := map[string]bool{}
	for _, f := range moduleSources(t) {
		if !strings.HasPrefix(f.rel, "internal/") && !strings.HasPrefix(f.rel, "cmd/") {
			continue
		}
		for _, decl := range f.file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				c, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				name := ""
				switch fun := c.Fun.(type) {
				case *ast.SelectorExpr:
					name = fun.Sel.Name
				case *ast.Ident:
					name = fun.Name // a route's local send closure that takes the origin: its call names the origin too
				}
				if !senders[name] && name != "send" {
					return true
				}
				origin := "?"
				for _, a := range c.Args {
					switch x := a.(type) {
					case *ast.SelectorExpr:
						if id, ok := x.X.(*ast.Ident); ok && id.Name == "terminalinput" {
							origin = x.Sel.Name
						}
					case *ast.Ident:
						if x.Name == "o" {
							origin = "o"
						}
					}
				}
				if name == "send" && origin == "?" {
					return true // a send closure that takes no origin
				}
				got[fmt.Sprintf("%s\t%s\t%s\t%s", f.rel, funcName(fd), name, origin)] = true
				return true
			})
		}
	}
	var keys []string
	for k := range got {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !reviewedOrigins[k] {
			t.Errorf("an unreviewed call of a sender (add it to reviewedOrigins with the origin it should pass): %q", k)
		}
	}
	for k := range reviewedOrigins {
		if !got[k] {
			t.Errorf("reviewedOrigins lists a call that no longer exists (remove it): %q", k)
		}
	}
}

// reviewedOrigins is "file<TAB>function<TAB>sender<TAB>origin".
var reviewedOrigins = map[string]bool{
	"cmd/launch-coral/main.go	launchAgentSessions	SendKeysToTarget	Internal":                                         true,
	"internal/background/agent_exited.go	deliverAutomatedIfWanted	SendInput	Coral":                                   true,
	"internal/background/launcher.go	AgentLauncher.startPromptDelivery	SendPrompt	?":                                 true, // AgentLauncher.SendPrompt, a method of its own that hands the prompt to the automated-input gate (Coral's text)
	"internal/background/runtime_pty.go	PTYRuntime.SendEnter	SendInput	Internal":                                     true,
	"internal/background/runtime_pty.go	PTYRuntime.SendInput	SendText	o":                                             true,
	"internal/background/runtime_pty.go	PTYRuntime.SendPrompt	SendPrompt	o":                                          true,
	"internal/background/runtime_pty.go	PTYRuntime.SendRawInput	SendRawInput	o":                                      true, // only the gate's survey-dismiss 0 (survey_dismiss.go), which passes terminalinput.Internal (#6974)
	"internal/background/runtime_tmux.go	TmuxRuntime.SendEnter	SendRawKeys	Internal":                                 true,
	"internal/background/runtime_tmux.go	TmuxRuntime.SendRawInput	SendRawKeys	o":                                     true, // only the gate's survey-dismiss 0 (survey_dismiss.go), which passes terminalinput.Internal (#6974)
	"internal/background/runtime_tmux.go	TmuxRuntime.SendInput	SendKeys	o":                                           true,
	"internal/background/runtime_tmux.go	TmuxRuntime.SendPrompt	SendPrompt	o":                                        true,
	"internal/background/runtime_tmux.go	TmuxRuntime.SpawnAgent	SendKeysToTarget	Internal":                           true,
	"internal/background/workflow_steps.go	WorkflowRunner.executeAgentInteractive	SendPrompt	?":                      true, // AgentLauncher.SendPrompt, a method of its own that hands the prompt to the automated-input gate (Coral's text)
	"internal/ptymanager/automated_input_deliver.go	inputGate.pasteAndEnter	SendPrompt	Coral":                        true,
	"internal/ptymanager/manager.go	PTYBackend.SendHumanInput	SendInput	Person":                                      true,
	"internal/ptymanager/manager.go	PTYBackend.SendLine	SendLineCtx	o":                                               true,
	"internal/ptymanager/manager.go	PTYBackend.SendLineCtx	SendInput	Internal":                                       true,
	"internal/ptymanager/manager.go	PTYBackend.SendLineCtx	SendInput	o":                                              true,
	"internal/ptymanager/manager.go	PTYBackend.SendText	SendLineCtx	o":                                               true,
	"internal/ptymanager/prompt_screen.go	TmuxSessionTerminal.SendArrow	SendRawKeys	Internal":                        true,
	"internal/ptymanager/prompt_screen.go	TmuxSessionTerminal.SendEnter	SendRawKeys	Internal":                        true,
	"internal/ptymanager/session_terminal_pty.go	PTYSessionTerminal.SendArrow	SendInput	Internal":                    true,
	"internal/ptymanager/session_terminal_pty.go	PTYSessionTerminal.SendEnter	SendInput	Internal":                    true,
	"internal/ptymanager/session_terminal_pty.go	PTYSessionTerminal.SendInput	SendText	o":                            true,
	"internal/ptymanager/session_terminal_pty.go	PTYSessionTerminal.SendPrompt	SendPrompt	o":                         true,
	"internal/ptymanager/session_terminal_pty.go	PTYSessionTerminal.SendRawInput	SendInput	o":                        true,
	"internal/ptymanager/session_terminal_pty.go	PTYSessionTerminal.SendTerminalInput	SendInput	o":                   true,
	"internal/ptymanager/session_terminal_pty.go	PTYSessionTerminal.sendLaunch	SendLine	o":                           true,
	"internal/ptymanager/session_terminal_tmux.go	TmuxSessionTerminal.SendInput	SendKeys	o":                          true,
	"internal/ptymanager/session_terminal_tmux.go	TmuxSessionTerminal.SendPrompt	SendPrompt	o":                       true,
	"internal/ptymanager/session_terminal_tmux.go	TmuxSessionTerminal.SendRawInput	SendRawKeys	o":                    true,
	"internal/ptymanager/session_terminal_tmux.go	TmuxSessionTerminal.SendTerminalInput	SendTerminalInputToTarget	o": true,
	"internal/ptymanager/session_terminal_tmux.go	TmuxSessionTerminal.SendToTarget	SendKeysToTarget	o":               true,
	"internal/ptymanager/survey_dismiss.go	inputGate.dismissSurvey	SendRawInput	Internal":                            true,
	"internal/ptymanager/tmux_backend.go	TmuxBackend.Restart	SendKeysToTarget	Internal":                              true,
	"internal/ptymanager/tmux_backend.go	TmuxBackend.sendInput	SendTerminalInputToTarget	o":                          true,
	"internal/ptymanager/tmux_backend.go	TmuxBackend.Spawn	SendKeysToTarget	Internal":                                true,
	"internal/server/routes/agent_exited.go	deliverAutomatedWhile	SendInput	Coral":                                   true,
	"internal/server/routes/agent_ui_request.go	SessionsHandler.agentUIRequest	SendPrompt	Coral":                     true,
	"internal/server/routes/auto_accept.go	AutoAcceptor.check	SendRawInput	Internal":                                 true,
	"internal/server/routes/human_send.go	SessionsHandler.deliverHumanSend	SendInput	Person":                         true,
	"internal/server/routes/prompt_answer.go	SessionsHandler.AnswerPrompt	SendInput	Person":                          true, // the text typed into a prompt's field (#5715)
	"internal/server/routes/prompt_answer.go	SessionsHandler.AnswerPrompt	SendRawInput	Unclassified":                 true, // the option digits of the answer cards: choices, not text a person typed; logged as they are
	"internal/server/routes/prompt_answer.go	SessionsHandler.toggleMulti	SendRawInput	Unclassified":                  true, // the option digits of the answer cards: choices, not text a person typed; logged as they are
	"internal/server/routes/sessions_input.go	SessionsHandler.Keys	SendRawInput	o":                                   true,
	"internal/server/routes/sessions_input.go	SessionsHandler.Keys	send	Coral":                                       true, // /keys from anything but a person: through the gate
	"internal/server/routes/sessions_input.go	SessionsHandler.Keys	send	Person":                                      true, // /keys from a person (#5715)
	"internal/server/routes/sessions_input.go	SessionsHandler.Send	SendInput	Coral":                                  true,
	"internal/server/routes/sessions_lifecycle.go	sendLaunchCommand	send	Internal":                                   true,
	"internal/server/routes/websocket.go	terminalStream.handleFrame	SendInput	Person":                                true,
	"internal/tmux/send_keys.go	Client.SendTerminalInputToTarget	SendTerminalInputToTarget	o":                        true,
}
