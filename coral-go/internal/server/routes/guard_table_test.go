package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/store/storetest"
)

var guardParam = regexp.MustCompile(`\{[^}]*\}|\*`)

// guardConcretePath fills each parameter of a route pattern with a plain value.
func guardConcretePath(pattern string) string { return guardParam.ReplaceAllString(pattern, "x1") }

// #3670: every entry of the tables is matched wherever its path is. Outside strict mode each
// agent call to an Operator-only route is counted once, by route (the artifact routes of
// #3553/#3554 never were: the hand-kept prefix list missed /api/artifacts/); in strict mode each
// is refused.
func TestGuardMatchesEveryTableEntry(t *testing.T) {
	for _, strict := range []bool{false, true} {
		db, err := storetest.Open(t, filepath.Join(t.TempDir(), "s.db"))
		require.NoError(t, err)
		token, err := db.MintSessionToken(context.Background(), "sid-dev", "claude-dev")
		require.NoError(t, err)
		o := NewIdentityObserver(db, nil, func(*http.Request) bool { return false })
		if strict {
			o.SetVerifiers(func(*http.Request) bool { return false }, func(r *http.Request) (*http.Request, bool) { return r, false })
			o.SetStrict()
		}
		r := chi.NewRouter()
		r.Use(o.Middleware)
		ok := func(w http.ResponseWriter, r *http.Request) {}
		keys := OperatorOnlyRouteKeys()
		for k := range recordedAgentRoutes {
			keys = append(keys, k)
		}
		for _, k := range keys {
			method, pattern, _ := strings.Cut(k, " ")
			r.MethodFunc(method, pattern, ok)
		}
		require.NoError(t, ValidateGuardedRoutes(r))
		for _, k := range keys {
			method, pattern, _ := strings.Cut(k, " ")
			req := httptest.NewRequest(method, guardConcretePath(pattern), strings.NewReader("{}"))
			req.Header.Set(store.SessionTokenHeader, token)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			_, only := operatorOnlyRoutes[k]
			if strict && only {
				assert.Equal(t, http.StatusForbidden, rec.Code, k)
			} else {
				assert.Equal(t, http.StatusOK, rec.Code, k)
			}
		}
		calls := o.Report()["agent_operator_calls"].(map[string]int64)
		for _, k := range keys {
			assert.EqualValues(t, 1, calls["claude-dev "+k], "%s recorded once (strict=%v)", k, strict)
		}
		db.Close()
	}
}

// An entry the router doesn't serve, under that method and pattern, is reported (the server
// refuses to start on it); a malformed key panics when the prefixes are built.
func TestValidateGuardedRoutesNamesAnUnservedEntry(t *testing.T) {
	r := chi.NewRouter()
	ok := func(w http.ResponseWriter, r *http.Request) {}
	for _, k := range OperatorOnlyRouteKeys() {
		method, pattern, _ := strings.Cut(k, " ")
		if k == "POST /api/artifacts/prune" {
			r.MethodFunc(http.MethodPut, pattern, ok) // the right path, the wrong method
			continue
		}
		r.MethodFunc(method, pattern, ok)
	}
	for k := range recordedAgentRoutes {
		method, pattern, _ := strings.Cut(k, " ")
		r.MethodFunc(method, pattern, ok)
	}
	err := ValidateGuardedRoutes(r)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "POST /api/artifacts/prune")

	for _, bad := range []string{"/api/x", "post /api/x", "POST api/x", "POST /api/x y", "FETCH /api/x", "POST\t/api/x"} {
		_, _, ok := splitGuardKey(bad)
		assert.False(t, ok, bad)
	}
	_, _, good := splitGuardKey("DELETE /api/artifacts/{id}/keep")
	assert.True(t, good)
}
