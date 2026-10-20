package jsonl

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cdknorow/coral/internal/transcriptlink"
)

// #5308: the transcript of a working directory with a dot, an underscore or a space in its path is found in the CLI's own folder for it, not by scanning every folder and taking the first
// that holds the id. A session that was resumed from another folder has its file in two (PrepareResume copies it): the scan answered with whichever folder sorts first.
func TestAWorkingDirectoryWithDotsAndSpacesFindsItsOwnFolder(t *testing.T) {
	base := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", base)
	const sid = "6f1c2d3e-4a5b-4c6d-8e7f-0a1b2c3d4e5f"
	for _, wd := range []string{"/Users/dev/my.project", "/tmp/a_b c/repo-x", "/srv/café/ünï", "/home/u/dir.with.dots/x.y.z"} {
		own := filepath.Join(base, transcriptlink.ClaudeProjectDirName(wd))
		decoy := filepath.Join(base, "-aaa-another-folder") // sorts before every folder of a real path (they start with "-" and a longer name, or a letter after "a")
		for _, d := range []string{own, decoy} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(d, sid+".jsonl"), []byte("{}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if got, want := resolveClaudeTranscript(sid, wd), filepath.Join(own, sid+".jsonl"); got != want {
			t.Errorf("working directory %q: the transcript was taken from %s, want %s", wd, filepath.Base(filepath.Dir(got)), filepath.Base(own))
		}
		_ = os.RemoveAll(own)
		_ = os.RemoveAll(decoy)
	}
}
