package coralnames_test

import (
	"github.com/subgentic/coral/internal/coralnames"
	"github.com/subgentic/coral/internal/gitutil"
	"os/exec"
	"strings"
	"testing"
	"unicode/utf8"
)

// A fuzz target for the team branch slug (#6701, #6878): whatever a board is called, "coral-team/<slug>" is a branch name git accepts, valid UTF-8, no longer than the name and its hash suffix, the same
// every time, and a name that is already a valid slug is used as it is.

var teamBranchSeeds = []string{
	"", "my-team", "my team", "a..b", "plan.lock", "x~1", "x^y", "a:b", "a?b", "a*b", "a[b", "a\\b", "a@{b", "@", ".hidden", "trail.", "-lead", "trail-", "a//b", "/slash/", "tab\tx", "nl\nx", "del\x7fx", "nul\x00x",
	"\u202etrojan", "emoji \U0001F600", "\xff\xfe", "a.lock.lock", "..", "...", ".lock", "x.lock", "UPPER_case-1.2", "b-7jxghjtn25",
}

// gitRefOK checks a full branch name against git's rules (git check-ref-format --branch): no control character, space, ~ ^ : ? * [ or backslash; no "..", "@{", "//"; no leading "-" or "/", no
// trailing "/" or "."; no component starting with "." or ending ".lock"; not "@".
func gitRefOK(ref string) bool {
	if ref == "" || ref == "@" || strings.HasPrefix(ref, "-") || strings.HasPrefix(ref, "/") || strings.HasSuffix(ref, "/") || strings.HasSuffix(ref, ".") {
		return false
	}
	if strings.Contains(ref, "..") || strings.Contains(ref, "@{") || strings.Contains(ref, "//") {
		return false
	}
	for _, r := range ref {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(" ~^:?*[\\", r) {
			return false
		}
	}
	for _, comp := range strings.Split(ref, "/") {
		if strings.HasPrefix(comp, ".") || strings.HasSuffix(comp, ".lock") {
			return false
		}
	}
	return true
}

func FuzzTeamBranch(f *testing.F) {
	for _, s := range teamBranchSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, board string) {
		br := coralnames.TeamBranch(board)
		if !strings.HasPrefix(br, "coral-team/") {
			t.Fatalf("coralnames.TeamBranch(%q) = %q has no coral-team/ prefix", board, br)
		}
		slug := strings.TrimPrefix(br, "coral-team/")
		if slug == "" {
			t.Fatalf("coralnames.TeamBranch(%q) = %q has an empty slug", board, br)
		}
		if !utf8.ValidString(br) {
			t.Fatalf("coralnames.TeamBranch(%q) = %q is not valid UTF-8", board, br)
		}
		if !gitRefOK(br) {
			t.Fatalf("coralnames.TeamBranch(%q) = %q breaks git's branch-name rules", board, br)
		}
		if err := gitutil.ValidRef(br); err != nil {
			t.Fatalf("coralnames.TeamBranch(%q) = %q is refused by ValidRef: %v", board, br, err)
		}
		// each rune of the name becomes at most itself (or "-"); at most "-xxxxxx" is added; "team" stands in for a name with nothing left
		if limit := len(board)*utf8.UTFMax + len("team") + len("-xxxxxx"); len(slug) > limit {
			t.Fatalf("coralnames.TeamBranch(%q) = %q is far longer than its name", board, br)
		}
		if coralnames.TeamBranch(board) != br {
			t.Fatalf("coralnames.TeamBranch(%q) is not the same twice", board)
		}
		if len(board) <= coralnames.MaxTeamSlug && gitRefOK("coral-team/"+board) && utf8.ValidString(board) && !strings.ContainsAny(board, "\u0080\u00a0") && onlySlugRunes(board) && coralnames.TeamBranch(board) != "coral-team/"+board {
			t.Fatalf("coralnames.TeamBranch(%q) = %q changed a name that was already a valid slug", board, br)
		}
	})
}

// onlySlugRunes: the characters TeamBranch keeps as they are (letters, digits, ".", "-", "_"), none at the edges or doubled in the ways it trims.
func onlySlugRunes(s string) bool {
	if s == "" || strings.HasPrefix(s, ".") || strings.HasPrefix(s, "-") || strings.HasSuffix(s, ".") || strings.HasSuffix(s, "-") || strings.HasSuffix(s, ".lock") || strings.Contains(s, "..") {
		return false
	}
	for _, r := range s {
		if !(r == '.' || r == '-' || r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}

// The seeds through the real git, so the pure checker above cannot drift from git's own rules.
func TestTeamBranchSeedsAreBranchNamesToRealGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	for _, board := range teamBranchSeeds {
		br := coralnames.TeamBranch(board)
		if out, err := exec.Command("git", "check-ref-format", "--branch", br).CombinedOutput(); err != nil {
			t.Errorf("git refuses coralnames.TeamBranch(%q) = %q: %s", board, br, strings.TrimSpace(string(out)))
		}
	}
}

// A long board name (#6878, found by FuzzTeamBranch): the branch stays under what git and ValidRef take (255), and two long names that differ only past the cut still get two branches.
func TestTeamBranchOfALongNameIsShortAndDistinct(t *testing.T) {
	a, b := strings.Repeat("a", 300)+"1", strings.Repeat("a", 300)+"2"
	ba, bb := coralnames.TeamBranch(a), coralnames.TeamBranch(b)
	if err := gitutil.ValidRef(ba); err != nil {
		t.Fatalf("TeamBranch of a long name = %q: %v", ba, err)
	}
	if len(ba) > 255 || len(bb) > 255 {
		t.Fatalf("a long name made a branch of %d characters", len(ba))
	}
	if ba == bb {
		t.Fatalf("two long names that differ past the cut share the branch %q", ba)
	}
}

// A team whose name works today keeps its branch across the update (#6878): a name at the cut (200 bytes) is used as it is, and one byte more is slugged with a hash.
func TestTeamBranchKeepsEveryNameThatCouldLaunch(t *testing.T) {
	for _, n := range []int{1, 50, 100, 101, 150, 199, coralnames.MaxTeamSlug} {
		name := strings.Repeat("a", n)
		if got := coralnames.TeamBranch(name); got != "coral-team/"+name {
			t.Errorf("a %d-character name changed its branch: %q", n, got)
		}
	}
	over := strings.Repeat("a", coralnames.MaxTeamSlug+1)
	if got := coralnames.TeamBranch(over); got == "coral-team/"+over || len(got) > 255 {
		t.Errorf("a name over the cut kept its whole length or broke the limit: %d characters", len(got))
	}
}
