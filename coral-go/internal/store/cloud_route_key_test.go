package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3683: the route key belongs to the enrollment with its hub URL. It is returned only for that
// hub, setting it for another hub changes nothing, and a new enrollment (another hub or the
// same one) starts without one.
func TestCloudRouteKeyIsKeptPerHub(t *testing.T) {
	rs, db, _, _ := newRemoteStore(t)
	cs := NewCloudStore(db, rs)
	ctx := context.Background()
	const hub1, hub2, key = "https://hub.example.com", "https://other.example.com", "0123456789abcdef0123456789abcdef"

	k, err := cs.RouteKey(ctx, hub1)
	require.NoError(t, err)
	assert.Empty(t, k, "nothing enrolled")

	require.NoError(t, cs.Save(ctx, hub1+"/", "srv_1", "Laptop", "tok"))
	k, _ = cs.RouteKey(ctx, hub1)
	assert.Empty(t, k, "none stored")
	require.NoError(t, cs.SetRouteKey(ctx, hub1, key))
	k, _ = cs.RouteKey(ctx, hub1+"/")
	assert.Equal(t, key, k)
	k, _ = cs.RouteKey(ctx, hub2)
	assert.Empty(t, k, "never for another hub")
	require.NoError(t, cs.SetRouteKey(ctx, hub2, "ffffffffffffffffffffffffffffffff"))
	k, _ = cs.RouteKey(ctx, hub1)
	assert.Equal(t, key, k, "another hub's key changes nothing")

	require.NoError(t, cs.Save(ctx, hub1, "srv_1", "Laptop", "tok2"))
	k, _ = cs.RouteKey(ctx, hub1)
	assert.Empty(t, k, "a new enrollment starts without one")
	require.NoError(t, cs.SetRouteKey(ctx, hub1, key))
	require.NoError(t, cs.Save(ctx, hub2, "srv_2", "Laptop", "tok3"))
	k, _ = cs.RouteKey(ctx, hub1)
	assert.Empty(t, k)
	k, _ = cs.RouteKey(ctx, hub2)
	assert.Empty(t, k, "the hub changed: dropped")
}
