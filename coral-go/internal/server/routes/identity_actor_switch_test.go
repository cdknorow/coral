package routes

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/store/storetest"
	"github.com/cdknorow/coral/pkg/tunnel"
)

// identity_actor is the Operator's switch (#4255): only the verified Operator on this computer may
// change it, it defaults to log, and the generic settings PUT can't write it.
func TestIdentityActorSwitchIsOperatorOnly(t *testing.T) {
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "c.db"))
	require.NoError(t, err)
	defer db.Close()
	ss := store.NewSessionStore(db)
	h := NewCloudHandler(ss, t.TempDir())
	was := identityActorEnforced()
	setIdentityActorEnforced(false)
	t.Cleanup(func() { setIdentityActorEnforced(was) })

	put := func(a *Actor, via bool, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("PUT", "http://127.0.0.1:8420/api/system/identity-actor", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if via {
			r.Header.Set(tunnel.ViaHeader, "x")
		}
		if a != nil {
			r = withActor(r, *a)
		}
		rec := httptest.NewRecorder()
		h.SetIdentityActor(rec, r)
		return rec
	}
	op := &Actor{Kind: ActorOperator, Name: "Operator"}
	enforce := `{"mode":"enforce"}`

	for name, c := range map[string]struct {
		a   *Actor
		via bool
	}{
		"an agent token":                 {&Actor{Kind: ActorToken, Name: "Lead", Session: "s"}, false},
		"a claimed caller":               {&Actor{Kind: ActorClaimed}, false},
		"no actor at all":                {nil, false},
		"the Operator through Coral Hub": {op, true},
		"a Hub actor named the Operator": {&Actor{Kind: ActorHub, Name: "Operator"}, false},
	} {
		rec := put(c.a, c.via, enforce)
		assert.Equal(t, http.StatusForbidden, rec.Code, name)
		assert.False(t, identityActorEnforced(), name+" must not turn enforce on")
	}
	settings, err := ss.GetSettings(t.Context())
	require.NoError(t, err)
	assert.NotContains(t, settings, IdentityActorSetting, "a refused request writes nothing")

	assert.Equal(t, http.StatusBadRequest, put(op, false, `{"mode":"yes"}`).Code)
	assert.False(t, identityActorEnforced())

	require.Equal(t, http.StatusOK, put(op, false, enforce).Code)
	assert.True(t, identityActorEnforced())
	setIdentityActorEnforced(false)
	LoadIdentityActor(t.Context(), ss)
	assert.True(t, identityActorEnforced(), "the stored mode is applied at startup")

	require.Equal(t, http.StatusOK, put(op, false, `{"mode":"log"}`).Code)
	assert.False(t, identityActorEnforced())
	setIdentityActorEnforced(true)
	LoadIdentityActor(t.Context(), ss)
	assert.False(t, identityActorEnforced(), "log is the stored mode")
}

func TestIdentityActorIsNotWrittenByTheSettingsPut(t *testing.T) {
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "c.db"))
	require.NoError(t, err)
	defer db.Close()
	h := NewSystemHandler(db, nil)
	for _, k := range []string{"identity_actor", "Identity_Actor", " IDENTITY_ACTOR "} {
		r := httptest.NewRequest("PUT", "/api/settings", strings.NewReader(`{"`+k+`":"enforce"}`))
		r.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.PutSettings(rec, r)
		assert.Equal(t, http.StatusBadRequest, rec.Code, k)
	}
}
