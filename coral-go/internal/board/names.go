package board

import (
	"regexp"
	"strings"
)

// NewNamePattern is the rule for a NEW board or team name (#3534, the operator's decision):
// ASCII letters, digits, '-' and '_', starting with a letter or digit, at most 64 characters.
// No spaces, dots, '@' or '/', so a name is safe in a URL path, a file path, a git branch
// and the dashboard's "@server/name" keys as it is. Boards that already exist keep any name.
var NewNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// reservedNames collide with the board API's own routes (/api/board/projects, /tasks,
// /remotes).
var reservedNames = map[string]bool{"projects": true, "remotes": true, "tasks": true}

// NewNameProblem returns "" when name may be used for a new board or team, else a sentence
// saying why not.
func NewNameProblem(name string) string {
	switch {
	case name == "":
		return "a board name is required"
	case len(name) > 64:
		return "a board name is at most 64 characters"
	case !NewNamePattern.MatchString(name):
		return "a board name may use only English letters (A-Z), digits, '-' and '_', starting with a letter or digit: no accents, spaces, dots, '@' or '/'"
	case reservedNames[strings.ToLower(name)]:
		return "\"" + name + "\" is reserved; choose another board name"
	}
	return ""
}
