package background

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// Auto-accept is the agent's CLI permission flag, which has no count: nothing enforced a limit on acceptances, so no
// field, map or document may offer one (#6917, #6916 point 4).
func TestNoAutoAcceptLimitIsOfferedAnywhere(t *testing.T) {
	gone := regexp.MustCompile(`\b(max_auto` + `_accepts|[mM]axAuto` + `Accepts|AutoAccept` + `(Limits|Counts|Sessions))\b`)
	root := filepath.Join("..", "..")
	self := "no_auto_accept_count_test.go"
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); n == "node_modules" || n == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(path) {
		case ".go", ".md", ".js", ".html":
		default:
			return nil
		}
		if d.Name() == self {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if w := gone.Find(b); w != nil {
			t.Errorf("%s still names %q", path, w)
		}
		return nil
	})
	if walkErr != nil {
		t.Fatal(walkErr)
	}
}
