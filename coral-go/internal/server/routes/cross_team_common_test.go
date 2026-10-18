package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
)

// The sender check both cross-team routes share (#4627): the Operator needs the board to exist; an
// Orchestrator must be active, a title match, and calling from the session its subscription records
// (a padded name is the same name). Anyone else is refused as not an Orchestrator.
func TestCheckCrossTeamSender(t *testing.T) {
	e := newCrossTeamEnv(t)
	ctx := context.Background()
	check := func(board, who, session string) (string, string, senderRefusal) {
		t.Helper()
		role, sess, refusal, err := e.h.checkCrossTeamSender(ctx, board, who, session)
		require.NoError(t, err)
		return role, sess, refusal
	}

	role, sess, ref := check("alpha", crossTeamOperatorID, "")
	assert.Equal(t, crossTeamOperatorID, role)
	assert.Equal(t, "", sess)
	assert.Equal(t, senderAllowed, ref)
	_, _, ref = check("nowhere", crossTeamOperatorID, "")
	assert.Equal(t, senderNoBoard, ref)

	role, sess, ref = check("alpha", "Orchestrator", "tmux-alphaOrchestrator")
	assert.Equal(t, crossTeamSenderRole, role)
	assert.Equal(t, "tmux-alphaOrchestrator", sess)
	assert.Equal(t, senderAllowed, ref)
	_, sess, ref = check("alpha", "Orchestrator", "  tmux-alphaOrchestrator \n")
	assert.Equal(t, senderAllowed, ref, "a padded session name is the same name")
	assert.Equal(t, "tmux-alphaOrchestrator", sess, "and the recorded name is what comes back")
	for _, session := range []string{"", "   ", "tmux-betaOrchestrator", "TMUX-ALPHAORCHESTRATOR"} {
		_, _, ref = check("alpha", "Orchestrator", session)
		assert.Equal(t, senderSessionMismatch, ref, "session %q", session)
	}
	_, _, ref = check("alpha", "Dev", "tmux-alphaDev")
	assert.Equal(t, senderNotOrchestrator, ref, "a developer is not an Orchestrator")
	_, _, ref = check("alpha", "Nobody", "x")
	assert.Equal(t, senderNotOrchestrator, ref, "nor is someone unknown")
	_, _, ref = check("gamma", "Orchestrator", "tmux-alphaOrchestrator")
	assert.Equal(t, senderNotOrchestrator, ref, "nor on a board it is not on")
	ok, err := e.h.bs.Unsubscribe(ctx, "alpha", "Orchestrator")
	require.NoError(t, err)
	require.True(t, ok)
	_, _, ref = check("alpha", "Orchestrator", "tmux-alphaOrchestrator")
	assert.Equal(t, senderNotOrchestrator, ref, "an inactive Orchestrator may not send")
}

// The rate wait is whole seconds, rounded up, and the bucket is per key.
func TestCrossTeamWait(t *testing.T) {
	e := newCrossTeamEnv(t)
	var limited bool
	var secs int
	n := 0
	for ; n < 1000; n++ {
		if secs, limited = e.h.crossTeamWait("a\x00b\x00c"); limited {
			break
		}
	}
	require.True(t, limited, "the sender is limited after a burst")
	assert.GreaterOrEqual(t, secs, 1)
	_, other := e.h.crossTeamWait("another\x00sender\x00key")
	assert.False(t, other, "another sender's bucket is separate")
	e.now = e.now.Add(time.Hour)
	_, again := e.h.crossTeamWait("a\x00b\x00c")
	assert.False(t, again, "and it refills")
}

// An attachment is sent as the Operator only when a person is the Operator; as the sender's own
// session only when the verified session is the one named; else from the shared bucket.
func TestAttachSenderFor(t *testing.T) {
	e := newCrossTeamEnv(t)
	r := httptest.NewRequest("POST", "/", nil)
	s := e.h.attachSenderFor(context.Background(), r, "alpha", "Orchestrator", "tmux-alphaOrchestrator")
	assert.False(t, s.person)
	assert.Equal(t, sharedUploadBucket, s.windowKey, "no verified session: the shared bucket")
	assert.Empty(t, s.sessionID)
	s = e.h.attachSenderFor(context.Background(), r, "alpha", crossTeamOperatorID, "")
	assert.False(t, s.person, "an anonymous caller is not the Operator")
	assert.Equal(t, sharedUploadBucket, s.windowKey)
}

// With a verified session, the attachment is sent as that session, in its team, only when it is the
// Orchestrator's own: the one named in the request.
func TestAttachSenderForVerifiedSession(t *testing.T) {
	e := newCrossTeamEnv(t)
	with := func(owner *store.SessionTokenOwner) *http.Request {
		r := httptest.NewRequest("POST", "/", nil)
		return r.WithContext(context.WithValue(r.Context(), verifiedSessionKey{}, owner))
	}
	own := &store.SessionTokenOwner{SessionID: "sid-a", SessionName: "tmux-alphaOrchestrator"}
	r := with(own)
	s := e.h.attachSenderFor(r.Context(), r, "alpha", "Orchestrator", "tmux-alphaOrchestrator")
	assert.Equal(t, "sid-a", s.sessionID)
	assert.Equal(t, "session:sid-a", s.windowKey)

	s = e.h.attachSenderFor(r.Context(), r, "alpha", "Orchestrator", "tmux-alphaDev")
	assert.Equal(t, sharedUploadBucket, s.windowKey, "another session's name: the shared bucket")
	s = e.h.attachSenderFor(r.Context(), r, "alpha", "Orchestrator", "")
	assert.Equal(t, sharedUploadBucket, s.windowKey, "no session named: the shared bucket")

	// A verified session with no name of its own does not match an empty name.
	r = with(&store.SessionTokenOwner{SessionID: "sid-b"})
	s = e.h.attachSenderFor(r.Context(), r, "alpha", "Orchestrator", "")
	assert.Equal(t, sharedUploadBucket, s.windowKey)
	assert.Empty(t, s.sessionID)
	// And one without an id is not a session at all.
	r = with(&store.SessionTokenOwner{SessionName: "tmux-alphaOrchestrator"})
	s = e.h.attachSenderFor(r.Context(), r, "alpha", "Orchestrator", "tmux-alphaOrchestrator")
	assert.Equal(t, sharedUploadBucket, s.windowKey)
}
