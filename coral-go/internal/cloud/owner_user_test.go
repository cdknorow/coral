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
