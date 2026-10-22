package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/pkg/tunnel"
)

// #6108 on the real router: a computer enrolled before the owner label existed refuses its owner (owner_label_missing); Confirm owner arms, the first owner-only request adopts and is served,
// the label is never replaced, and none of it is reachable through Coral Hub.
func TestAComputerLinkedBeforeTheOwnerLabelCanConfirmItsOwner(t *testing.T) {
	r := newE2ERemote(t, "owner-adopt", nil)
	h := r.srv.Router()
	js := map[string]string{"Content-Type": "application/json"}
	ctx := t.Context()
	r.srv.cloudHandler.SetLinkedCheck(func() bool { return true })
	r.srv.cloudHandler.SetConnectedCheck(func() bool { return true })
	cs := store.NewCloudStore(r.db, store.NewRemoteServerStore(r.db, r.cfg.CoralDir()))
	require.NoError(t, cs.Save(ctx, "https://hub.example.invalid", "srv_1", "L", "coralhub_srv_abcd1234_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")) // no owner label: an older enrolment
	require.Equal(t, 200, call(h, "PUT", "/api/cloud/max-mode", `{"mode":"launch"}`, js).Code)

	owner := map[string]string{tunnel.ViaHeader: cloud.ViaSecret(), tunnel.HubRoleHeader: "owner", tunnel.HubUserHeader: "hub-user-1234abcd", "Content-Type": "application/json"}
	other := map[string]string{tunnel.ViaHeader: cloud.ViaSecret(), tunnel.HubRoleHeader: "owner", tunnel.HubUserHeader: "hub-user-9999ffff", "Content-Type": "application/json"}
	const route = "/api/settings/prompt-inspection?board=team" // an owner-only route

	rec := call(h, "GET", route, "", owner)
	assert.Equal(t, 403, rec.Code)
	assert.Contains(t, rec.Body.String(), "owner_label_missing")
	assert.Contains(t, rec.Body.String(), "Confirm owner")

	// Not through Coral Hub: the Hub's own stamped request cannot arm, and sees no route for it.
	assert.Equal(t, 403, call(h, "POST", "/api/cloud/owner-label/arm", `{}`, owner).Code)
	assert.Equal(t, 403, call(h, "GET", "/api/cloud/owner-label", "", owner).Code)
	l, err := cs.OwnerUser(ctx)
	require.NoError(t, err)
	assert.Empty(t, l)

	// The person at this computer arms; the first owner request in the minute is adopted and served.
	arm := call(h, "POST", "/api/cloud/owner-label/arm", `{}`, js)
	require.Equal(t, 200, arm.Code, arm.Body.String())
	assert.Equal(t, 200, call(h, "GET", route, "", owner).Code, "the owner is served as the label is adopted")
	l, err = cs.OwnerUser(ctx)
	require.NoError(t, err)
	assert.Equal(t, "hub-user-1234abcd", l)
	at, err := cs.OwnerAdoptedAt(ctx)
	require.NoError(t, err)
	assert.NotEmpty(t, at, "recorded as adopted locally")

	// From now on it is an ordinary recorded label: another owner-stamped account is refused, a second arm is refused, nothing is replaced.
	assert.Equal(t, 403, call(h, "GET", route, "", other).Code)
	assert.Equal(t, 409, call(h, "POST", "/api/cloud/owner-label/arm", `{}`, js).Code)
	l, _ = cs.OwnerUser(ctx)
	assert.Equal(t, "hub-user-1234abcd", l)

	// The store itself never replaces a recorded label, whatever calls it, and a paired label (PinOwnerUser) is not "adopted".
	ok, err := cs.AdoptOwnerUser(ctx, "srv_1", "hub-user-9999ffff", "2026-10-22T12:00:00Z")
	require.NoError(t, err)
	assert.False(t, ok)
	l, _ = cs.OwnerUser(ctx)
	assert.Equal(t, "hub-user-1234abcd", l)
	ok, err = cs.AdoptOwnerUser(ctx, "srv_other", "hub-user-9999ffff", "2026-10-22T12:00:00Z")
	require.NoError(t, err)
	assert.False(t, ok, "and only for the enrolment it names")
}
