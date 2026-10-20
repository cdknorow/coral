package routes

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #5499: a cross-team send whose text names coral://artifacts links that were not attached is still sent, with a warning in the reply that names them: such a link is a file on THIS computer
// only, and means nothing on the other Coral.
func TestRelaySendWarnsOfArtifactLinksThatAreNotAttached(t *testing.T) {
	e := newRelayAttachEnv(t, true)
	h1, h2 := strings.Repeat("a", 64), strings.Repeat("b1", 32)
	l1, l2 := "coral://artifacts/"+h1, "coral://artifacts/"+h2

	// Two links in the text, neither attached: the message goes, the warning names both once, in order.
	code, out, _ := e.relayText(alphaOrchSID, "m1", "see "+l1+" and "+l2+", again "+l1+".")
	require.Equal(t, http.StatusOK, code, out)
	assert.Equal(t, true, out["ok"], "the message is still sent")
	assert.Equal(t, []any{"unattached_artifact_links"}, out["warnings"])
	assert.Equal(t, []any{l1, l2}, out["unattached_links"])
	_, _, msgs := e.wire.snapshot()
	assert.Equal(t, 1, msgs)

	// No link, no warning.
	code, out, _ = e.relayText(alphaOrchSID, "m2", "no links here")
	require.Equal(t, http.StatusOK, code, out)
	assert.NotContains(t, out, "warnings")
	assert.NotContains(t, out, "unattached_links")

	// A link that IS attached is not named; one that is not still is.
	a := e.upload(alphaOrchSID, "# alpha report\nnumbers\n")
	code, out, _ = e.relayText(alphaOrchSID, "m3", "the report "+uri(a)+" and the other "+l2, uri(a))
	require.Equal(t, http.StatusOK, code, out)
	assert.Equal(t, []any{l2}, out["unattached_links"], "only the one that is not attached")
	assert.Equal(t, []any{uri(a)}, out["attached"])

	// Everything named is attached: no warning.
	b := e.upload(alphaOrchSID, "# second\nmore\n")
	code, out, _ = e.relayText(alphaOrchSID, "m4", "files "+uri(a)+" "+uri(b), uri(a), uri(b))
	require.Equal(t, http.StatusOK, code, out)
	assert.NotContains(t, out, "warnings")
}

func TestUnattachedArtifactLinksAreOnlyRealLinks(t *testing.T) {
	h := strings.Repeat("c", 64)
	for text, want := range map[string][]string{
		"coral://artifacts/" + h:                        {"coral://artifacts/" + h},
		"(coral://artifacts/" + h + ")":                 {"coral://artifacts/" + h},
		"coral://artifacts/" + h + "0":                  nil, // a longer hex run is not a link
		"coral://artifacts/" + h[:63]:                   nil,
		"coral://artifacts/" + strings.ToUpper(h):       nil, // ids are lower case
		"http://x/coral://artifacts/" + h + "?v=2":      {"coral://artifacts/" + h},
		"coral:/artifacts/" + h + " and artifacts/" + h: nil,
		"see /api/artifacts/" + h + "?v=2 for it":       {"coral://artifacts/" + h}, // the upload's own url form (#5500 review)
		"/api/artifacts/" + h + "0":                     nil,
		"/api/artifacts/" + strings.ToUpper(h):          nil,
		"":                                              nil,
	} {
		assert.Equal(t, want, unattachedArtifactLinks(text, nil), text)
	}
	var many strings.Builder
	for i := 0; i < 30; i++ {
		many.WriteString("coral://artifacts/" + strings.Repeat(string(rune('a'+i%6)), 63) + string(rune('0'+i%10)) + " ")
	}
	assert.LessOrEqual(t, len(unattachedArtifactLinks(many.String(), nil)), maxUnattachedLinksNamed, "the reply names the first few")
}
