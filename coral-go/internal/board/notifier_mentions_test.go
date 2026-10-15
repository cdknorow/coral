package board

import (
	"context"
	"testing"
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
