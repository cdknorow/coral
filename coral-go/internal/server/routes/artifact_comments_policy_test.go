package routes

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/subgentic/coral/internal/store"
)

// The whole policy as a table: every caller x action x locked x hub-write flag (#5176). A new row of the doc table that is not here is a missing test.
func TestCommentPolicyTable(t *testing.T) {
	owner := commentCaller{Kind: commentCallerOwner, Issuer: "local", Subject: "me"}
	hub := commentCaller{Kind: commentCallerHub, Issuer: "coral-hub", Subject: "hub-user-3f2a"}
	otherHub := commentCaller{Kind: commentCallerHub, Issuer: "coral-hub", Subject: "hub-user-9999"}
	agent := commentCaller{Kind: commentCallerAgent, Session: "dev"}
	none := commentCaller{}
	mine := func(c commentCaller) *store.ArtifactComment {
		return &store.ArtifactComment{AuthorIssuer: c.Issuer, AuthorSubject: c.Subject}
	}
	theirs := &store.ArtifactComment{AuthorIssuer: "local", AuthorSubject: "someone-else"}
	type row struct {
		who    string
		c      commentCaller
		action string
		target *store.ArtifactComment
		locked bool
		hubOn  bool
		want   bool
	}
	rows := []row{
		// read: every named caller, always (locked or not, flag or not); nobody else
		{"owner", owner, commentRead, nil, false, false, true}, {"owner", owner, commentRead, nil, true, false, true},
		{"hub", hub, commentRead, nil, false, false, true}, {"hub", hub, commentRead, nil, true, false, true},
		{"agent", agent, commentRead, nil, false, false, true}, {"none", none, commentRead, nil, false, true, false},
		// create / reply
		{"owner", owner, commentCreate, nil, false, false, true}, {"owner locked", owner, commentCreate, nil, true, false, false},
		{"hub without a subject, flag on", commentCaller{Kind: commentCallerHub, Issuer: "coral-hub"}, commentCreate, nil, false, true, false},
		{"hub flag off", hub, commentCreate, nil, false, false, false}, {"hub flag on", hub, commentCreate, nil, false, true, true}, {"hub flag on locked", hub, commentCreate, nil, true, true, false},
		{"agent", agent, commentCreate, nil, false, true, false}, {"none", none, commentCreate, nil, false, true, false},
		// edit and delete: own only, the owner included; never when locked
		{"owner own", owner, commentEdit, mine(owner), false, false, true}, {"owner others'", owner, commentEdit, theirs, false, false, false},
		{"owner own delete", owner, commentDelete, mine(owner), false, false, true}, {"owner others' delete", owner, commentDelete, theirs, false, false, false},
		{"owner own delete locked", owner, commentDelete, mine(owner), true, false, true}, {"owner own edit locked", owner, commentEdit, mine(owner), true, false, false},
		{"owner others' delete locked", owner, commentDelete, theirs, true, false, false}, {"hub own delete locked flag on", hub, commentDelete, mine(hub), true, true, false},
		{"hub own flag on", hub, commentEdit, mine(hub), false, true, true}, {"hub own delete flag on", hub, commentDelete, mine(hub), false, true, true},
		{"hub others' delete", hub, commentDelete, mine(otherHub), false, true, false}, {"hub deleting the owner's", hub, commentDelete, theirs, false, true, false},
		{"hub own flag off", hub, commentEdit, mine(hub), false, false, false},
		{"agent own", agent, commentDelete, mine(agent), false, true, false},
		// resolve and reopen
		{"owner", owner, commentResolve, nil, false, false, true}, {"owner locked", owner, commentResolve, nil, true, false, false},
		{"hub flag on", hub, commentResolve, nil, false, true, true}, {"hub without a subject resolves nothing", commentCaller{Kind: commentCallerHub, Issuer: "coral-hub"}, commentResolve, nil, false, true, false}, {"hub flag off", hub, commentResolve, nil, false, false, false}, {"agent", agent, commentResolve, nil, false, true, false},
		// lock: the owner only, locked or not
		{"owner", owner, commentLock, nil, false, false, true}, {"owner while locked", owner, commentLock, nil, true, false, true},
		{"hub", hub, commentLock, nil, false, true, false}, {"agent", agent, commentLock, nil, false, true, false}, {"none", none, commentLock, nil, false, true, false},
	}
	for _, r := range rows {
		name := fmt.Sprintf("%s/%s/locked=%v/hub=%v", r.who, r.action, r.locked, r.hubOn)
		got, why := commentPolicy(r.c, r.action, r.target, r.locked, r.hubOn)
		assert.Equal(t, r.want, got, name)
		if !got {
			assert.NotEmpty(t, why, name+": a refusal says why")
		} else {
			assert.Empty(t, why, name)
		}
	}
	// A caller without an identity owns nothing: an empty subject matches no comment, not even one with an empty author.
	blank := commentCaller{Kind: commentCallerOwner}
	ok, _ := commentPolicy(blank, commentDelete, &store.ArtifactComment{}, false, false)
	assert.False(t, ok, "an owner with no subject owns nothing")
	// Same subject, different issuer: not the same author.
	elsewhere := commentCaller{Kind: commentCallerOwner, Issuer: "https://app.example", Subject: "me"}
	ok, _ = commentPolicy(elsewhere, commentDelete, mine(owner), false, false)
	assert.False(t, ok, "the issuer is part of the identity")
	// An unknown action is refused.
	ok, _ = commentPolicy(owner, "teleport", nil, false, false)
	assert.False(t, ok)
}
