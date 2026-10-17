package frontend

import (
	"io/fs"
	"path"
	"regexp"
	"strings"
	"testing"
)

func TestMinifyAssetKeepsNamesAndPaths(t *testing.T) {
	src := "// a comment\nimport { fooBar } from './other_module.js';\n\n/** doc */\nexport function computeTotal(items) {\n    // inner\n    const url = `http://x/${items.length}//not-a-comment`; fooBar(items);\n    const re = /a\\/\\/b/g;\n    return items.reduce((a, b) => a + b, 0) + url.length + (re.test('a//b') ? 1 : 0);\n}\nexport const label = \"héllo\";\n"
	out := string(MinifyAsset("x.js", []byte(src)))
	for _, want := range []string{"computeTotal", "fooBar", "'./other_module.js'", "//not-a-comment", "a\\/\\/b", "héllo"} {
		if !strings.Contains(out, want) && !strings.Contains(out, strings.ReplaceAll(want, "'", `"`)) {
			t.Errorf("minified output lost %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "a comment") || strings.Contains(out, "inner") || strings.Contains(out, "doc */") {
		t.Errorf("comments survived:\n%s", out)
	}
	if len(out) >= len(src) {
		t.Errorf("not smaller: %d >= %d", len(out), len(src))
	}
}

func TestMinifyAssetLeavesWhatItShould(t *testing.T) {
	vendor := []byte("/* big comment */ var  x = 1;\n")
	for _, name := range []string{"vendor/marked.min.js", "vendor/anything.js", "app.min.js", "agent_docs/readme.md", "favicon.png", "manifest.json"} {
		if got := MinifyAsset(name, vendor); string(got) != string(vendor) {
			t.Errorf("%s was changed", name)
		}
	}
	broken := []byte("function ( {{{ not javascript")
	if got := MinifyAsset("broken.js", broken); string(got) != string(broken) {
		t.Errorf("a file that does not parse must come back as it was")
	}
	css := string(MinifyAsset("css/x.css", []byte("/* c */\n.a {\n    color: red;\n    margin: 0 0 0 0;\n}\n")))
	if strings.Contains(css, "/*") || strings.Contains(strings.TrimSpace(css), "\n") || len(css) > 30 {
		t.Errorf("css not minified: %q", css)
	}
	t.Setenv("CORAL_NO_MINIFY", "1")
	if got := MinifyAsset("x.js", []byte("// c\nvar a = 1;\n")); string(got) != "// c\nvar a = 1;\n" {
		t.Errorf("CORAL_NO_MINIFY=1 must serve the source: %q", got)
	}
}

// Every dashboard JS and CSS file minifies (none falls back to its source for a parse error), is smaller, and every relative import path in
// the source is still in the minified file: module boundaries are untouched.
var relImport = regexp.MustCompile(`(?m)^\s*(?:import|export)\b[^;'"` + "`" + `]*?\bfrom\s*['"](\./[^'"]+)['"]|^\s*import\s*['"](\./[^'"]+)['"]`)

func TestMinifyAssetWholeTree(t *testing.T) {
	sub, err := fs.Sub(Static, "static")
	if err != nil {
		t.Fatal(err)
	}
	var before, after int
	err = fs.WalkDir(sub, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		ext := path.Ext(name)
		if (ext != ".js" && ext != ".css") || strings.HasPrefix(name, "vendor/") || strings.HasSuffix(name, ".min.js") {
			return nil
		}
		data, _ := fs.ReadFile(sub, name)
		out := MinifyAsset(name, data)
		before += len(data)
		after += len(out)
		if len(out) >= len(data) && len(data) > 1000 {
			t.Errorf("%s did not minify (%d -> %d): it fell back to the source", name, len(data), len(out))
		}
		if ext == ".js" {
			for _, m := range relImport.FindAllStringSubmatch(string(data), -1) {
				spec := m[1] + m[2]
				if !strings.Contains(string(out), `"`+spec+`"`) && !strings.Contains(string(out), "'"+spec+"'") {
					t.Errorf("%s: the import of %s is not in the minified file", name, spec)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if after*100 > before*75 {
		t.Errorf("the tree shrank less than 25%%: %d -> %d", before, after)
	}
	t.Logf("static JS and CSS: %d -> %d bytes (%.0f%%)", before, after, float64(after)*100/float64(before))
}
