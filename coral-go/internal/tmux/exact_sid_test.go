package tmux

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/terminalinput"
)

// #3214: a session id matches only the "<type>-<sid>" session, never a session whose name
// merely contains it.
func TestIsSessionNameFor(t *testing.T) {
	const sid = "aaaaaaaa-1111-4222-8333-444444444444"
	for _, c := range []struct {
		name, sid string
		want      bool
	}{
		{"claude-" + sid, sid, true},
		{"CLAUDE-" + strings.ToUpper(sid), sid, true},
		{"codex-sid-001", "sid-001", true},
		{"claude-" + sid, "aaaaaaaa", false},     // a prefix of the UUID
		{"claude-" + sid, "1111-4222", false},    // a middle piece
		{"claude-" + sid, "444444444444", false}, // a suffix piece
		{"team-claude-" + sid, sid, false},       // not "<type>-<sid>"
		{"claude-" + sid + "-old", sid, false},
		{"-" + sid, sid, false},
		{"claude-" + sid, "", false},
	} {
		assert.Equal(t, c.want, IsSessionNameFor(c.name, c.sid), "%q / %q", c.name, c.sid)
	}
}

func TestFindPaneBySessionIDIsExact(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	const sid = "aaaaaaaa-1111-4222-8333-444444444444"
	createTestSession(t, c, "claude-"+sid, t.TempDir())

	pane, err := c.FindPane(ctx, "x", "claude", sid)
	require.NoError(t, err)
	require.NotNil(t, pane)
	assert.Equal(t, "claude-"+sid, pane.SessionName)

	for _, partial := range []string{"aaaaaaaa", "1111-4222", "444444444444", "laude-" + sid} {
		pane, err := c.FindPane(ctx, "x", "claude", partial)
		require.NoError(t, err)
		assert.Nil(t, pane, "a partial id %q matched %v", partial, pane)
		assert.ErrorIs(t, c.SendKeys(ctx, terminalinput.Unclassified, "x", "echo nope", "claude", partial), ErrTerminalGone)
	}
}

// A lookup by bare name (no session id) is deprecated and logged, once per name.
func TestBareNameLookupIsLogged(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	var buf bytes.Buffer
	old, oldFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(old); log.SetFlags(oldFlags) })

	name := fmt.Sprintf("bare-%d", time.Now().UnixNano()) // once per name per process: unique per run (-count)
	c.FindPane(ctx, name, "claude", "")
	c.FindPane(ctx, name, "claude", "")
	c.FindPane(ctx, "claude-aaaaaaaa-1111-4222-8333-444444444444", "", "") // an exact name is not bare
	c.FindPane(ctx, name, "claude", "aaaaaaaa-1111-4222-8333-444444444444")
	out := buf.String()
	assert.Equal(t, 1, strings.Count(out, "deprecated"), out)
	assert.Contains(t, out, `terminal "`+name+`" looked up by bare name without a session id`)
}
