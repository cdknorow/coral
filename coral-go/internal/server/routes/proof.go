package routes

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/serverproof"
)

// The proof that this is Coral (#6103): a tool on this computer asks it on a new connection, before it sends a credential on it. See package serverproof. It is public (the tool has nothing to
// give before it knows who it talks to) and returns only an HMAC of a well-formed challenge; it is never reachable through Coral Hub.

// proofRate bounds challenges from this computer: they are cheap, and unauthenticated.
var proofRate = newProofBucket(200, 400)

// ProofKeys finds the key for a challenge: the command-line token's, or a live session's by the SHA-256 of its token.
func (h *CloudHandler) ProofKeys(ctx context.Context, kind, tokenHash string) ([]byte, bool) {
	switch kind {
	case serverproof.KindOperator:
		return h.cliAuth.ProofKey()
	case serverproof.KindSession:
		if h.ss == nil {
			return nil, false
		}
		return h.ss.DB().SessionProofKey(ctx, tokenHash)
	}
	return nil, false
}

// Proof: GET /api/system/proof?n=<32 hex>&k=operator, or &k=session&h=<sha256 of the session token>.
func (h *CloudHandler) Proof() http.Handler {
	inner := serverproof.Handler(h.ProofKeys, proofRate.allow)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cloud.Tunneled(r) {
			http.Error(w, "not available through Coral Hub", http.StatusForbidden)
			return
		}
		inner.ServeHTTP(w, r)
	})
}

// proofBucket is a token bucket: perSecond refilled each second up to burst.
type proofBucket struct {
	mu        sync.Mutex
	tokens    float64
	burst     float64
	perSecond float64
	last      time.Time
}

func newProofBucket(perSecond, burst float64) *proofBucket {
	return &proofBucket{tokens: burst, burst: burst, perSecond: perSecond, last: time.Now()}
}

func (b *proofBucket) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	b.tokens += now.Sub(b.last).Seconds() * b.perSecond
	if b.tokens > b.burst {
		b.tokens = b.burst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
