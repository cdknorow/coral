package board

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var warnNames = []string{"Lead Developer", "Digital Ocean Expert", "Security Reviewer", "Orchestrator", "QA", "Backend Dev"}

func TestMentionWarningsSuggestTheClosestName(t *testing.T) {
	for content, want := range map[string][]string{
		// the example from the request: the name is close to a member's
		"@DO Expert please check the droplet":     {"no subscriber named 'DO Expert'; did you mean 'Digital Ocean Expert'?"},
		"@Digital Ocean Exprt the firewall":       {"no subscriber named 'Digital Ocean Exprt'; did you mean 'Digital Ocean Expert'?"},
		"@Sercurity Reviewer is the patch ready?": {"no subscriber named 'Sercurity Reviewer'; did you mean 'Security Reviewer'?"},
		"@Orchestrater done":                      {"no subscriber named 'Orchestrater'; did you mean 'Orchestrator'?"},
		"@backend dv one more thing":              {"no subscriber named 'backend dv'; did you mean 'Backend Dev'?"},
		// a --to run: each unknown name, with or without a close one
		"@Lead Developer @Nobody Here do it":       {"no subscriber named 'Nobody'"},
		"@Nobody @Security Reviewer hello":         {"no subscriber named 'Nobody'"},
		"@Lead Developer @Sercurity Reviewer ship": {"no subscriber named 'Sercurity Reviewer'; did you mean 'Security Reviewer'?"},
		// in the middle of text: only a close one
		"thanks, @Sercurity Reviewer, for the review":   {"no subscriber named 'Sercurity Reviewer'; did you mean 'Security Reviewer'?"},
		"the decorator @param and @2x are not mentions": nil,
		"mail me at someone@example.com":                nil,
		"see @Orchestrator and @all and @notify-all":    nil,
		"@Operator the release is ready":                nil,
		"@LeadDeveloper spaceless is fine":              nil,
		"@qa @QA @Qa":                                   nil,
		"no mentions at all":                            nil,
		"@Backend Dev 2 is a numbered name":             nil,
	} {
		assert.Equal(t, want, mentionWarnings(content, warnNames), content)
	}
	// at most five, each name once
	got := mentionWarnings("@A1 @B2 @C3 @D4 @E5 @F6 @G7 @A1 x", warnNames)
	assert.Len(t, got, 5)
	assert.Len(t, mentionWarnings("@Zed @zed @ZED hi", warnNames), 1, "each name once, whatever its case")
}

func TestAbbreviations(t *testing.T) {
	assert.True(t, abbreviates([]string{"DO", "Expert"}, "Digital Ocean Expert"))
	assert.True(t, abbreviates([]string{"Dig", "Ocean", "Exp"}, "Digital Ocean Expert"))
	assert.True(t, abbreviates([]string{"LD"}, "Lead Developer"))
	assert.False(t, abbreviates([]string{"DO"}, "Digital Ocean Expert"), "the whole name must be used")
	assert.False(t, abbreviates([]string{"DO", "Cloud"}, "Digital Ocean Expert"))
	assert.False(t, abbreviates([]string{"Lead"}, "Lead"), "a one-word name has nothing to shorten")
}

func TestStoreMentionWarningsUseTheBoardsMembers(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, err := s.Subscribe(ctx, "team", "Digital Ocean Expert", "Cloud", "claude-"+"11111111-0000-0000-0000-000000000000", nil, "all")
	require.NoError(t, err)
	_, err = s.Subscribe(ctx, "team", "Lead", "Lead Developer", "claude-"+"22222222-0000-0000-0000-000000000000", nil, "all")
	require.NoError(t, err)
	assert.Equal(t, []string{"no subscriber named 'DO Expert'; did you mean 'Digital Ocean Expert'?"}, s.MentionWarnings(ctx, "team", "@DO Expert are you there"))
	assert.Nil(t, s.MentionWarnings(ctx, "team", "@Lead @Cloud @Lead Developer hello"), "a name, a role and a spaced name are all members")
	assert.Nil(t, s.MentionWarnings(ctx, "team", "no at-sign"))
	assert.Nil(t, s.MentionWarnings(ctx, "elsewhere", "@Lead hello"), "a board with no members names nobody: nothing to compare with")
}
