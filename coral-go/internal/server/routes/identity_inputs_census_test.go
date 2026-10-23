package routes

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// routesSource is the routes package's non-test source, parsed, with its named struct types.
type routesSource struct {
	fset  *token.FileSet
	files map[string]*ast.File // by file name
	named map[string]*ast.StructType
}

func parseRoutesSource(t *testing.T) *routesSource {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, 0)
	require.NoError(t, err)
	pkg := pkgs["routes"]
	require.NotNil(t, pkg)
	files := map[string]*ast.File{}
	for name, f := range pkg.Files {
		if !strings.HasSuffix(name, "_test.go") {
			files[name] = f
		}
	}
	return newRoutesSource(fset, files)
}

func newRoutesSource(fset *token.FileSet, files map[string]*ast.File) *routesSource {
	src := &routesSource{fset: fset, files: files, named: map[string]*ast.StructType{}}
	for _, f := range files {
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, s := range gd.Specs {
				if ts, ok := s.(*ast.TypeSpec); ok {
					if st, ok := ts.Type.(*ast.StructType); ok {
						src.named[ts.Name.Name] = st
					}
				}
			}
		}
	}
	return src
}

// tags collects the JSON field names of a struct with the shallowest depth each is found at,
// through embedded, nested, pointer and slice fields.
func (src *routesSource) tags(st *ast.StructType, out map[string]int, depth int) {
	if depth > 6 {
		return
	}
	for _, f := range st.Fields.List {
		if f.Tag != nil {
			tag := reflect.StructTag(strings.Trim(f.Tag.Value, "`")).Get("json")
			if name, _, _ := strings.Cut(tag, ","); name != "" && name != "-" {
				if d, ok := out[name]; !ok || depth < d {
					out[name] = depth
				}
			}
		}
		var inner ast.Expr = f.Type
		for {
			switch x := inner.(type) {
			case *ast.StarExpr:
				inner = x.X
				continue
			case *ast.ArrayType:
				inner = x.Elt
				continue
			}
			break
		}
		switch x := inner.(type) {
		case *ast.StructType:
			src.tags(x, out, depth+1)
		case *ast.Ident:
			if n, ok := src.named[x.Name]; ok {
				// An embedded struct's fields are the struct's own (depth stays).
				d := depth + 1
				if len(f.Names) == 0 {
					d = depth
				}
				src.tags(n, out, d)
			}
		}
	}
}

// decodedBody is one place a handler decodes a request body.
type decodedBody struct {
	file, fn string
	fields   map[string]int // JSON field name -> shallowest depth
	opaque   string         // the target's type when it can't be audited by tags (map, any, RawMessage), else ""
}

func isRequestBodyRead(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if se, ok := n.(*ast.SelectorExpr); ok && se.Sel.Name == "Body" {
			if id, ok := se.X.(*ast.Ident); ok && (id.Name == "r" || id.Name == "req") {
				found = true
			}
		}
		return true
	})
	return found
}

func opaqueType(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.MapType:
		return "map"
	case *ast.InterfaceType:
		return "interface"
	case *ast.Ident:
		if x.Name == "any" {
			return "any"
		}
	case *ast.SelectorExpr:
		if x.Sel.Name == "RawMessage" {
			return "json.RawMessage"
		}
	}
	return ""
}

