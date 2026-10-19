package settings

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The census (#5147): a setting is read through this package's typed readers, never by indexing the settings map and parsing the string.
// It finds, in every non-test Go file of the module, an index of a variable that holds settings (named in settingMapNames) by a string that is a
// declared key or a family member, or by a constant named like a setting key (...Setting, ...SettingKey). A write (settings[k] = v) is not a read.
// It is a heuristic, not a proof: a settings map passed under another name is not seen. The names are the ones the code uses; a new name
// that holds a settings map should be added here.
var settingMapNames = map[string]bool{
	"settings": true, "st": true, "s": true, "saved": true, "cur": true, "current": true, "userSettings": true, "boardSettings": true, "values": true,
}

var settingConstName = regexp.MustCompile(`(Setting|SettingKey|Key)$`)

// rawReads lists the raw settings reads in src, as "line: text".
func rawReads(t *testing.T, filename string, src any) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, 0)
	if err != nil {
		t.Fatalf("%s: %v", filename, err)
	}
	writes := map[*ast.IndexExpr]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		if as, ok := n.(*ast.AssignStmt); ok && as.Tok != token.DEFINE {
			for _, l := range as.Lhs {
				if ix, ok := l.(*ast.IndexExpr); ok {
					writes[ix] = true
				}
			}
		}
		return true
	})
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		ix, ok := n.(*ast.IndexExpr)
		if !ok || writes[ix] {
			return true
		}
		id, ok := ix.X.(*ast.Ident)
		if !ok || !settingMapNames[id.Name] {
			return true
		}
		if isSettingKeyExpr(ix.Index) {
			out = append(out, strconv.Itoa(fset.Position(ix.Pos()).Line)+": "+id.Name+"[...]")
		}
		return true
	})
	return out
}

func isSettingKeyExpr(e ast.Expr) bool {
	switch k := e.(type) {
	case *ast.BasicLit:
		if k.Kind != token.STRING {
			return false
		}
		v, _ := strconv.Unquote(k.Value)
		_, ok := Lookup(v)
		return ok
	case *ast.Ident:
		return settingConstName.MatchString(k.Name)
	case *ast.SelectorExpr:
		return settingConstName.MatchString(k.Sel.Name)
	case *ast.BinaryExpr: // "default_model_" + type
		if lit, ok := k.X.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			v, _ := strconv.Unquote(lit.Value)
			for _, fam := range families {
				if v == fam.Prefix {
					return true
				}
			}
		}
	}
	return false
}

// allowed are the files that may index a settings map: none today.
var allowedRawReads = map[string]bool{}

func TestNoRawSettingsReads(t *testing.T) {
	root := filepath.Join("..", "..")
	var found []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == "internal/settings" || d.Name() == "node_modules" || d.Name() == "vendor" || (rel != "." && strings.HasPrefix(d.Name(), ".")) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") || allowedRawReads[rel] {
			return nil
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, r := range rawReads(t, p, src) {
			found = append(found, rel+":"+r)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(found)
	if len(found) > 0 {
		t.Errorf("raw settings reads (use settings.Bool/Int/IntState/IntOr/Enum/Text/TextFamily/Raw/Has instead; one place decides the words, the default and the clamp):\n  %s", strings.Join(found, "\n  "))
	}
}

// The census must see what it claims to: each of these is a read, and a write or an unrelated map is not.
func TestRawReadCensusSeesReads(t *testing.T) {
	src := `package x
func f(settings map[string]string, st map[string]string, other map[string]string, agentType string) {
	_ = settings["telemetry_enabled"]
	_ = st[SomeSetting]
	_ = settings[pkg.OtherSettingKey]
	_ = settings["default_model_"+agentType]
	settings["telemetry_enabled"] = "x"
	_ = other["telemetry_enabled"]
	_ = settings["not_a_setting_at_all"]
	_ = settings[k]
}`
	got := rawReads(t, "x.go", src)
	if len(got) != 4 {
		t.Fatalf("want 4 reads (lines 3,4,5,6), got %v", got)
	}
}
