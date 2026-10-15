package background

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// #3518: a team's knowledge folder is always <coral>/agent_knowledge/<one component>.
func TestKnowledgeTeamNamesStayInsideTheFolder(t *testing.T) {
	for _, ok := range []string{"coral-go-team", "team.v2", "a b", "Ünïcode"} {
		if !ValidKnowledgeTeam(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	dir := t.TempDir()
	for _, bad := range []string{"", ".", "..", "../x", "a/b", `a\b`, "a\x00b"} {
		if ValidKnowledgeTeam(bad) {
			t.Errorf("%q should be refused", bad)
		}
		if err := WriteKnowledgeFile(dir, bad, "agent", "x"); !errors.Is(err, ErrBadKnowledgeTeam) {
			t.Errorf("write %q: %v", bad, err)
		}
		if _, err := ReadKnowledgeBundle(dir, bad); !errors.Is(err, ErrBadKnowledgeTeam) {
			t.Errorf("read %q: %v", bad, err)
		}
		if LoadAgentKnowledge(dir, bad, "agent") != "" {
			t.Errorf("load %q", bad)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "x")); err == nil {
		t.Error("nothing is written for a refused name")
	}
}
