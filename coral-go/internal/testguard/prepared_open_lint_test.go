package testguard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// board.NewStorePrepared and store.OpenPrepared open a database file WITHOUT running the schema step (and so none of the migrations that follow it): they are for a test that
// starts from a copy of a database this build just migrated (boardtest, storetest, #5444). A production caller would open an older database and silently skip its migrations,
// so no non-test file may name either one except the two test-support packages that make the copies. This reads every non-test .go file of the module (both names, called or
// only referenced) and fails with the file and line.
var preparedOpeners = map[string]bool{"NewStorePrepared": true, "OpenPrepared": true}

// preparedAllowed are the files that may use them: the test-support packages (and the two definitions, which are declarations, not uses).
var preparedAllowed = map[string]bool{
	"internal/board/boardtest/boardtest.go": true,
	"internal/store/storetest/storetest.go": true,
}

func TestNoProductionCodeOpensADatabaseWithoutItsSchemaStep(t *testing.T) {
	root := filepath.Join("..", "..") // coral-go
	fset := token.NewFileSet()
	var offenders []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "testdata" || name == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if preparedAllowed[rel] {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			return nil // a file that does not parse is the compiler's to report
		}
		declared := map[*ast.Ident]bool{} // the definitions themselves are not uses
		for _, decl := range f.Decls {
			if fd, ok := decl.(*ast.FuncDecl); ok {
				declared[fd.Name] = true
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && preparedOpeners[id.Name] && !declared[id] {
				offenders = append(offenders, rel+":"+strconv.Itoa(fset.Position(id.Pos()).Line)+" uses "+id.Name)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Fatalf("a non-test file uses an opener that skips the schema step and its migrations (only boardtest and storetest may):\n  %s", strings.Join(offenders, "\n  "))
	}
}

// boardtest and storetest hand out databases that skip the schema step (above): only tests may import them. A non-test file that did would put a prepared database in a program,
// or make a production package depend on test support. This reads the imports of every non-test .go file of the module.
func TestNoNonTestFileImportsTheDatabaseTestSupportPackages(t *testing.T) {
	root := filepath.Join("..", "..") // coral-go
	banned := map[string]bool{
		"github.com/subgentic/coral/internal/board/boardtest": true,
		"github.com/subgentic/coral/internal/store/storetest": true,
	}
	fset := token.NewFileSet()
	var offenders []string
	checked := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "testdata" || name == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if perr != nil {
			return nil // a file that does not parse is the compiler's to report
		}
		checked++
		rel, _ := filepath.Rel(root, path)
		for _, imp := range f.Imports {
			if p, uerr := strconv.Unquote(imp.Path.Value); uerr == nil && banned[p] {
				offenders = append(offenders, filepath.ToSlash(rel)+" imports "+p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	assert.Greater(t, checked, 100, "the module's files were read")
	sort.Strings(offenders)
	assert.Empty(t, offenders, "a non-test file imports a database test-support package (only _test.go files may)")
}
