package routes

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/querypage"
)

// #4806: the saved list's q, the team artifacts' q and filters and the History events' q refuse every control character, at the start, in the
// middle and at the end, over HTTP (or the parser the route calls).
func TestSearchRoutesRefuseEveryControlCharacter(t *testing.T) {
	cases := querypage.ControlCases()
	rig := newSavedRig(t)
	hist := setupHistoryEventsServer(t)
	for _, v := range cases {
		code, _ := rig.do("GET", "/api/saved?q="+url.QueryEscape(v), savedAsOperator, nil)
		assert.Equal(t, 400, code, "saved q %q", v)
		code, _ = hist.get(t, "s1", "q="+url.QueryEscape(v), false)
		assert.Equal(t, 400, code, "history events q %q", v)
		for _, key := range []string{"q", "kind", "origin", "by", "board"} {
			_, err := parseTeamArtifactQuery(url.Values{key: {v}})
			assert.Error(t, err, "artifacts %s %q", key, v)
		}
	}
	code, _ := rig.do("GET", "/api/saved?q="+url.QueryEscape(" a b "), savedAsOperator, nil)
	assert.Equal(t, 200, code)
	_, err := parseTeamArtifactQuery(url.Values{"q": {" a b "}})
	require.NoError(t, err)
}