// decodedBodies finds every place a handler decodes a request body into a variable: decodeJSON(r,
// &x), json.NewDecoder(r.Body).Decode(&x), or json.Unmarshal(b, &x) (whose source is not followed).
func (src *routesSource) decodedBodies() []decodedBody {
	var out []decodedBody
	for file, f := range src.files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			vars := map[string]ast.Expr{}
			ptrs := map[string]bool{} // variables that are pointers: passed as they are, not as &x
			// A function parameter is a decode target too: decodeJSON(r, body) where body is *T.
			if fd.Type.Params != nil {
				for _, f := range fd.Type.Params.List {
					var te ast.Expr = f.Type
					isPtr := false
					if st, ok := te.(*ast.StarExpr); ok {
						te, isPtr = st.X, true
					}
					for _, id := range f.Names {
						vars[id.Name] = te
						ptrs[id.Name] = isPtr
					}
				}
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.ValueSpec:
					for _, id := range x.Names {
						if x.Type != nil {
							vars[id.Name] = x.Type
						}
					}
				case *ast.AssignStmt:
					for i, l := range x.Lhs {
						id, ok := l.(*ast.Ident)
						if !ok || i >= len(x.Rhs) {
							continue
						}
						if cl, ok := x.Rhs[i].(*ast.CompositeLit); ok && cl.Type != nil {
							vars[id.Name] = cl.Type
						}
					}
				}
				return true
			})
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) == 0 {
					return true
				}
				var fn string
				bodyRead := false
				switch c := call.Fun.(type) {
				case *ast.Ident:
					fn = c.Name
					bodyRead = fn == "decodeJSON"
				case *ast.SelectorExpr:
					fn = c.Sel.Name
					if fn == "Decode" {
						if inner, ok := c.X.(*ast.CallExpr); ok && len(inner.Args) > 0 && isRequestBodyRead(inner.Args[0]) {
							bodyRead = true
						}
					}
				}
				if fn != "decodeJSON" && fn != "Decode" && fn != "Unmarshal" {
					return true
				}
				var id *ast.Ident
				switch a := call.Args[len(call.Args)-1].(type) {
				case *ast.UnaryExpr: // &x
					if a.Op == token.AND {
						id, _ = a.X.(*ast.Ident)
					}
				case *ast.Ident: // x, a pointer parameter
					if ptrs[a.Name] {
						id = a
					}
				}
				if id == nil {
					return true
				}
				te, ok := vars[id.Name]
				if !ok {
					return true
				}
				db := decodedBody{file: file, fn: fd.Name.Name, fields: map[string]int{}}
				switch x := te.(type) {
				case *ast.StructType:
					src.tags(x, db.fields, 0)
				case *ast.Ident:
					if n, ok := src.named[x.Name]; ok {
						src.tags(n, db.fields, 0)
					}
				}
				if bodyRead {
					db.opaque = opaqueType(te)
				}
				out = append(out, db)
				return true
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].file+out[i].fn < out[j].file+out[j].fn })
	return out
}

// ---- What the identity layer's claim check does not read from the body (#4244; Security's answer to
// #4232, question 3): nested identity fields, bodies that can't be audited by their tags, the query
// string, path parameters and WebSocket messages. Each is listed with its reason, so a new one
// fails here until someone has decided how it is bound.

// nestedIdentityFields are identity keys found inside a decoded request struct below the top level
// (the identity layer reads only the top level), with why each is not a caller's claim. None today.
var nestedIdentityFields = map[string]string{
	"relaunch_fixture.go readFixture from": "relaunchfixture builds only: a version string inside the test fixture file, not a request body (#5452)",
}

func TestNoIdentityFieldIsNestedInADecodedBody(t *testing.T) {
	canon := map[string]bool{}
	for _, k := range identityKeys {
		canon[k] = true
	}
	var found []string
	for _, d := range parseRoutesSource(t).decodedBodies() {
		for name, depth := range d.fields {
			if canon[name] && depth > 0 {
				key := d.file + " " + d.fn + " " + name
				if _, ok := nestedIdentityFields[key]; !ok {
					found = append(found, key)
				}
			}
		}
	}
	sort.Strings(found)
	assert.Empty(t, found, "an identity field nested in a request body is read by no guard: flatten it, or list it in nestedIdentityFields with why it is not a claim")
}

// opaqueRequestDecodes are the handlers that decode a request body into a map, an interface or a
// json.RawMessage, which no tag scan can audit: each reads only the keys named, none an identity,
// and (where it says so) is an Operator-only route (operatorOnlyRoutes).
var opaqueRequestDecodes = map[string]string{
	"schedule.go UpdateJob":                  "reads only job fields (agent_type, base_branch, cron_expr, enabled, paused_reason, repo_path, timezone); PUT /api/scheduled/jobs/{jobID} is Operator-only",
	"system_settings.go PutSettings":         "the body is a map of Coral's setting names to values, none an identity; PUT /api/settings is Operator-only",
	"system_security.go SetDatabaseSecurity": "reads only mode; PUT /api/system/database-security is Operator-only",
	"themes.go SaveTheme":                    "reads only base, description and variables",
	"workflow.go UpdateWorkflow":             "reads only workflow fields (name, steps, default_agent, repo_path, base_branch, max_duration_s); PUT /api/workflows/{workflowID} is Operator-only",
}

