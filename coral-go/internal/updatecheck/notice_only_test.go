package updatecheck

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The release channels are for the NOTICE only (#5557, design #5551, Security's condition 7): the updater and the relaunch's apply path install
// what a promote manifest names, never what a channel offers. This fails when that code starts to read the channel, the list feed or this package
// (apart from the Feeds type the status handler carries), so the rule cannot erode when automatic updates are built.

var channelWords = regexp.MustCompile(`Agentic|ReleasesListAPI|UpdateChannel|update_channel|ParseChannel|FetchFeed|PeekFeed|WarmFeed|\.Feed\(`)
var updatecheckUse = regexp.MustCompile(`updatecheck\.(\w+)`)

func TestTheUpdaterNeverReadsTheUpdateChannel(t *testing.T) {
	roots := []string{"../updater", "../../cmd/coral-update"}
	files := []string{"../server/routes/system_update_relaunch.go", "../server/routes/system_update_relaunch_wire.go"}
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
				files = append(files, p)
			}
			return nil
		})
	}
	if len(files) < 20 {
		t.Fatalf("the scan found %d files, want at least 20: it has gone blind (a moved folder?)", len(files))
	}
	relaunch := map[string]bool{files[0]: true, files[1]: true}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		src := string(b)
		if m := channelWords.FindString(src); m != "" {
			t.Errorf("%s names %q: the channel is for the notice only and never reaches the updater or its apply path", f, m)
		}
		if !relaunch[f] && strings.Contains(src, "internal/updatecheck") {
			t.Errorf("%s imports updatecheck: the updater does not read the release feeds", f)
		}
		if relaunch[f] {
			for _, m := range updatecheckUse.FindAllStringSubmatch(src, -1) {
				if m[1] != "Feeds" {
					t.Errorf("%s uses updatecheck.%s: the relaunch handler carries only the Feeds type", f, m[1])
				}
			}
		}
	}
}

func TestTheNoticeOnlyScanSeesAForbiddenWord(t *testing.T) {
	for _, bad := range []string{"updatecheck.Agentic", "ReleasesListAPI", `settings.UpdateChannel`, `"update_channel"`, "updatecheck.Default.Feed(ctx)", "ParseChannel(x)"} {
		if !channelWords.MatchString("x := " + bad) {
			t.Errorf("%q is not seen", bad)
		}
	}
	if channelWords.MatchString("updatecheck.Feeds{}") {
		t.Error("the Feeds type is allowed")
	}
}
