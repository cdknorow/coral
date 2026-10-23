package routes

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
)

// #6357 stage 3 (#6455): an agent's reply into a thread it was asked into. The ask is the gate; the check and the count are one step.

func asAgentSession(n int) map[string]string { return map[string]string{testAgentHeader: sendSid(n)} }

func (rig *cmtRig) reply(cid int64, h map[string]string, body string) (int, map[string]any) {
	return rig.do("POST", fmt.Sprintf("%s/%d/reply", cp(cmtDoc), cid), h, map[string]any{"body": body})
}

func TestAnAskedAgentRepliesIntoTheThreadAsItself(t *testing.T) {
	rig := newCmtRig(t)
	rig.agent(1, "Dev", false)
	cid := rig.ownerComment("Try 5,000 rows per batch.")
	_, out := rig.send(cid, "Dev")
	require.Equal(t, "sent", states(out)["Dev"])

	code, rep := rig.reply(cid, asAgentSession(1), "Done: 5,000 is fine.")
	require.Equal(t, http.StatusCreated, code, fmt.Sprint(rep))
	assert.Equal(t, "Done: 5,000 is fine.", rep["body"])
	assert.Equal(t, float64(cid), rep["parent_id"])
	author := rep["author"].(map[string]any)
	assert.Equal(t, "agent", author["kind"], "labelled as an agent's")
	assert.Equal(t, sendSid(1), author["subject"], "the session its token names")
	assert.Equal(t, "Dev", author["name"])
	// the owner sees it in the thread
	_, list := rig.do("GET", cp(cmtDoc), cmtAsOwner, nil)
	assert.Len(t, list["comments"], 2)
}

func TestAnAgentThatWasNotAskedCannotReply(t *testing.T) {
	rig := newCmtRig(t)
	rig.agent(1, "Dev", false)
	rig.agent(2, "QA", false)
	cid := rig.ownerComment("hello")
	// nobody asked
	code, out := rig.reply(cid, asAgentSession(1), "uninvited")
	assert.Equal(t, http.StatusForbidden, code, fmt.Sprint(out))
	// Dev was asked, QA was not
	_, _ = rig.send(cid, "Dev")
	code, out = rig.reply(cid, asAgentSession(2), "me too")
	assert.Equal(t, http.StatusForbidden, code, fmt.Sprint(out))
	assert.Equal(t, "not_asked", out["code"])
	// a thread it was not asked into
	other := rig.ownerComment("another thread")
	code, _ = rig.reply(other, asAgentSession(1), "wrong thread")
	assert.Equal(t, http.StatusForbidden, code)
	_, list := rig.do("GET", cp(cmtDoc), cmtAsOwner, nil)
	assert.Len(t, list["comments"], 2, "no reply was stored")
}

func TestOnlyAnAgentRepliesHereAndNeverThroughCoralHub(t *testing.T) {
	rig := newCmtRig(t)
	rig.agent(1, "Dev", false)
	cid := rig.ownerComment("hello")
	_, _ = rig.send(cid, "Dev")
	rig.hubOn()
	for name, h := range map[string]map[string]string{"the owner": cmtAsOwner, "a hub viewer": cmtAsHub, "a forged stamp": cmtAsForged, "the owner through the tunnel": cmtAsOwnerThroughTunnel, "nobody": nil} {
		code, out := rig.reply(cid, h, "text")
		assert.Contains(t, []int{http.StatusForbidden, http.StatusUnauthorized}, code, "%s: %v", name, out)
	}
	_, list := rig.do("GET", cp(cmtDoc), cmtAsOwner, nil)
	assert.Len(t, list["comments"], 1)
}

func TestTheSixthReplyIsRefusedAndTheLimitHoldsForRepliesAtOnce(t *testing.T) {
	rig := newCmtRig(t)
	rig.agent(1, "Dev", false)
	cid := rig.ownerComment("hello")
	_, _ = rig.send(cid, "Dev")
	for i := 1; i <= store.MaxAskReplies; i++ {
		code, out := rig.reply(cid, asAgentSession(1), fmt.Sprintf("reply %d", i))
		require.Equal(t, http.StatusCreated, code, fmt.Sprint(out))
	}
	code, out := rig.reply(cid, asAgentSession(1), "one too many")
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "reply_limit", out["code"])

	// at once, on a fresh thread: exactly the cap is stored
	cid2 := rig.ownerComment("second")
	_, _ = rig.send(cid2, "Dev")
	var wg sync.WaitGroup
	var made atomic.Int32
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if code, _ := rig.reply(cid2, asAgentSession(1), "at once"); code == http.StatusCreated {
				made.Add(1)
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, int32(store.MaxAskReplies), made.Load())
}

