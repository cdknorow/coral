package routes

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/serverproof"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
)

// #6103: the proof route is public, so it must not be a way to stop the tools that need it: only this computer's own processes are answered (anyone else gets a 404 before any rate bucket is
// touched), and each identity has its own bucket, so a flood of made-up hashes leaves the operator's and a real session's challenges answered.
func proofRoute(t *testing.T, limiter *proofLimiter) (call func(remote, query string) int, session string) {
	t.Helper()
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "c.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	h := NewCloudHandler(store.NewSessionStore(db), t.TempDir())
	session, err = db.MintSessionToken(context.Background(), "aaaaaaaa-6103-4000-8000-000000000003", "claude-p")
	require.NoError(t, err)
	handler := h.proof(limiter)
	return func(remote, query string) int {
		r := httptest.NewRequest(http.MethodGet, serverproof.Path+query, nil)
		r.RemoteAddr = remote
		r = r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, net.Addr(&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8420})))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)
		return rec.Code
	}, session
}

func TestTheProofRouteAnswersOnlyThisComputer(t *testing.T) {
	limiter := newProofLimiter(0.001, 3, 0.001, 3, 16)
	call, _ := proofRoute(t, limiter)
	op := "?k=operator&n=" + strings.Repeat("ab", 16)
	for i := 0; i < 50; i++ {
		require.Equal(t, 404, call("203.0.113.5:4000", op), "a host on the network gets nothing")
		require.Equal(t, 404, call("198.51.100.7:4000", op))
	}
	assert.Len(t, limiter.known, 0, "a peer off this computer used no tokens and made no bucket")
	for i := 0; i < 3; i++ {
		assert.Equal(t, 200, call("127.0.0.1:4000", op), "this computer's tool still has all of its tokens")
	}
	assert.Equal(t, 429, call("127.0.0.1:4000", op), "and its own bucket is the bound")
}

func TestAFloodOfMadeUpHashesLeavesTheOperatorAndARealSessionAnswering(t *testing.T) {
	limiter := newProofLimiter(0.001, 5, 0.001, 5, 16)
	call, session := proofRoute(t, limiter)
	nonce := strings.Repeat("cd", 16)
	var limited int
	for i := 0; i < 200; i++ {
		fake := strings.Repeat("0", 60) + string([]byte{'a' + byte(i/16%6), 'a' + byte(i%6), '0' + byte(i%10), '0' + byte(i/10%10)})
		if call("127.0.0.1:4000", "?k=session&n="+nonce+"&h="+fake) == 429 {
			limited++
		}
	}
	assert.Greater(t, limited, 150, "the made-up hashes ran out of their shared bucket")
	assert.Equal(t, 200, call("127.0.0.1:4000", "?k=operator&n="+nonce), "the operator's challenge is answered")
	assert.Equal(t, 200, call("127.0.0.1:4000", "?k=session&n="+nonce+"&h="+serverproof.TokenHash(session)), "so is a real session's")
	assert.Equal(t, 429, call("127.0.0.1:4000", "?k=session&n="+nonce+"&h="+strings.Repeat("9", 64)), "while one more made-up hash is limited: only its own share is gone")
}

func TestTheProofLimiterKeepsItsIdentityMapBounded(t *testing.T) {
	l := newProofLimiter(0.001, 5, 0.001, 5, 4)
	for i := 0; i < 4; i++ {
		require.True(t, l.allow("session:"+strings.Repeat(string(rune('a'+i)), 64)))
	}
	require.True(t, l.allow("operator"), "a fifth identity, while the four are in use, shares the unknown bucket")
	assert.Len(t, l.known, 4, "the map did not grow")
}
