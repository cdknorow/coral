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

// nanoWriters: file (under internal/) -> the number of Format(time.RFC3339Nano) calls, with where they go.
var nanoWriters = map[string]int{
	"store/artifact_comments.go":   1, // artifact_comments.created_at, edited_at, resolved_at, deleted_at (listed by id)
	"store/saved_messages.go":      2, // saved_messages.created_at, done_at (listed by id)
	"store/sessions_tags.go":       1, // pinned_folders.pinned_at (listed by rowid)
	"store/ui_requests.go":         3, // agent_ui_requests.created_at, updated_at (only a retention cutoff compares them)
	"store/call_metrics.go":        4, // call_metrics.created_at (hour-prefix ranges and a prune cutoff)
	"store/cloud_enrollment.go":    1, // cloud_enrollment times, parsed to time.Time, never compared as text
	"tracking/milestones.go":       1, // a JSON file, parsed
	"server/routes/token_usage.go": 1, // a response field, not stored
	"background/health_held.go":    1, // part of an episode id, not compared
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

func TestEveryRFC3339NanoWriterIsListed(t *testing.T) {
	found := map[string]int{}
	for name, f := range goFilesUnderInternal(t) {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Format" || len(call.Args) != 1 {
				return true
			}
			if arg, ok := call.Args[0].(*ast.SelectorExpr); ok && arg.Sel.Name == "RFC3339Nano" {
				found[name]++
			}
			return true
		})
	}
	assert.Equal(t, nanoWriters, found, "a time written as RFC3339Nano text does not sort as a time: list the new writer in nanoWriters with its column, and make sure no list is ordered by it (use id), or write a fixed-width stamp (ISOFormat)")
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