func TestAReplyIsRefusedWhenTheAskEndedTheAgentSleepsOrTheBoardIsPaused(t *testing.T) {
	rig := newCmtRig(t)
	rig.agent(1, "Dev", false)
	ctx := context.Background()
	cid := rig.ownerComment("hello")
	_, _ = rig.send(cid, "Dev")
	// the thread is resolved: the ask ends
	code, _ := rig.do("POST", fmt.Sprintf("%s/%d/resolve", cp(cmtDoc), cid), cmtAsOwner, nil)
	require.Equal(t, 200, code)
	code, out := rig.reply(cid, asAgentSession(1), "late")
	assert.Equal(t, http.StatusConflict, code, fmt.Sprint(out))
	assert.Equal(t, "ask_ended", out["code"])

	cid2 := rig.ownerComment("second")
	_, _ = rig.send(cid2, "Dev")
	rig.h.boardHandler = &BoardHandler{paused: map[string]bool{"team-a": true}}
	code, out = rig.reply(cid2, asAgentSession(1), "paused")
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "board_paused", out["code"])
	rig.h.boardHandler = nil
	require.NoError(t, rig.ss.SetSessionSleeping(ctx, sendSid(1), true))
	code, _ = rig.reply(cid2, asAgentSession(1), "asleep")
	assert.Equal(t, http.StatusForbidden, code)
	require.NoError(t, rig.ss.SetSessionSleeping(ctx, sendSid(1), false))
	code, _ = rig.reply(cid2, asAgentSession(1), "awake again")
	assert.Equal(t, http.StatusCreated, code)
	// a locked artifact
	code, _ = rig.do("PUT", cp(cmtDoc)+"/lock", cmtAsOwner, map[string]any{"scope": "artifact"})
	require.Equal(t, 200, code)
	code, out = rig.reply(cid2, asAgentSession(1), "locked")
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "comments_locked", out["code"])
}

func TestAReplyIsRedactedBoundedAndTheFailureGivesTheCountBack(t *testing.T) {
	rig := newCmtRig(t)
	rig.agent(1, "Dev", false)
	cid := rig.ownerComment("hello")
	_, _ = rig.send(cid, "Dev")
	secret := "ghp_" + strings.Repeat("a1B2c3D4e5", 4)
	code, rep := rig.reply(cid, asAgentSession(1), "token "+secret)
	require.Equal(t, http.StatusCreated, code, fmt.Sprint(rep))
	assert.NotContains(t, fmt.Sprint(rep["body"]), secret)
	code, _ = rig.reply(cid, asAgentSession(1), strings.Repeat("x", store.MaxAskReplyBytes+1))
	assert.Equal(t, http.StatusRequestEntityTooLarge, code)
	// an empty reply is refused and does not use up one of the five
	code, _ = rig.reply(cid, asAgentSession(1), "   ")
	assert.Equal(t, http.StatusBadRequest, code)
	for i := 0; i < store.MaxAskReplies-1; i++ {
		code, out := rig.reply(cid, asAgentSession(1), fmt.Sprintf("r%d", i))
		require.Equal(t, http.StatusCreated, code, "reply %d: %v", i, out)
	}
	code, _ = rig.reply(cid, asAgentSession(1), "sixth")
	assert.Equal(t, http.StatusConflict, code)
	// a reply to a reply
	_, list := rig.do("GET", cp(cmtDoc), cmtAsOwner, nil)
	var replyID int64
	for _, c := range list["comments"].([]any) {
		if m := c.(map[string]any); m["parent_id"] != nil {
			replyID = int64(m["id"].(float64))
		}
	}
	code, _ = rig.reply(replyID, asAgentSession(1), "nested")
	assert.Equal(t, http.StatusBadRequest, code)
}
