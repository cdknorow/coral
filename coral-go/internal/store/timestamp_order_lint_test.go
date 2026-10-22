package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The bug class behind TestArtifactCommentsEditResolveAndTombstone: a time stored as RFC3339Nano text does not sort as a time. The format drops trailing zeros (".12Z" is 120 ms, ".123Z" is 123 ms) and
// the fraction altogether on a whole second, so two rows written within a second can compare the wrong way round, in an ORDER BY, a MAX or MIN, or a < or >.
//
// Two rules keep a new one from being written:
//  1. Every non-test file that writes a time with Format(time.RFC3339Nano) is listed below, with its count: a new writer fails here until someone has read the list of columns below and said what
//     its column is used for.
//  2. No SQL in a file that mentions one of those tables may ORDER BY (first key), MAX or MIN one of those columns. Order by id (write order) or rowid, or store a fixed-width stamp (ISOFormat).
//     A comparison against a cutoff (created_at < ?) is allowed: it is exact to the second, which is what a retention cutoff needs.

// nanoWriter is a file that writes a time as RFC3339Nano text: how many Format calls, the tables whose columns it fills (each must be in nanoColumns, which is what the order rule checks), or, for a
// writer whose text is never stored in a column, why that is all right.
type nanoWriter struct {
	calls  int
	tables []string
	why    string
}

// nanoWriters: file (under internal/) -> what it writes.
var nanoWriters = map[string]nanoWriter{
	"store/artifact_comments.go":   {calls: 1, tables: []string{"artifact_comments"}}, // created_at, edited_at, resolved_at, deleted_at (listed by id)
	"store/saved_messages.go":      {calls: 2, tables: []string{"saved_messages"}},    // created_at, done_at (listed by id)
	"store/sessions_tags.go":       {calls: 1, tables: []string{"pinned_folders"}},    // pinned_at (listed by rowid)
	"store/ui_requests.go":         {calls: 3, tables: []string{"agent_ui_requests"}}, // created_at, updated_at (only a retention cutoff compares them)
	"store/call_metrics.go":        {calls: 4, tables: []string{"call_metrics"}},      // created_at (hour-prefix ranges and a prune cutoff)
	"store/cloud_enrollment.go":    {calls: 1, tables: []string{"cloud_enrollment"}},  // parsed to time.Time, never compared as text
	"tracking/milestones.go":       {calls: 1, why: "a JSON file, parsed"},
	"server/routes/token_usage.go": {calls: 1, why: "a response field, not stored"},
	"background/health_held.go":    {calls: 1, why: "part of an episode id, not compared"},
}

// nanoTables and their time columns that hold such text.
var nanoColumns = map[string][]string{
	"artifact_comments": {"created_at", "edited_at", "resolved_at", "deleted_at"},
	"saved_messages":    {"created_at", "done_at"},
	"pinned_folders":    {"pinned_at"},
	"agent_ui_requests": {"created_at", "updated_at"},
	"call_metrics":      {"created_at"},
	"cloud_enrollment":  {"expires_at", "next_start_at"},
}

// nanoOrderAllowed: SQL that does order by one of these columns and why that is all right. Keyed by file and a distinctive piece of the SQL.
var nanoOrderAllowed = map[string]string{
	"store/call_metrics.go: ORDER BY created_at LIMIT": "the oldest rows are pruned first, in batches, below a cutoff: two rows in one second swapping places changes which of them goes in this batch, never what is pruned",
}

func goFilesUnderInternal(t *testing.T) map[string]*ast.File {
	t.Helper()
	fset := token.NewFileSet()
	out := map[string]*ast.File{}
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		out[filepath.ToSlash(strings.TrimPrefix(path, "../"))] = f
		return nil
	})
	require.NoError(t, err)
	return out
}

func stringLiterals(f *ast.File) []string {
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			if s, err := strconv.Unquote(lit.Value); err == nil {
				out = append(out, s)
			}
		}
		return true
	})
	return out
}

