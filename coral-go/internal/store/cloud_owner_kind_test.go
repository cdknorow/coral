package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const teamOrgID = "0b9f1c2e-3d4a-4b5c-8d6e-7f8091a2b3c4"

// #7061: an enrolment is personal unless the hub enrolled it for an organisation; the record is written with the enrolment, read back exactly, and a new enrolment never inherits it.
func TestOwnerRecordPersonalTeamAndReenrolment(t *testing.T) {
	db := openTestDB(t)
	cs := NewCloudStore(db, NewRemoteServerStore(db, t.TempDir()))
	ctx := t.Context()
	const token = "coralhub_srv_abcd1234_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"

	kind, org, err := cs.OwnerRecord(ctx)
	require.NoError(t, err)
	assert.Equal(t, OwnerKindPersonal, kind, "no enrolment: nothing is a team's")
	assert.Empty(t, org)

	require.NoError(t, cs.Save(ctx, "https://hub.example.invalid", "srv_1", "L", token))
	kind, org, err = cs.OwnerRecord(ctx)
	require.NoError(t, err)
	assert.Equal(t, OwnerKindPersonal, kind)
	assert.Empty(t, org)

	require.NoError(t, cs.SaveTeam(ctx, "https://hub.example.invalid", "srv_2", "L", token, teamOrgID))
	kind, org, err = cs.OwnerRecord(ctx)
	require.NoError(t, err)
	assert.Equal(t, OwnerKindTeam, kind)
	assert.Equal(t, teamOrgID, org)
	got, err := cs.Token(ctx)
	require.NoError(t, err)
	assert.Equal(t, token, got, "the token is stored as with Save")

	// A new personal enrolment clears the team record; a team one replaces it.
	require.NoError(t, cs.Save(ctx, "https://hub.example.invalid", "srv_3", "L", token))
	kind, org, err = cs.OwnerRecord(ctx)
	require.NoError(t, err)
	assert.Equal(t, OwnerKindPersonal, kind)
	assert.Empty(t, org, "a re-enrolment never inherits a team record")
	require.NoError(t, cs.SaveTeam(ctx, "https://hub.example.invalid", "srv_4", "L", token, teamOrgID))
	other := "ffffffff-0000-4000-8000-000000000001"
	require.NoError(t, cs.SaveTeam(ctx, "https://hub.example.invalid", "srv_5", "L", token, other))
	_, org, _ = cs.OwnerRecord(ctx)
	assert.Equal(t, other, org)
}

func TestSaveTeamRefusesAnOrgThatIsNotTheCanonicalUUID(t *testing.T) {
	db := openTestDB(t)
	cs := NewCloudStore(db, NewRemoteServerStore(db, t.TempDir()))
	ctx := t.Context()
	const token = "coralhub_srv_abcd1234_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
	for _, bad := range []string{"", "ORG", "0B9F1C2E-3D4A-4B5C-8D6E-7F8091A2B3C4", "0b9f1c2e3d4a4b5c8d6e7f8091a2b3c4", teamOrgID + "x"} {
		require.Error(t, cs.SaveTeam(ctx, "https://hub.example.invalid", "srv_1", "L", token, bad), "%q", bad)
	}
	_, err := cs.Get(ctx)
	assert.ErrorIs(t, err, ErrNoCloudConnection, "a refused enrolment stores nothing")
}

// A stored record this code does not understand is an error, never personal: the owner check then refuses.
func TestOwnerRecordNeverReadsAnUnknownRecordAsPersonal(t *testing.T) {
	db := openTestDB(t)
	cs := NewCloudStore(db, NewRemoteServerStore(db, t.TempDir()))
	ctx := t.Context()
	const token = "coralhub_srv_abcd1234_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
	require.NoError(t, cs.Save(ctx, "https://hub.example.invalid", "srv_1", "L", token))
	for name, c := range map[string]struct{ kind, org any }{
		"another kind":           {"organisation", teamOrgID},
		"team without an org":    {"team", nil},
		"team with an empty org": {"team", ""},
		"team, upper-case org":   {"team", "0B9F1C2E-3D4A-4B5C-8D6E-7F8091A2B3C4"},
		"personal with an org":   {"personal", teamOrgID},
		"no kind, an org":        {nil, teamOrgID},
		"another case":           {"Team", teamOrgID},
	} {
		_, err := db.ExecContext(ctx, "UPDATE cloud_connection SET hub_owner_kind = ?, hub_owner_org = ? WHERE id = 1", c.kind, c.org)
		require.NoError(t, err, name)
		kind, org, err := cs.OwnerRecord(ctx)
		require.Error(t, err, name)
		assert.Empty(t, kind, name)
		assert.Empty(t, org, name)
	}
	// An enrolment made before the columns existed has NULLs: personal.
	_, err := db.ExecContext(ctx, "UPDATE cloud_connection SET hub_owner_kind = NULL, hub_owner_org = NULL WHERE id = 1")
	require.NoError(t, err)
	kind, _, err := cs.OwnerRecord(ctx)
	require.NoError(t, err)
	assert.Equal(t, OwnerKindPersonal, kind)
}
