package routes

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cdknorow/coral/internal/server/proxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRemoteResolver_UnknownServer(t *testing.T) {
	e := newRSEnv(t)
	_, err := NewRemoteResolver(e.h.Store()).Resolve(context.Background(), "nope")
	assert.True(t, errors.Is(err, proxy.ErrUnknownServer), "err = %v", err)
}

// A server registered through the registry API is reachable through the
// proxy, with the stored (decrypted) key attached.
func TestRemoteResolver_ProxyUsesRegisteredKey(t *testing.T) {
	e := newRSEnv(t)
	rejectKeys.Store(false)
	remote := fakeRemote(t, routeSecret)

	w := e.do("POST", "/api/servers", map[string]any{
		"id": "ws", "label": "Workstation", "url": remote.URL, "api_key": routeSecret, "allow_private": true})
	require.Equal(t, 201, w.Code, w.Body.String())

	MountRemoteProxy(e.router, NewRemoteResolver(e.h.Store()))
	req := httptest.NewRequest(http.MethodGet, "/api/remote/ws/api/health", nil)
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	assert.Equal(t, 200, rec.Code, rec.Body.String())

	req = httptest.NewRequest(http.MethodGet, "/api/remote/missing/api/health", nil)
	rec = httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	assert.Equal(t, 404, rec.Code)
}
