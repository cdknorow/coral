package board

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// #3549 (QA's #3543 follow-up): the notifier's LatestUnreadMessageID and HasUnread*Message
// confirm their LIKE candidates with containsExplicitTag and the board's numbered names, as
// CheckUnread and ReadMessages do, so "@Backend Dev 2" nudges "Backend Dev 2" and never
// "Backend Dev" on a board that has both.
func TestNotifierConfirmsMentions(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for _, id := range []string{"Backend Dev", "Backend Dev 2", "Lead"} {
		if _, err := s.Subscribe(ctx, "team", id, id, "", nil, nil, "mentions"); err != nil {
			t.Fatal(err)
		}
	}
	post := func(from, content string) int64 {
		t.Helper()
		m, err := s.PostMessage(ctx, "team", from, content, nil)
		if err != nil {
			t.Fatal(err)
		}
		return m.ID
	}
	type state struct {
		unread int
		latest int64
		other  bool
		health bool
	}
	get := func(sub string) state {
		t.Helper()
		n, err := s.CheckUnread(ctx, "team", sub)
		if err != nil {
			t.Fatal(err)
		}
		latest, err := s.LatestUnreadMessageID(ctx, "team", sub)
		if err != nil {
			t.Fatal(err)
		}
		other, err := s.HasUnreadNonHealthMessage(ctx, "team", sub)
		if err != nil {
			t.Fatal(err)
		}
		health, err := s.HasUnreadHealthMessage(ctx, "team", sub)
		if err != nil {
			t.Fatal(err)
		}
		return state{n, latest, other, health}
	}

	// Only "Backend Dev 2" is tagged.
	numbered := post("Lead", "@Backend Dev 2 please take #7")
	if got := get("Backend Dev"); got != (state{}) {
		t.Errorf("Backend Dev after '@Backend Dev 2': %+v, want nothing", got)
	}
	if got := get("Backend Dev 2"); got != (state{1, numbered, true, false}) {
		t.Errorf("Backend Dev 2: %+v, want 1, latest %d, other", got, numbered)
	}

	// A genuine mention, then a newer one for "Backend Dev 2" only: the latest for
	// "Backend Dev" is the genuine one, not the newer LIKE match.
	genuine := post("Lead", "@Backend Dev can you check the build")
	post("Lead", "@Backend Dev 2 and the logs too")
	if got := get("Backend Dev"); got != (state{1, genuine, true, false}) {
		t.Errorf("Backend Dev after a genuine mention: %+v, want 1, latest %d, other", got, genuine)
	}

	// The health monitor's message for "Backend Dev 2" isn't one for "Backend Dev".
	health := post("Coral Health Monitor", "@Backend Dev 2 has been idle for 30 minutes")
	if got := get("Backend Dev"); got.health {
		t.Errorf("Backend Dev: a health message for Backend Dev 2 counted: %+v", got)
	}
	if got := get("Backend Dev 2"); !got.health || got.latest != health {
		t.Errorf("Backend Dev 2: %+v, want the health message %d", got, health)
	}

	// A word-boundary miss ("@Backend Developer") isn't a mention of "Backend Dev" either.
	if _, err := s.Subscribe(ctx, "solo", "Backend Dev", "Backend Dev", "", nil, nil, "mentions"); err != nil {
		t.Fatal(err)
	}
	post2, err := s.PostMessage(ctx, "solo", "Lead", "@Backend Developers, the new hires, start Monday", nil)
	if err != nil || post2 == nil {
		t.Fatal(err)
	}
	if latest, _ := s.LatestUnreadMessageID(ctx, "solo", "Backend Dev"); latest != 0 {
		t.Errorf("'@Backend Developers' gave latest %d, want 0", latest)
	}
	if has, _ := s.HasUnreadNonHealthMessage(ctx, "solo", "Backend Dev"); has {
		t.Error("'@Backend Developers' counted as a mention of Backend Dev")
	}
}

// QA's #3549 review: on a store with one connection (":memory:", or a board on sessions.db via
// UseDatabase, where the reader is the writer's only connection) the mention checks must not
// need a second connection while their candidates' cursor is open; that waited forever.
func TestMentionChecksOnOneConnectionStore(t *testing.T) {
	s, err := NewStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	for _, id := range []string{"Backend Dev", "Backend Dev 2", "Lead"} {
		if _, err := s.Subscribe(ctx, "team", id, id, "", nil, nil, "mentions"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.PostMessage(ctx, "team", "Lead", "@Backend Dev 2 please take #7", nil); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		latest, err := s.LatestUnreadMessageID(ctx, "team", "Backend Dev")
		if err == nil && latest != 0 {
			err = fmt.Errorf("latest %d, want 0", latest)
		}
		if err == nil {
			var has bool
			if has, err = s.HasUnreadNonHealthMessage(ctx, "team", "Backend Dev"); err == nil && has {
				err = fmt.Errorf("a mention of Backend Dev 2 counted for Backend Dev")
			}
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("the mention checks hung on a one-connection store")
	}
}

// #3578: HasUnreadMention is the gate's mention check, by the board's own rule, whatever the
// subscriber's receive mode.
func TestHasUnreadMention(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for _, id := range []string{"Backend Dev", "Backend Dev 2", "Lead"} {
		if _, err := s.Subscribe(ctx, "team", id, id, "", nil, nil, "all"); err != nil {
			t.Fatal(err)
		}
	}
	has := func(sub string) bool {
		t.Helper()
		ok, err := s.HasUnreadMention(ctx, "team", sub)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if _, err := s.PostMessage(ctx, "team", "Lead", "general chatter, nobody tagged", nil); err != nil {
		t.Fatal(err)
	}
	if has("Backend Dev") || has("Backend Dev 2") {
		t.Fatal("chatter tags nobody")
	}
	if _, err := s.PostMessage(ctx, "team", "Lead", "@Backend Dev 2 take #7", nil); err != nil {
		t.Fatal(err)
	}
	if !has("Backend Dev 2") {
		t.Error("Backend Dev 2 is tagged")
	}
	if has("Backend Dev") {
		t.Error("'@Backend Dev 2' doesn't tag Backend Dev on a board that has a Backend Dev 2")
	}
	if _, err := s.PostMessage(ctx, "team", "Lead", "@all standup in five", nil); err != nil {
		t.Fatal(err)
	}
	if !has("Backend Dev") {
		t.Error("@all tags everyone")
	}
	if has("Lead") {
		t.Error("an agent's own posts never tag it")
	}
	if ok, err := s.HasUnreadMention(ctx, "team", "Nobody"); err != nil || ok {
		t.Errorf("an unknown subscriber: %v %v", ok, err)
	}
}