func TestOpaqueRequestDecodesAreListed(t *testing.T) {
	found := map[string]string{}
	for _, d := range parseRoutesSource(t).decodedBodies() {
		if d.opaque != "" {
			found[d.file+" "+d.fn] = d.opaque
		}
	}
	for key, typ := range found {
		_, ok := opaqueRequestDecodes[key]
		assert.True(t, ok, "%s decodes a request body into a %s: no guard can read an identity from it by tag. Decode into a struct, or list it in opaqueRequestDecodes with what it reads", key, typ)
	}
	for key := range opaqueRequestDecodes {
		_, ok := found[key]
		assert.True(t, ok, "opaqueRequestDecodes lists %s, which no longer decodes a request body that way: remove it", key)
	}
}

// Where a handler reads an identity from the query string, and how that read is bound.
const (
	queryBoard   = "board request: the identity layer reads subscriber_id from the query and compares it with the token's member name (boardClaims)"
	queryTarget  = "names whom the request is about (a target), not who is calling"
	queryAgent   = "an agent route: the file binds the claim to the token (callerSession, agentFromRequest or VerifiedSession)"
	queryLookup  = "the session the request is about (a lookup or filter key); it does not say who is calling"
	queryMetrics = "labels the call in the call metrics"
	// relay-targets: the identity layer reads session_name from the query too (boardClaims) and compares it with
	// the token's session; relaySenderRole requires it to match the named subscriber's subscription.
	queryBoardSession = "board request: the identity layer compares session_name with the token's session (boardClaims), and relaySenderRole requires it to match the subscriber's subscription"
)

var queryIdentityReads = map[string]string{
	"board_messages.go subscriber_id":      queryBoard,
	"shared_images.go session_id":          queryLookup, // DELETE /api/shared-images/{id}?session_id=: the agent whose pasted image it is; the Operator calls it (#5284)
	"board_subscribers.go subscriber_id":   queryBoard,
	"board_task_create.go subscriber_id":   queryBoard,
	"board_waits.go subscriber_id":         queryBoard,
	"board_subscribers.go target":          queryTarget,
	"board_reminders.go subscriber_id":     queryTarget, // GET /api/board/{project}/reminder?subscriber_id=: whose reminder the Operator reads (#6329); an agent's token is refused on the route
	"board_remotes.go subscriber_id":       queryBoard,
	"cross_team_send.go subscriber_id":     queryBoard,
	"cross_team_send.go session_name":      queryBoardSession,
	"token_usage.go session_id":            queryLookup,
	"team_availability.go subscriber_id":   queryBoard,
	"call_metrics.go subscriber_id":        queryMetrics,
	"call_metrics.go session_id":           queryMetrics,
	"agent_tasks.go session_id":            queryAgent,
	"agent_ui.go session_id":               queryAgent,
	"artifacts.go session_id":              queryAgent,
	"sessions_tasks.go session_id":         queryAgent,
	"agent_history.go session_id":          queryLookup,
	"agent_ui_request.go session_id":       queryLookup,
	"helpers.go session_id":                queryLookup,
	"sessions_chat.go session_id":          queryLookup,
	"sessions_attachment.go session_id":    queryLookup,
	"sessions_files.go session_id":         queryLookup,
	"subagents.go session_id":              queryLookup,
	"team_artifacts.go session_id":         queryLookup,
	"websocket.go session_id":              queryLookup,
	"sessions_changes.go session_id":       queryLookup,
	"sessions_files_content.go session_id": queryLookup,
	"sessions_files_resolve.go session_id": queryLookup,
	"sessions_files_search.go session_id":  queryLookup,
	"agent_session_scope.go session_id":    queryLookup, // the session a live route is about: checked against the token's own session, lineage and board (#5013)
}

