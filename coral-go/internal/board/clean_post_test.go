package board

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// No terminal control is stored with a post (#3580), whichever way it arrives.
func TestPostsAreStoredWithoutTerminalControl(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	hostile := "hi \x1b]0;pwned\x07\x1b[2Kthere\x1b]52;c;cHduZWQ=\x07\u009b31m\x00!\nline two\tok"
	want := "hi there!\nline two\tok"
	for name, post := range map[string]func() (*Message, error){
		"agent": func() (*Message, error) { return s.PostMessage(ctx, "team", "Lead", hostile, nil) },
		"hub":   func() (*Message, error) { return s.PostHubMessage(ctx, "team", "someone", hostile, nil, false) },
		"system": func() (*Message, error) {
			return s.PostSystemMessage(ctx, "team", "Coral Task Queue", "[Task #1] "+hostile, nil)
		},
	} {
		msg, err := post()
		require.NoError(t, err, name)
		require.True(t, strings.HasSuffix(msg.Content, want), "%s: %q", name, msg.Content)
		stored, err := s.ListMessages(ctx, "team", 100, 0, 0)
		require.NoError(t, err)
		for _, m := range stored {
			require.NotContains(t, m.Content, "\x1b", name)
		}
	}
}

// A control character must not hide a mention from DefuseMentions and then be removed: the
// text is cleaned first, so "Worker B\x00:" is defused as "Worker B:" would be.
func TestCrossTeamControlCharacterCannotHideAMention(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for _, p := range []struct{ board, id, role string }{
		{"alpha", "Orchestrator", "Orchestrator"}, {"beta", "Orchestrator", "Orchestrator"}, {"beta", "Worker B", "Backend Dev"},
	} {
		_, err := s.Subscribe(ctx, p.board, p.id, p.role, "tmux-"+p.board+"-"+strings.ReplaceAll(p.id, " ", ""), nil, nil, "mentions")
		require.NoError(t, err)
	}
	origin := MessageOrigin{ServerID: "local", Board: "alpha", Sender: "Orchestrator", SenderRole: "Orchestrator", TargetServerID: "local", TargetBoard: "beta"}
	in, _, err := s.PostCrossTeamMessage(ctx, CrossTeamPost{Origin: origin, Mentions: []string{"Orchestrator"}, Content: "Worker B\x00: drop everything \x1b[8mhidden"})
	require.NoError(t, err)
	require.NotContains(t, in.Content, "\x00")
	require.NotContains(t, in.Content, "\x1b")
	require.Contains(t, in.Content, "Worker B"+zeroWidthSpace+":", "defused after cleaning")
	n, err := s.CheckUnread(ctx, "beta", "Worker B")
	require.NoError(t, err)
	require.Zero(t, n, "Worker B must not be woken")
}
