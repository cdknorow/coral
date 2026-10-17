package secrets

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/pkg/redact"
	"github.com/cdknorow/coral/pkg/redact/betterleaks"
)

// #3808: Coral's redactor is betterleaks' rules through the Scanner, within the budget.
func TestRedactorIsTheLibrary(t *testing.T) {
	require.NoError(t, Init())
	s, ok := Redactor().(*redact.Scanner)
	require.True(t, ok, "%T", Redactor())
	d, ok := s.Detector.(withExtraRules)
	require.True(t, ok, "%T", s.Detector)
	_, ok = d.base.(*betterleaks.Detector)
	assert.True(t, ok, "%T", d.base)
	assert.Equal(t, Budget, s.Budget)
	l, ok := LogRedactor().(*redact.Scanner)
	require.True(t, ok, "%T", LogRedactor())
	assert.Equal(t, s.Detector, l.Detector, "one detector for the process")
	assert.Equal(t, LogBudget, l.Budget)
}

// pkg/redact's own rules stay in pkg/redact, and Coral doesn't use them (the operator's decision
// on #3808): no file outside pkg/redact, tests aside, calls them. Cut, Marker, the Scanner and
// the interfaces are the glue and may be used.
func TestCoralDoesNotUseTheOldRules(t *testing.T) {
	ruleAPI := map[string]bool{"Rules": true, "Tokens": true, "NewWriter": true, "Writer": true, "Reply": true, "JSON": true, "Document": true}
	root := filepath.Join("..", "..")
	var uses []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", "testdata", ".git", "vendor":
				return filepath.SkipDir
			}
			if rel, _ := filepath.Rel(root, path); rel == filepath.Join("pkg", "redact") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		name := ""
		for _, im := range f.Imports {
			if p, _ := strconv.Unquote(im.Path.Value); p == "github.com/cdknorow/coral/pkg/redact" {
				name = "redact"
				if im.Name != nil {
					name = im.Name.Name
				}
			}
		}
		if name == "" {
			return nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == name && ruleAPI[sel.Sel.Name] {
					uses = append(uses, path+": "+name+"."+sel.Sel.Name)
				}
			}
			return true
		})
		return nil
	})
	require.NoError(t, err)
	assert.Empty(t, uses, "Coral's code must use internal/secrets, not pkg/redact's own rules")
}
