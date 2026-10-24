package startup

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

// #6792 (the #6767 class): every place that builds agent.LaunchParams for an agent it starts passes Coral's own address, data folder and the session's name and run tag, which agent.CoralEnv turns
// into CORAL_URL, CORAL_PORT, CORAL_DIR, CORAL_SESSION_NAME and CORAL_PTY_RUN. A path that forgets them starts agents whose hooks and coral-board dial :8420 (#6767: the scheduled launch did). This is
// an AST census over the product's packages: a new LaunchParams literal fails it until it is added to the table below with what it passes, and an entry that drops one of the fields fails it.
var launchEnvFields = []string{"SessionName", "CoralHost", "CoralPort", "CoralDir", "RunTag"}

// launchSites: file (under internal/) + "." + the enclosing function -> why, and the fields it must set. A nil list is a literal that starts no agent (see why).
var launchSites = map[string]struct {
	why    string
	fields []string
}{
	"server/routes/sessions_launch_session.go.launchSession": {"a dashboard launch, and a team launch's members", launchEnvFields},
	"server/routes/sessions_lifecycle.go.restartCommand":     {"a restart", launchEnvFields},
	"server/routes/sessions_lifecycle.go.resumeCommand":      {"a resume", launchEnvFields},
	"server/routes/sessions_sleep.go.wakeExistingSession":    {"a wake after sleep", launchEnvFields},
	"background/launcher.go.LaunchAgent":                     {"a scheduled or one-shot run", launchEnvFields},
	// A workflow step does not start an agent session: this literal only builds the step's settings file (PrintArgs); its process environment is buildStepEnv's, checked below.
	"background/workflow_steps.go.executeAgentPrint": {"a workflow step's settings (PrintArgs), not a launch", nil},
}

func TestEveryLaunchPathPassesCoralsAddress(t *testing.T) {
	fset := token.NewFileSet()
	seen := map[string]bool{}
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil
		}
		rel := filepath.ToSlash(strings.TrimPrefix(filepath.ToSlash(path), "../"))
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				cl, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				if se, ok := cl.Type.(*ast.SelectorExpr); !ok || se.Sel.Name != "LaunchParams" {
					return true
				}
				key := rel + "." + fn.Name.Name
				site, known := launchSites[key]
				if !known {
					t.Errorf("%s builds agent.LaunchParams and is not in the launch census: if it starts an agent it must pass %v; add it to launchSites with what it passes", key, launchEnvFields)
					return true
				}
				seen[key] = true
				set := map[string]bool{}
				for _, el := range cl.Elts {
					if kv, ok := el.(*ast.KeyValueExpr); ok {
						if id, ok := kv.Key.(*ast.Ident); ok {
							set[id.Name] = true
						}
					}
				}
				for _, want := range site.fields {
					if !set[want] {
						t.Errorf("%s (%s) does not set %s: its agents would not know this server's %s", key, site.why, want, want)
					}
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var stale []string
	for key := range launchSites {
		if !seen[key] {
			stale = append(stale, key)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("launch census entries no code builds any more (renamed or removed): %v", stale)
	}
}

// A workflow step's agent runs with buildStepEnv's variables: the census above exempts its LaunchParams (settings only), so what it does pass is checked here.
func TestWorkflowStepsExportCoralsAddress(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "../background/workflow_templates.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var fn *ast.FuncDecl
	for _, d := range f.Decls {
		if x, ok := d.(*ast.FuncDecl); ok && x.Name.Name == "buildStepEnv" {
			fn = x
		}
	}
	if fn == nil {
		t.Fatal("buildStepEnv is gone: the workflow step census looks in the wrong place")
	}
	found := map[string]bool{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			for _, v := range []string{"CORAL_PORT=", "CORAL_HOST=", "CORAL_URL=", "CORAL_DIR=", "CORAL_DATA_DIR="} {
				if strings.Contains(lit.Value, v) {
					found[v] = true
				}
			}
		}
		return true
	})
	for _, v := range []string{"CORAL_PORT=", "CORAL_HOST=", "CORAL_URL=", "CORAL_DIR=", "CORAL_DATA_DIR="} {
		if !found[v] {
			t.Errorf("buildStepEnv does not export %s: a workflow step's agent would dial the default port", strings.TrimSuffix(v, "="))
		}
	}
}