// identityQueryReads finds where the source reads one of the identity keys from the query string:
// r.URL.Query().Get("k"), r.URL.Query()["k"], and the same through a variable (q := r.URL.Query()).
func (src *routesSource) identityQueryReads() map[string]bool {
	canon := map[string]bool{}
	for _, k := range identityKeys {
		canon[k] = true
	}
	found := map[string]bool{}
	for file, f := range src.files {
		// Variables holding the query (per file, by name; a name reused for something else is read as
		// the query, which only makes this stricter).
		queryVars := map[string]bool{}
		ast.Inspect(f, func(n ast.Node) bool {
			if as, ok := n.(*ast.AssignStmt); ok && len(as.Lhs) == len(as.Rhs) {
				for i, l := range as.Lhs {
					if id, ok := l.(*ast.Ident); ok {
						if c, ok := as.Rhs[i].(*ast.CallExpr); ok {
							if se, ok := c.Fun.(*ast.SelectorExpr); ok && se.Sel.Name == "Query" {
								queryVars[id.Name] = true
							}
						}
					}
				}
			}
			return true
		})
		isQuery := func(e ast.Expr) bool { // r.URL.Query(), or a variable holding it
			if id, ok := e.(*ast.Ident); ok {
				return queryVars[id.Name]
			}
			c, ok := e.(*ast.CallExpr)
			if !ok {
				return false
			}
			se, ok := c.Fun.(*ast.SelectorExpr)
			return ok && se.Sel.Name == "Query"
		}
		ast.Inspect(f, func(n ast.Node) bool {
			var key string
			switch x := n.(type) {
			case *ast.CallExpr: // r.URL.Query().Get("k")
				if se, ok := x.Fun.(*ast.SelectorExpr); ok && se.Sel.Name == "Get" && isQuery(se.X) && len(x.Args) == 1 {
					if bl, ok := x.Args[0].(*ast.BasicLit); ok {
						key = strings.Trim(bl.Value, `"`)
					}
				}
			case *ast.IndexExpr: // r.URL.Query()["k"]
				if isQuery(x.X) {
					if bl, ok := x.Index.(*ast.BasicLit); ok {
						key = strings.Trim(bl.Value, `"`)
					}
				}
			}
			if canon[key] {
				found[file+" "+key] = true
			}
			return true
		})
	}
	return found
}

func TestIdentityQueryReadsAreClassified(t *testing.T) {
	src := parseRoutesSource(t)
	found := src.identityQueryReads()
	require.Greater(t, len(found), 10, "the census must find the handlers' query reads (found %d)", len(found))
	for key := range found {
		_, ok := queryIdentityReads[key]
		assert.True(t, ok, "%s reads an identity from the query string: say how it is bound in queryIdentityReads", key)
	}
	for key, why := range queryIdentityReads {
		if !found[key] {
			t.Errorf("queryIdentityReads lists %s, which no longer reads it: remove it", key)
			continue
		}
		if why == queryAgent {
			file, _, _ := strings.Cut(key, " ")
			b, err := os.ReadFile(file)
			require.NoError(t, err)
			text := string(b)
			assert.True(t, strings.Contains(text, "callerSession(") || strings.Contains(text, "agentFromRequest(") || strings.Contains(text, "VerifiedSession("),
				"%s is listed as bound to the token, but the file calls none of callerSession, agentFromRequest or VerifiedSession", file)
		}
	}
}

// knownPathParams are the URL parameters the handlers read. None of them is who is calling: each
// names a thing the request is about (a member named in a path is a target, as the group member
// to remove). A new one, such as {subscriberID}, fails here until it is classified.
var knownPathParams = map[string]string{
	"project":      "the board (an id or a label); the identity layer resolves it and compares it with the token's board",
	"name":         "the thing addressed by name: a live session, a theme, a team, an agent template, a workflow by name: a target",
	"sessionID":    "a session addressed (live, history, or a group member to remove): a target",
	"boardName":    "the board a team action (kill, reset, sleep status) acts on: a target",
	"agentName":    "an agent's knowledge file in a team: a target",
	"remoteServer": "a configured remote server's id",
	"folderName":   "a folder's tag set",
	"category":     "a template category",
	"ref":          "an image of the session's own transcript, by the hash of its bytes (the chat's attachment): a target inside the session addressed by {name}",
	"*":            "the rest of a path: a file inside a workflow run's step, or a remote server's own path (board remotes, the call metrics' remote pattern)",
	"series_id":    "an artifact series addressed by its id (32 hex, checked before any use) (#6357): a target; who is acting comes from the caller derivation, never from a path",
	"cid":          "a comment of an artifact addressed by its id (the artifact is {id}): a target; who is acting comes from the caller derivation, never from a path or a body",
	"id":           "an id", "taskID": "an id", "groupID": "an id", "workflowID": "an id", "runID": "an id", "jobID": "an id", "jobId": "an id",
	"tagID": "an id", "presetID": "an id", "noteID": "an id", "messageID": "an id", "subagentID": "an id", "stepIndex": "an index",
}