// nanoLayout is time.RFC3339Nano's text, for a layout written out.
const nanoLayout = "2006-01-02T15:04:05.999999999Z07:00"

func isNanoLayout(e ast.Expr, held map[string]bool) bool {
	switch x := e.(type) {
	case *ast.SelectorExpr:
		return x.Sel.Name == "RFC3339Nano"
	case *ast.BasicLit:
		s, err := strconv.Unquote(x.Value)
		return x.Kind == token.STRING && err == nil && s == nanoLayout
	case *ast.Ident:
		return held[x.Name]
	case *ast.ParenExpr:
		return isNanoLayout(x.X, held)
	}
	return false
}

func TestEveryRFC3339NanoWriterIsListed(t *testing.T) {
	found := map[string]int{}
	for name, f := range goFilesUnderInternal(t) {
		// a layout held in a name (const layout = time.RFC3339Nano, or the layout written out) is the same layout when it reaches Format
		held := map[string]bool{}
		for changed := true; changed; {
			changed = false
			ast.Inspect(f, func(n ast.Node) bool {
				if vs, ok := n.(*ast.ValueSpec); ok {
					for i, v := range vs.Values {
						if i < len(vs.Names) && !held[vs.Names[i].Name] && isNanoLayout(v, held) {
							held[vs.Names[i].Name] = true
							changed = true
						}
					}
				}
				if as, ok := n.(*ast.AssignStmt); ok && len(as.Lhs) == len(as.Rhs) {
					for i, v := range as.Rhs {
						if id, ok := as.Lhs[i].(*ast.Ident); ok && !held[id.Name] && isNanoLayout(v, held) {
							held[id.Name] = true
							changed = true
						}
					}
				}
				return true
			})
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || len(call.Args) != 1 || (sel.Sel.Name != "Format" && sel.Sel.Name != "AppendFormat") {
				return true
			}
			if isNanoLayout(call.Args[0], held) {
				found[name]++
			}
			return true
		})
	}
	calls := map[string]int{}
	for file, w := range nanoWriters {
		calls[file] = w.calls
	}
	assert.Equal(t, calls, found, "a time written as RFC3339Nano text does not sort as a time: list the new writer in nanoWriters with its tables (and those tables' columns in nanoColumns), and make sure no list is ordered by it (use id), or write a fixed-width stamp (ISOFormat)")
}

// The two lists cannot drift: a writer names the tables it fills, each must have its columns in nanoColumns (so the order rule looks at them), a writer with no table says why, and no table in
// nanoColumns is left without a writer.
func TestEveryNanoWriterNamesItsColumns(t *testing.T) {
	claimed := map[string]bool{}
	for file, w := range nanoWriters {
		if len(w.tables) == 0 {
			assert.NotEmpty(t, w.why, "%s writes RFC3339Nano text into no listed table: say why that is all right", file)
		}
		for _, table := range w.tables {
			claimed[table] = true
			assert.NotEmpty(t, nanoColumns[table], "%s fills %s, which has no columns in nanoColumns: the order rule would not look at them", file, table)
		}
	}
	for table := range nanoColumns {
		assert.True(t, claimed[table], "nanoColumns lists %s but no writer in nanoWriters names it", table)
	}
}

func TestNoSQLOrdersByATimeStoredAsRFC3339Nano(t *testing.T) {
	cols := map[string]bool{}
	for _, cs := range nanoColumns {
		for _, c := range cs {
			cols[c] = true
		}
	}
	var names []string
	for c := range cols {
		names = append(names, regexp.QuoteMeta(c))
	}
	sort.Strings(names)
	colRE := `(?:\w+\.)?(?:` + strings.Join(names, "|") + `)\b`
	orderRE := regexp.MustCompile(`(?i)\bORDER BY\s+` + colRE)
	aggRE := regexp.MustCompile(`(?i)\b(?:MAX|MIN)\(\s*` + colRE)

	var bad []string
	for name, f := range goFilesUnderInternal(t) {
		lits := stringLiterals(f)
		mentions := false
		for _, l := range lits {
			for table := range nanoColumns {
				if strings.Contains(l, table) {
					mentions = true
				}
			}
		}
		if !mentions {
			continue
		}
		for _, l := range lits {
			if !orderRE.MatchString(l) && !aggRE.MatchString(l) {
				continue
			}
			allowed := false
			for key := range nanoOrderAllowed {
				if file, snippet, ok := strings.Cut(key, ": "); ok && file == name && strings.Contains(l, snippet) {
					allowed = true
				}
			}
			if !allowed {
				bad = append(bad, name+": "+strings.TrimSpace(l))
			}
		}
	}
	sort.Strings(bad)
	assert.Empty(t, bad, "these order by, or take the MAX or MIN of, a time stored as RFC3339Nano text, which does not sort as a time within a second: order by id (or rowid), or store a fixed-width stamp")
}

