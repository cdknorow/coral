package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #6108: AdoptOwnerUser is how the person at this computer confirms the owner of an enrolment that recorded none. It records a label only where none is recorded, only for the enrolment it names, and
// marks the label as adopted locally; a label that came with the enrolment (PinOwnerUser) or was adopted before is never replaced, and a new enrolment starts clean.
func TestAdoptOwnerUserNeverReplacesALabelAndIsMarkedLocal(t *testing.T) {
	db := openTestDB(t)
	cs := NewCloudStore(db, NewRemoteServerStore(db, t.TempDir()))
	ctx := t.Context()
	const token = "coralhub_srv_abcd1234_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
	require.NoError(t, cs.Save(ctx, "https://hub.example.invalid", "srv_1", "L", token))

	// No label, not adopted.
	l, err := cs.OwnerUser(ctx)
	require.NoError(t, err)
	assert.Empty(t, l)
	at, err := cs.OwnerAdoptedAt(ctx)
	require.NoError(t, err)
	assert.Empty(t, at)

	// An enrolment this call does not name is not touched.
	ok, err := cs.AdoptOwnerUser(ctx, "srv_other", "hub-user-1234abcd", "2026-10-22T12:00:00Z")
	require.NoError(t, err)
	assert.False(t, ok)
	l, _ = cs.OwnerUser(ctx)
	assert.Empty(t, l)

	// The first adoption records the label and when.
	ok, err = cs.AdoptOwnerUser(ctx, "srv_1", "hub-user-1234abcd", "2026-10-22T12:00:00Z")
	require.NoError(t, err)
	assert.True(t, ok)
	l, _ = cs.OwnerUser(ctx)
	assert.Equal(t, "hub-user-1234abcd", l)
	at, _ = cs.OwnerAdoptedAt(ctx)
	assert.Equal(t, "2026-10-22T12:00:00Z", at)

	// Nothing replaces it: not another adoption, not the same account again with a new time, not a pin from an enrolment reply.
	ok, err = cs.AdoptOwnerUser(ctx, "srv_1", "hub-user-9999ffff", "2026-10-22T13:00:00Z")
	require.NoError(t, err)
	assert.False(t, ok)
	ok, err = cs.AdoptOwnerUser(ctx, "srv_1", "hub-user-1234abcd", "2026-10-22T13:00:00Z")
	require.NoError(t, err)
	assert.False(t, ok, "the same account again changes nothing, not even the time")
	pinned, err := cs.PinOwnerUser(ctx, "srv_1", "hub-user-9999ffff")
	require.NoError(t, err)
	assert.False(t, pinned)
	l, _ = cs.OwnerUser(ctx)
	assert.Equal(t, "hub-user-1234abcd", l)
	at, _ = cs.OwnerAdoptedAt(ctx)
	assert.Equal(t, "2026-10-22T12:00:00Z", at)

	// A new enrolment starts clean: no label, not adopted; and a label that came with the enrolment is not "adopted".
	require.NoError(t, cs.Save(ctx, "https://hub.example.invalid", "srv_2", "L", token))
	l, _ = cs.OwnerUser(ctx)
	assert.Empty(t, l)
	at, _ = cs.OwnerAdoptedAt(ctx)
	assert.Empty(t, at)
	pinned, err = cs.PinOwnerUser(ctx, "srv_2", "hub-user-1234abcd")
	require.NoError(t, err)
	require.True(t, pinned)
	at, _ = cs.OwnerAdoptedAt(ctx)
	assert.Empty(t, at, "a label from the enrolment reply is not recorded as adopted locally")
	ok, err = cs.AdoptOwnerUser(ctx, "srv_2", "hub-user-9999ffff", "2026-10-22T14:00:00Z")
	require.NoError(t, err)
	assert.False(t, ok)
}