func TestPathParamsAreClassified(t *testing.T) {
	found := map[string]bool{}
	for _, f := range parseRoutesSource(t).files {
		ast.Inspect(f, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok || len(c.Args) != 2 {
				return true
			}
			var fn string
			switch x := c.Fun.(type) {
			case *ast.Ident:
				fn = x.Name
			case *ast.SelectorExpr:
				fn = x.Sel.Name
			}
			if fn == "urlParam" || fn == "URLParam" {
				if bl, ok := c.Args[1].(*ast.BasicLit); ok {
					found[strings.Trim(bl.Value, `"`)] = true
				}
			}
			return true
		})
	}
	require.Greater(t, len(found), 10, "the census must find the handlers' path parameters (found %d)", len(found))
	for name := range found {
		_, ok := knownPathParams[name]
		assert.True(t, ok, "a handler reads the path parameter {%s}: classify it in knownPathParams (a target, or who is calling?)", name)
	}
	for name := range knownPathParams {
		assert.True(t, found[name], "knownPathParams lists {%s}, which no handler reads: remove it", name)
	}
}

// Only the terminal WebSocket reads messages from a client, and what it reads is keystrokes, a resize and an id:
// no identity field.
func TestWebSocketClientMessagesCarryNoIdentity(t *testing.T) {
	src := parseRoutesSource(t)
	canon := map[string]bool{}
	for _, k := range identityKeys {
		canon[k] = true
	}
	readers := map[string]bool{}
	for _, f := range src.files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			reads := false
			tags := map[string]int{}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.CallExpr:
					if se, ok := x.Fun.(*ast.SelectorExpr); ok && se.Sel.Name == "Read" {
						switch c := se.X.(type) {
						case *ast.Ident: // conn.Read
							reads = reads || c.Name == "conn"
						case *ast.SelectorExpr: // a stream's own connection: t.conn.Read
							reads = reads || c.Sel.Name == "conn"
						}
					}
				case *ast.StructType:
					src.tags(x, tags, 0)
				}
				return true
			})
			if reads {
				readers[fd.Name.Name] = true
				for name := range tags {
					assert.False(t, canon[name], "%s reads client WebSocket messages and declares the identity field %q: no guard reads it", fd.Name.Name, name)
				}
			}
		}
	}
	assert.Equal(t, map[string]bool{"readFrames": true}, readers, "a new WebSocket handler reads client messages: classify what it may carry here")
	// The terminal stream's reader decodes its frames into terminalFrame (parseTerminalFrame), declared outside the
	// reader's body: its fields are checked here (#4713).
	frame := src.named["terminalFrame"]
	require.NotNil(t, frame, "the terminal stream's client frame type")
	tags := map[string]int{}
	src.tags(frame, tags, 0)
	require.NotEmpty(t, tags)
	for name := range tags {
		assert.False(t, canon[name], "the terminal stream's client frame declares the identity field %q: no guard reads it", name)
	}
}

// The census's two blind spots, closed (#4244 follow-up): a decode into a function parameter, and a
// query read through a variable. Checked on a source made for it, so the helper itself is tested.
func TestCensusSeesParameterDecodesAndQueryVariables(t *testing.T) {
	const code = `package routes

import "net/http"

type nestedBody struct {
	Items []struct {
		CreatedBy string ` + "`json:\"created_by\"`" + `
	} ` + "`json:\"items\"`" + `
}

func readBody(r *http.Request, body *nestedBody) { _ = decodeJSON(r, body) }

func readOpaque(r *http.Request, raw map[string]any) { _ = decodeJSON(r, &raw) }

func viaVariable(r *http.Request) string {
	q := r.URL.Query()
	return q.Get("subscriber_id") + q["created_by"][0]
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "synthetic.go", code, 0)
	require.NoError(t, err)
	src := newRoutesSource(fset, map[string]*ast.File{"synthetic.go": f})

	byFn := map[string]decodedBody{}
	for _, d := range src.decodedBodies() {
		byFn[d.fn] = d
	}
	require.Contains(t, byFn, "readBody", "a decode into a pointer parameter is found")
	assert.Equal(t, 1, byFn["readBody"].fields["created_by"], "and its nested identity field is seen at depth 1")
	require.Contains(t, byFn, "readOpaque", "a decode into a parameter's address is found")
	assert.Equal(t, "map", byFn["readOpaque"].opaque)

	reads := src.identityQueryReads()
	assert.True(t, reads["synthetic.go subscriber_id"], "a Get through a variable holding the query is found")
	assert.True(t, reads["synthetic.go created_by"], "and an index through it")
}
