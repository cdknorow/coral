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
	"relaunch_fixture.go:readFixture:OpenNoFollow":                             "relaunchfixture builds only: the one fixture file in the owner-only folder named by CORAL_RELAUNCH_FIXTURE_DIR, Lstat-checked regular 0600 own file, 16 KiB cap (#5452)",
	"system_update_relaunch.go:UpdateRelaunchHandler.readPrivate:OpenNoFollow": "<data dir>/updates/ack.key and ack_used.json: fixed names, Lstat-checked plain files only this user can read (#5280)",
	"artifact_prune.go:SessionsHandler.shrinkDiffs:ReadFile":                   "server data folder; object names match objectNameRe",
	"artifact_prune.go:recordTime:ReadFile":                                    "server data folder (a sidecar the server wrote)",
	"artifact_prune.go:scanKnowledgeReferences:Open":                           "server data folder (agent_knowledge), regular files only",
	"artifacts.go:SessionsHandler.openArtifactForRead:ReadFile":                "{id} is 64 lower-case hex (checked), in the server's objects folder",
	"artifacts.go:SessionsHandler.openArtifactForRead:OpenNoFollow":            "the same object, opened once without following a link to serve it and stat its size (#4443)",
	"artifacts.go:SessionsHandler.uploadAgentArtifact:ReadFile":                "the server's own temp file and hex digest",
	"artifacts.go:SessionsHandler.uploadAgentArtifact:WriteFile":               "uploads/<uploadsDir(session)>/<server hex>.json",
	"artifacts.go:createSidecarExclusive:OpenFile":                             "O_EXCL in the server's objects folder, server hex name",
	"artifacts.go:readHead:Open":                                               "an object named by its server-computed hex",
	"artifacts.go:readSidecar:ReadFile":                                        "an object's sidecar, hex name",
	"auto_attach.go:NewAutoAttacher:ReadFile":                                  "server data folder (auto-attach-bytes)",
	"board_task_read.go:BoardHandler.TaskChangesDiff:ReadFile":                 "the task id is an int64; the server's artifacts folder",
	"changes_diff_filter.go:diffWithNewFiles:ReadFile":                         "the working tree's own index, at the path git rev-parse --git-path index gives (git reads the same file on every command); copied only into a private temp folder, never served (#243)",
	"changes_diff_filter.go:diffWithNewFiles:WriteFile":                        "the index copy in this call's own os.MkdirTemp folder, 0600, removed when it returns (#243)",
	"changes_diff_filter.go:isBinaryFile:Open":                                 "an untracked name git lists, under diffPathHidden and an Lstat regular-file check",
	"file_explorer.go:serveExplorerDirectory:Open":                             "the explorer: no abs or '..', EvalSymlinks + Rel inside the root, strict roots",
	"sessions_changes.go:SessionsHandler.Diff:ReadFile":                        "isPathWithinDir on the resolved path; strict callers get diffPathRefused",
	"sessions_changes.go:SessionsHandler.SessionChanges:ReadFile":              "the session id is safefs.IsSafeID (W4)",
	"sessions_changes.go:SessionsHandler.SessionChangesArtifact:ReadFile":      "the session id is safefs.IsSafeID (W4)",
	"log_tail.go:readLogTail:Open":                                             "a log named from a tmux session matching UUIDSessionRE",
	"sessions_launch_board.go:writeBoardStateFile:WriteFile":                   "server data folder, board_state_<Coral's session name>.json, 0600",
	"system_team_import.go:runTeamGeneration:WriteFile":                        "server data folder, a fixed name",
	"system_bounds.go:readImportFile:OpenNoFollow":                             "#3583: bounded import (no link followed, regular file, size cap)",
	"team_artifacts.go:enrichTeamArtifact:ReadFile":                            "a managed artifact's hex name, lower-cased and checked",
	"artifact_comments.go:readSmallFile:Open":                                  "an artifact's sidecar by its 64-hex name (checked lower-case hex before the call), read to a byte cap",
	"team_artifacts.go:uploadDirCache.list:ReadFile":                           "names read from the server's uploads folder",
	"task_artifact_vet.go:BoardHandler.objectSidecarDigest:ReadFile":           "a managed artifact's 64-hex id (managedArtifactID), Lstat'ed as a regular file under the bound first (#4379)",
	"cross_team_attach.go:BoardHandler.crossTeamRecord:ReadFile":               "a 64-hex id under <data>/artifacts/<kind>/<hash of the board>, Lstat'ed as a regular file under the bound first (#4379)",
	"cross_team_pending.go:readPending:ReadFile":                               "a file in <data>/artifacts/pending/<hash of the board>, listed from that folder, Lstat'ed as a regular file under the bound first (#4440)",
	"cross_team_attach.go:BoardHandler.objectMeta:ReadFile":                    "a managed artifact's 64-hex id (managedArtifactID), Lstat'ed as a regular file under the bound first (#4379)",
	"cross_team_attach.go:BoardHandler.writeCrossTeamRecord:OpenFile":          "the received row's fallback where the data folder can't hard-link: the final name is created O_EXCL (never follows a link at it, never replaces), under a bound folder named by a hash, the id a 64-hex string (#4379, Security's C1 INFO 1)",
	"cross_team_attach.go:BoardHandler.readAttachment:OpenNoFollow":            "the attach read: a 64-hex id in the object store, opened once no-follow and checked through the handle (#4379, Security's one-step rule)",
	"cross_team_attach_relay.go:BoardHandler.loadAttachment:OpenNoFollow":      "an object named by its 64-hex id in the server's objects folder, opened once without following a link, checked through the handle and re-checked by name after the read",
	"received_cap.go:receivedUsage:ReadFile":                                   "<data>/artifacts/received/<hash of a board>/<64-hex id>.json: the file name must be 64 hex and .json (managedArtifactID), Lstat'ed as a regular file under the bound first, nothing from a request (#4441)",
	"themes.go:ThemesHandler.GetTheme:ReadFile":                                "the theme name keeps only [A-Za-z0-9-_ ] (safeNameRE)",
	"themes.go:ThemesHandler.ImportTheme:WriteFile":                            "safeNameRE",
	"themes.go:ThemesHandler.ListThemes:ReadFile":                              "names read from the server's themes folder",
	"themes.go:ThemesHandler.SaveTheme:WriteFile":                              "safeNameRE",
	"themes.go:ThemesHandler.SeedBundledThemes:ReadFile":                       "the embedded bundled themes",
	"themes.go:ThemesHandler.SeedBundledThemes:WriteFile":                      "the bundled theme names, into the server's themes folder",
	"uploads.go:UploadFile:WriteFile":                                          "a random hex prefix + filepath.Base of the name, an extension allowlist",
	"workflow.go:stepFileType:Open":                                            "a workflow step file: Clean + prefix check, realPath + within, sensitivePath",
	"shared_images.go:readUploadRecord:ReadFile":                               "uploads/<uploadsDir(session): a safe id>/<64 lower-case hex>.json, a record the server wrote (#5284)",
	"artifacts_list_all.go:listAllArtifactRows:ReadFile":                       "the machine-wide list (#5575): objects/<64 lower-case hex>.json sidecars (size-bounded) and artifacts/received/<32-hex board>/<64-hex>.json rows the server wrote; every name is checked against objectNameRe and boardDirName",
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
				// secfile.OpenNoFollow follows no link (#3920), but its path can still be steered,
				// so it is reviewed like os.OpenFile.
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "secfile" && sel.Sel.Name == "OpenNoFollow" {
					found[f+":"+name+":"+sel.Sel.Name] = true
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
