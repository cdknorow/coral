package routes

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// identityWords are the words that make a JSON field name look like it names a participant.
var identityWords = map[string]bool{"subscriber": true, "session": true, "sender": true, "creator": true, "created": true, "assign": true,
	"assigned": true, "assignee": true, "reviewer": true, "reviewers": true, "approver": true, "owner": true, "author": true, "actor": true,
	"from": true, "target": true, "as": true, "job": true, "agent": true, "member": true, "members": true, "user": true, "by": true,
	"claimed": true, "released": true, "completed": true, "requester": true, "to": true}

// notIdentityFields are decoded fields that look like an identity but do not say who a request is
// from or for: each with why. A new one needs a line here, or to be added to identityKeys.
var notIdentityFields = map[string]string{
	"agent_type":         "which CLI to launch, not a participant",
	"agents":             "a list of launch specs for a team launch, validated as names by the launcher",
	"reviewers":          "task add --reviewers: names the Operator or orchestrator chose, checked against the board's members when delegated (#4175 rule 13)",
	"to":                 "who a message is for (a mention list): a target, not who it is from",
	"to_address":         "a cross-team address",
	"from_board":         "a board id on a cross-team send; the sender is subscriber_id",
	"target_message_id":  "a message id",
	"target_group_id":    "a group id",
	"target_index":       "an index",
	"target_id":          "a wait target: who or what to wait for, not who is waiting",
	"session":            "a session name addressed by a dashboard action (the Operator's routes)",
	"user":               "a Hub user label set by the tunnel, not by the body",
	"agent":              "a workflow step's agent type",
	"default_agent":      "a workflow's default agent type",
	"blocked_by":         "task ids",
	"completed":          "a flag",
	"created_at":         "a time",
	"current_session_id": "the session a launch replaces, checked by the launcher against the live session",
	"resume_session_id":  "the conversation to resume, not a board participant",
	"from_id":            "a message id",
	"reply_to":           "a message id",
	"job_type":           "what a scheduled job runs",
	"by":                 "a counter label",
}

func TestEveryDecodedIdentityFieldIsCanonicalised(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, 0)
	require.NoError(t, err)
	pkg := pkgs["routes"]
	require.NotNil(t, pkg)

	named := map[string]*ast.StructType{}
	var files []*ast.File
	for name, f := range pkg.Files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		files = append(files, f)
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, s := range gd.Specs {
				if ts, ok := s.(*ast.TypeSpec); ok {
					if st, ok := ts.Type.(*ast.StructType); ok {
						named[ts.Name.Name] = st
					}
				}
			}
		}
	}

	// tags collects the JSON field names of a struct, through embedded and nested structs.
	var tags func(st *ast.StructType, out map[string]bool, depth int)
	tags = func(st *ast.StructType, out map[string]bool, depth int) {
		if depth > 6 {
			return
		}
		for _, f := range st.Fields.List {
			if f.Tag != nil {
				tag := reflect.StructTag(strings.Trim(f.Tag.Value, "`")).Get("json")
				if name, _, _ := strings.Cut(tag, ","); name != "" && name != "-" {
					out[name] = true
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
				tags(x, out, depth+1)
			case *ast.Ident:
				if n, ok := named[x.Name]; ok {
					tags(n, out, depth+1)
				}
			}
		}
	}

	decoded := map[string]string{} // json field name -> where
	for _, f := range files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			vars := map[string]ast.Expr{} // local var -> its type expression
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
				switch c := call.Fun.(type) {
				case *ast.Ident:
					fn = c.Name
				case *ast.SelectorExpr:
					fn = c.Sel.Name
				}
				if fn != "decodeJSON" && fn != "Decode" && fn != "Unmarshal" {
					return true
				}
				u, ok := call.Args[len(call.Args)-1].(*ast.UnaryExpr)
				if !ok || u.Op != token.AND {
					return true
				}
				id, ok := u.X.(*ast.Ident)
				if !ok {
					return true
				}
				te, ok := vars[id.Name]
				if !ok {
					return true
				}
				out := map[string]bool{}
				switch x := te.(type) {
				case *ast.StructType:
					tags(x, out, 0)
				case *ast.Ident:
					if n, ok := named[x.Name]; ok {
						tags(n, out, 0)
					}
				}
				for name := range out {
					decoded[name] = fd.Name.Name
				}
				return true
			})
		}
	}
	require.Greater(t, len(decoded), 40, "the census must find the handlers' request fields (found %d)", len(decoded))
	require.Contains(t, decoded, "subscriber_id")

	canon := map[string]bool{}
	for _, k := range identityKeys {
		canon[k] = true
	}
	var missing []string
	for name, where := range decoded {
		looks := false
		for _, w := range strings.Split(strings.ToLower(name), "_") {
			if identityWords[w] {
				looks = true
			}
		}
		if !looks || canon[name] {
			continue
		}
		if _, ok := notIdentityFields[name]; ok {
			continue
		}
		missing = append(missing, name+" (decoded in "+where+")")
	}
	sort.Strings(missing)
	assert.Empty(t, missing, "a request field named like a participant is neither canonicalised (identityKeys) nor listed as not an identity (notIdentityFields)")
}