// A list ordered by a time and nothing else comes back in an arbitrary order for rows with the same time (the board's times are to the second, the store's to the microsecond): two runs can
// differ. The first key being a time, a later key must be id or rowid, which no two rows share.
var timeKeyRE = regexp.MustCompile(`(?i)\w*(?:_at|timestamp)\b`)
var uniqueKeyRE = regexp.MustCompile(`(?i)^(?:\w+\.)?(?:id|rowid)\b`)

// timeOrderAllowed: file: the ORDER BY clause, and why it needs no tie-break.
var timeOrderAllowed = map[string]string{
	"board/store_groups_projects.go: last_message_at": "p.project follows: the derived table lists each project once, and a project is its own key",
	"store/token_usage.go: MAX(t.recorded_at)":        "t.session_id follows: the rows are grouped by it, so each is one row's key",
	"store/call_metrics.go: created_at":               "the prune of the oldest rows below a cutoff: which of two rows in one second goes in this batch changes nothing that is pruned",
	"store/agent_event_query.go: created_at ":         "the id key follows in the next piece of the statement (dir is the same for both)",
}

// orderKeys splits an ORDER BY clause (the text after the words) into its keys at the commas outside brackets, up to a LIMIT, the end of a sub-select or the end of the text.
func orderKeys(s string) []string {
	var keys []string
	depth, start := 0, 0
	end := len(s)
scan:
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			if depth == 0 {
				end = i
				break scan
			}
			depth--
		case ',':
			if depth == 0 {
				keys = append(keys, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		case ';':
			end = i
			break scan
		}
		if depth == 0 && i+5 <= len(s) && strings.EqualFold(s[i:i+5], "LIMIT") && (i == 0 || s[i-1] == ' ' || s[i-1] == '\n' || s[i-1] == '\t') {
			end = i
			break
		}
	}
	return append(keys, strings.TrimSpace(s[start:end]))
}

func TestEveryOrderByATimeHasAUniqueTieBreak(t *testing.T) {
	orderRE := regexp.MustCompile(`(?i)\bORDER BY\s`)
	var bad []string
	for name, f := range goFilesUnderInternal(t) {
		for _, l := range stringLiterals(f) {
			for _, at := range orderRE.FindAllStringIndex(l, -1) {
				keys := orderKeys(l[at[1]:])
				if !timeKeyRE.MatchString(keys[0]) {
					continue
				}
				unique := false
				for _, k := range keys[1:] {
					if uniqueKeyRE.MatchString(k) {
						unique = true
					}
				}
				first := strings.Join(strings.Fields(keys[0]), " ")
				allowed := false
				for key := range timeOrderAllowed {
					if file, clause, ok := strings.Cut(key, ": "); ok && file == name && strings.HasPrefix(first, strings.TrimSpace(clause)) {
						allowed = true
					}
				}
				if !unique && !allowed {
					bad = append(bad, name+": ORDER BY "+strings.Join(strings.Fields(strings.Join(keys, ", ")), " "))
				}
			}
		}
	}
	sort.Strings(bad)
	assert.Empty(t, bad, "these order by a time with no id or rowid after it: rows with the same time come back in an arbitrary order. Add ', id' (or rowid)")
}
