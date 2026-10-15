package board

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// #3534 (A): the rule for new board and team names.
func TestNewNameProblem(t *testing.T) {
	for _, ok := range []string{"coral-go-team", "Team_2", "a", "9lives", strings.Repeat("x", 64), "Projects-2"} {
		assert.Empty(t, NewNameProblem(ok), ok)
	}
	for _, bad := range []string{"", "Coral Go-To-Market Team", "team.v2", "@homelab/qa", "a/b", "../x", "-lead", "_x",
		strings.Repeat("x", 65), "tëam", "team\x00", "projects", "Tasks", "remotes", "a b", "a@b"} {
		assert.NotEmpty(t, NewNameProblem(bad), "%q", bad)
	}
}
