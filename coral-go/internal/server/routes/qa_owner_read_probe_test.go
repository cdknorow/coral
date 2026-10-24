package routes

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// QA #7058: the owner's READ of the comments through the Hub says the owner may comment and shows the owner's own comments as theirs.
func TestArtifactCommentsOwnerReadThroughTheHubSaysYouMayComment(t *testing.T) {
	rig := newCmtRig(t)
	withLabel(t, cmtHubUser, nil)
	code, out := rig.do("POST", cp(cmtDoc), cmtAsHubOwner, map[string]any{"body": "from the Hub", "anchor": anchorOf("the lock"), "board": "team-a"})
	require.Equal(t, 201, code, fmt.Sprint(out))
	code, out = rig.do("GET", cp(cmtDoc), cmtAsHubOwner, nil)
	require.Equal(t, 200, code)
	t.Logf("you = %v", out["you"])
	you, _ := out["you"].(map[string]any)
	assert.NotEqual(t, "hub", you["kind"], "the owner is not a hub viewer")
	assert.Equal(t, true, you["can_comment"], "the owner may comment through the Hub")
	for _, c := range out["comments"].([]any) {
		assert.Equal(t, true, c.(map[string]any)["mine"], "the owner's own comment is theirs")
	}
}
