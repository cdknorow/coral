package cloud

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #5280: the enrolment records the owner's hub-user label (the update routes compare every relaunch with it), once, only from the
// enrolment reply, and only when it has the form the hub stamps.
func TestEnrollmentRecordsTheOwnerLabel(t *testing.T) {
	for _, tc := range []struct {
		name, reply, want string
		set               bool
	}{
		{"a label", "hub-user-1234abcd", "hub-user-1234abcd", true},
		{"none sent", "", "", false},
		{"not a label", "owner", "", true},
		{"upper case", "hub-user-1234ABCD", "", true},
		{"too long", "hub-user-1234abcd0", "", true},
		{"a pseudonym with text after", "hub-user-1234abcd\n", "", true},
	} {
		e := newEnv(t)
		if tc.set {
			e.hub.ownerUser.Store(tc.reply)
		}
		e.hub.approveAt.Store(2)
		m := e.manager(e.hub.srv.URL)
		start(t, m)
		require.NoError(t, m.Connect(context.Background()), tc.name)
		waitState(t, m, StateConnected)
		got, err := e.cs.OwnerUser(context.Background())
		require.NoError(t, err, tc.name)
		assert.Equal(t, tc.want, got, tc.name)
	}
}

func TestOwnerLabelIsPinnedOnceAndClearedByANewEnrollment(t *testing.T) {
	e := newEnv(t)
	e.hub.ownerUser.Store("hub-user-1234abcd")
	m := e.manager(e.hub.srv.URL)
	start(t, m)
	ctx := context.Background()
	require.NoError(t, m.Connect(ctx))
	waitState(t, m, StateConnected)
	got, _ := e.cs.OwnerUser(ctx)
	require.Equal(t, "hub-user-1234abcd", got)

	pinned, err := e.cs.PinOwnerUser(ctx, "srv_1", "hub-user-ffffffff")
	require.NoError(t, err)
	assert.False(t, pinned, "a recorded label is never replaced")
	got, _ = e.cs.OwnerUser(ctx)
	assert.Equal(t, "hub-user-1234abcd", got)

	require.NoError(t, e.cs.Save(ctx, e.hub.srv.URL, "srv_1", "again", fakeToken))
	got, _ = e.cs.OwnerUser(ctx)
	assert.Empty(t, got, "a new enrolment starts without the earlier owner's label")
	pinned, err = e.cs.PinOwnerUser(ctx, "srv_2", "hub-user-ffffffff")
	require.NoError(t, err)
	assert.False(t, pinned, "another server's enrolment is not this one")
}

// #7061: a team's enrolment records the organisation with the enrolment and no person (neither the account that pressed Confirm nor its label), the Hello says "team", and a new personal enrolment
// clears the record.
func TestTeamEnrollmentRecordsTheOrgAndNoPersonAndTheHelloSaysTeam(t *testing.T) {
	const org = "0b9f1c2e-3d4a-4b5c-8d6e-7f8091a2b3c4"
	e := newEnv(t)
	e.hub.ownerKind.Store("team")
	e.hub.ownerOrg.Store(org)
	e.hub.ownerUser.Store("hub-user-1234abcd")
	e.hub.account.Store("acct_1")
	e.hub.approveAt.Store(2)
	m := e.manager(e.hub.srv.URL)
	start(t, m)
	ctx := context.Background()
	require.NoError(t, m.Connect(ctx))
	waitState(t, m, StateConnected)

	kind, got, err := e.cs.OwnerRecord(ctx)
	require.NoError(t, err)
	assert.Equal(t, "team", kind)
	assert.Equal(t, org, got)
	label, _ := e.cs.OwnerUser(ctx)
	assert.Empty(t, label, "the person who confirmed is not the team's owner")
	acct, _ := e.cs.Owner(ctx)
	assert.Empty(t, acct, "and is not pinned as the Hub sign-in's owner")
	s := e.hub.session(t)
	assert.Equal(t, "team", s.PeerOwnerKind())
	assert.True(t, s.HasPeerCapability("team_owner/1"))

	require.NoError(t, e.cs.Save(ctx, e.hub.srv.URL, "srv_1", "again", fakeToken))
	kind, got, _ = e.cs.OwnerRecord(ctx)
	assert.Equal(t, "personal", kind)
	assert.Empty(t, got)
}

func TestPersonalEnrollmentsHelloSaysPersonal(t *testing.T) {
	e := newEnv(t)
	e.hub.approveAt.Store(2)
	m := e.manager(e.hub.srv.URL)
	start(t, m)
	require.NoError(t, m.Connect(context.Background()))
	waitState(t, m, StateConnected)
	assert.Equal(t, "personal", e.hub.session(t).PeerOwnerKind())
}

// #7061: Hub sign-in has no owner on a team's machine, whatever was pinned: the record decides, and a record that cannot be understood refuses too.
func TestSigninHasNoOwnerOnATeamMachine(t *testing.T) {
	const org = "0b9f1c2e-3d4a-4b5c-8d6e-7f8091a2b3c4"
	const acct = "11111111-2222-4333-8444-555555555555"
	e := newEnv(t)
	m := e.manager(e.hub.srv.URL)
	ctx := context.Background()

	require.NoError(t, e.cs.Save(ctx, e.hub.srv.URL, "srv_1", "L", fakeToken))
	_, err := e.cs.PinOwner(ctx, "srv_1", acct)
	require.NoError(t, err)
	got, err := m.owner(ctx)
	require.NoError(t, err)
	assert.Equal(t, acct, got, "a personal machine: the pinned account")

	require.NoError(t, e.cs.SaveTeam(ctx, e.hub.srv.URL, "srv_2", "L", fakeToken, org))
	_, err = e.cs.PinOwner(ctx, "srv_2", acct) // even if one were pinned
	require.NoError(t, err)
	_, err = m.owner(ctx)
	assert.ErrorIs(t, err, ErrSigninNoOwner)
	assert.False(t, m.SigninReady(ctx, "http://localhost:8420"))

	// A record that cannot be understood is no owner either.
	require.NoError(t, e.cs.Save(ctx, e.hub.srv.URL, "srv_3", "L", fakeToken))
	_, err = e.cs.PinOwner(ctx, "srv_3", acct)
	require.NoError(t, err)
	_, err = e.db.ExecContext(ctx, "UPDATE cloud_connection SET hub_owner_kind = 'organisation'")
	require.NoError(t, err)
	_, err = m.owner(ctx)
	assert.ErrorIs(t, err, ErrSigninNoOwner)
}
