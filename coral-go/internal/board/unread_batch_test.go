package board

import (
	"context"
	"fmt"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4121: UnreadForSessions gives every session the subscription GetSubscriptionBySessionName gives
// it and the count CheckUnread gives, on a fresh cache and on a warm one, across receive modes,
// numbered names, "@all", a mention of a similar name, the sender's own messages, the task queue's
// and read cursors part way.
func TestUnreadForSessionsMatchesCheckUnread(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	rng := rand.New(rand.NewSource(4121))
	type member struct{ id, title, session, mode string }
	var members []member
	for b, project := range []string{"alpha", "beta"} {
		for i, m := range []struct{ id, title, mode string }{
			{"Lead Developer", "Lead Developer", ""}, {"Backend Dev", "Backend Dev", "mentions"},
			{"Backend Dev 2", "Backend Dev", "mentions"}, {"QA Engineer", "QA", ""},
			{"Orchestrator", "Orchestrator", "all"}, {"Quiet", "Quiet", "none"},
		} {
			sess := fmt.Sprintf("claude-%d%d00-0000", b, i)
			_, err := s.Subscribe(ctx, project, m.id, m.title, sess, nil, m.mode)
			require.NoError(t, err)
			members = append(members, member{m.id, m.title, sess, m.mode})
		}
	}
	texts := []string{"@Lead Developer please look", "@Backend Dev on it", "@Backend Dev 2 yours", "@all heads up",
		"@notify_all now", "@BACKENDDEV case", "QA: done", "Lead Developer — note", "no mention", "@Lead Developerx not a tag",
		"@backend dev 22 no", "@QA Engineer here", "@notifyXall wildcard-ish"}
	senders := []string{"Lead Developer", "Backend Dev", "QA Engineer", "Coral Task Queue", "Orchestrator"}
	post := func(n int) {
		for i := 0; i < n; i++ {
			project := []string{"alpha", "beta"}[rng.Intn(2)]
			_, err := s.PostMessage(ctx, project, senders[rng.Intn(len(senders))], texts[rng.Intn(len(texts))], nil)
			require.NoError(t, err)
		}
	}
	var sessions []string
	for _, m := range members {
		sessions = append(sessions, m.session)
	}
	sessions = append(sessions, "claude-nobody-0000")
	compare := func(what string) {
		t.Helper()
		got, err := s.UnreadForSessions(ctx, sessions)
		require.NoError(t, err)
		_, absent := got["claude-nobody-0000"]
		assert.False(t, absent, "%s: a session with no subscription is absent", what)
		s.unreadCounts.invalidate() // the per-agent path counts afresh
		total := 0
		for _, m := range members {
			sub, err := s.GetSubscriptionBySessionName(ctx, m.session)
			require.NoError(t, err)
			want, err := s.CheckUnread(ctx, sub.Project, sub.SubscriberID)
			require.NoError(t, err)
			require.NoError(t, got[m.session].Err)
			assert.Equal(t, sub.ID, got[m.session].Sub.ID, "%s: %s's subscription", what, m.session)
			assert.Equal(t, want, got[m.session].Unread, "%s: %s (%s) on %s", what, m.id, m.mode, sub.Project)
			total += want
		}
		assert.Positive(t, total, "%s: some unread at all", what)
	}

	post(60)
	compare("first count")
	_, _ = s.UnreadForSessions(ctx, sessions) // warm the cache
	post(25)
	compare("warm cache, more messages")
	for _, m := range members[:3] {
		sub, _ := s.GetSubscriptionBySessionName(ctx, m.session)
		_, err := s.ReadMessages(ctx, sub.Project, sub.SubscriberID, 10)
		require.NoError(t, err)
	}
	post(15)
	compare("cursors moved")
}

func TestLikeMatchIsSQLiteLike(t *testing.T) {
	s := testStore(t)
	for _, c := range []struct {
		s, p string
		want bool
	}{
		{"hello @Notify_All x", "%@notify_all%", true},
		{"hello @notifyXall x", "%@notify_all%", true}, // '_' is any one character
		{"hello @notify-all", "%@notify_all%", true},
		{"ÄBC", "%äbc%", false}, // folding is ASCII only
		{"abc", "%ABC%", true},
		{"", "%a%", false},
		{"a%b", "%a%b%", true},
		// QA's #4121 cases: whitespace, empty, the wildcards alone, multi-byte characters, and
		// letters SQLite does not fold (only A-Z is folded). The expectation is SQLite's own.
		{"name ", "%name %", true},
		{" name", "% name%", true},
		{"", "", true},
		{"x", "", false},
		{"", "%", true},
		{"abc", "%", true},
		{"", "_", false},
		{"a", "_", true},
		{"ab", "_", false},
		{"%", "%\\%%", false},
		{"a_b", "%a_b%", true},
		{"axb", "%a_b%", true},
		{"a\\b", "%a\\b%", true},
		{"😀x", "_x", true},
		{"😀x", "%😀%", true},
		{"日本語", "%本%", true},
		{"日本語", "__語", true},
		{"İstanbul", "%istanbul%", false},
		{"ISTANBUL", "%istanbul%", true},
		{"ΣΟΦΙΑ", "%σοφια%", false},
		{"café DEV", "%CAFÉ dev%", false},
		{"cafe DEV", "%CAFE dev%", true},
		{"@Backend Dev 2 here", "%@backend dev%", true},
		{"@BackendDev", "%@backenddev%", true},
	} {
		assert.Equal(t, c.want, likeMatch(c.s, c.p), "%q LIKE %q", c.s, c.p)
		var sqlite bool
		require.NoError(t, s.reader().Get(&sqlite, "SELECT ? LIKE ? COLLATE NOCASE", c.s, c.p))
		assert.Equal(t, sqlite, likeMatch(c.s, c.p), "SQLite says %q LIKE %q is %v", c.s, c.p, sqlite)
		assert.Equal(t, sqlite, likeAny(c.s, []string{c.p}), "likeAny's fast path too")
	}
}
