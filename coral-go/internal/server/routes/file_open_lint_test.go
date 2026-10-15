package routes

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// #3596 (Security's point 5): a route that opens, reads or writes a file by path does it through
// internal/safefs, or it is one of the sites below, each reviewed, with the reason its path can't
// be steered by a request, a hook payload, board text, a transcript or a repository. A new
// os.Open / OpenFile / ReadFile / WriteFile / Create in this package fails until it moves to
// safefs or is reviewed and listed here; a listed site that no longer exists fails too, so the
// list stays true. Keyed file:function:call, never by line.
var reviewedFileOpens = map[string]string{
	"artifact_prune.go:SessionsHandler.shrinkDiffs:ReadFile":      "server data folder; object names match objectNameRe",
	"artifact_prune.go:recordTime:ReadFile":                       "server data folder (a sidecar the server wrote)",
	"artifact_prune.go:scanKnowledgeReferences:Open":              "server data folder (agent_knowledge), regular files only",
	"artifacts.go:SessionsHandler.getAgentArtifact:ReadFile":      "{id} is 64 lower-case hex (checked), in the server's objects folder",
	"artifacts.go:SessionsHandler.uploadAgentArtifact:ReadFile":   "the server's own temp file and hex digest",
	"artifacts.go:SessionsHandler.uploadAgentArtifact:WriteFile":  "uploads/<uploadsDir(session)>/<server hex>.json",
	"artifacts.go:createSidecarExclusive:OpenFile":                "O_EXCL in the server's objects folder, server hex name",
	"artifacts.go:readHead:Open":                                  "an object named by its server-computed hex",
	"artifacts.go:readSidecar:ReadFile":                           "an object's sidecar, hex name",
	"auto_attach.go:NewAutoAttacher:ReadFile":                     "server data folder (auto-attach-bytes)",
	"auto_attach.go:readAttachable:OpenFile":                      "auto-attach: realPath + within its roots + O_NOFOLLOW + SameFile (W8/W9 move it to safefs, later)",
	"board.go:BoardHandler.TaskChangesDiff:ReadFile":              "the task id is an int64; the server's artifacts folder",
	"changes_diff_filter.go:isBinaryFile:Open":                    "an untracked name git lists, under diffPathHidden and an Lstat regular-file check",
	"file_explorer.go:serveExplorerDirectory:Open":                "the explorer: no abs or '..', EvalSymlinks + Rel inside the root, strict roots",
	"sessions.go:SessionsHandler.Diff:ReadFile":                   "isPathWithinDir on the resolved path; strict callers get diffPathRefused",
	"sessions.go:SessionsHandler.GetFileContent:Open":             "isPathWithinDir; strict callers: withinAny(sessionFileRoots) + rootAllowed + sensitivePath",
	"sessions.go:SessionsHandler.Restart:WriteFile":               "a log file named by a server-generated session UUID",
	"sessions.go:SessionsHandler.Resume:WriteFile":                "a log file named by a server-generated session UUID",
	"sessions.go:SessionsHandler.SessionChanges:ReadFile":         "the session id is safefs.IsSafeID (W4)",
	"sessions.go:SessionsHandler.SessionChangesArtifact:ReadFile": "the session id is safefs.IsSafeID (W4)",
	"sessions.go:SessionsHandler.launchSession:WriteFile":         "a log file named by a server-generated session UUID",
	"sessions.go:SessionsHandler.wakeExistingSession:WriteFile":   "a log file named by a server-generated session UUID",
	"sessions.go:getLogStatus:Open":                               "a log named from a tmux session matching UUIDSessionRE",
	"sessions.go:writeBoardStateFile:WriteFile":                   "server data folder, board_state_<Coral's session name>.json, 0600",
	"system.go:runTeamGeneration:WriteFile":                       "server data folder, a fixed name",
	"system_bounds.go:readImportFile:OpenFile":                    "#3583: bounded import (O_NOFOLLOW, regular file, size cap)",
	"team_artifacts.go:enrichTeamArtifact:ReadFile":               "a managed artifact's hex name, lower-cased and checked",
	"team_artifacts.go:uploadDirCache.list:ReadFile":              "names read from the server's uploads folder",
	"themes.go:ThemesHandler.GetTheme:ReadFile":                   "the theme name keeps only [A-Za-z0-9-_ ] (safeNameRE)",
	"themes.go:ThemesHandler.ImportTheme:WriteFile":               "safeNameRE",
	"themes.go:ThemesHandler.ListThemes:ReadFile":                 "names read from the server's themes folder",
	"themes.go:ThemesHandler.SaveTheme:WriteFile":                 "safeNameRE",
	"themes.go:ThemesHandler.SeedBundledThemes:ReadFile":          "the embedded bundled themes",
	"themes.go:ThemesHandler.SeedBundledThemes:WriteFile":         "the bundled theme names, into the server's themes folder",
	"uploads.go:UploadFile:WriteFile":                             "a random hex prefix + filepath.Base of the name, an extension allowlist",
	"workflow.go:stepFileType:Open":                               "a workflow step file: Clean + prefix check, realPath + within, sensitivePath",
}

func TestFileOpensInRoutesAreReviewed(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		af, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range af.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			name := fd.Name.Name
			if fd.Recv != nil && len(fd.Recv.List) > 0 {
				typ := fd.Recv.List[0].Type
				if st, ok := typ.(*ast.StarExpr); ok {
					typ = st.X
				}
				if id, ok := typ.(*ast.Ident); ok {
					name = id.Name + "." + name
				}
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "os" {
					switch sel.Sel.Name {
					case "Open", "OpenFile", "ReadFile", "WriteFile", "Create":
						found[f+":"+name+":"+sel.Sel.Name] = true
					}
				}
				return true
			})
		}
	}
	var unreviewed, stale []string
	for k := range found {
		if _, ok := reviewedFileOpens[k]; !ok {
			unreviewed = append(unreviewed, k)
		}
	}
	for k := range reviewedFileOpens {
		if !found[k] {
			stale = append(stale, k)
		}
	}
	sort.Strings(unreviewed)
	sort.Strings(stale)
	if len(unreviewed) > 0 {
		t.Errorf("file opened by path outside internal/safefs and not reviewed (use safefs, or review it and add it to reviewedFileOpens with the reason):\n%s", strings.Join(unreviewed, "\n"))
	}
	if len(stale) > 0 {
		t.Errorf("reviewedFileOpens lists sites that no longer exist (remove them):\n%s", strings.Join(stale, "\n"))
	}
}
