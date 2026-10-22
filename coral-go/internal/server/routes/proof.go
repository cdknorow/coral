package routes

import (
	"context"
	"github.com/subgentic/coral/internal/httputil"
	"net/http"
	"sync"
	"time"

	"github.com/subgentic/coral/internal/auth"
	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/serverproof"
)

// The proof that this is Coral (#6103): a tool on this computer asks it on a new connection, before it sends a credential on it. See package serverproof. It is public (the tool has nothing to
// give before it knows who it talks to) and returns only an HMAC of a well-formed challenge; it is never reachable through Coral Hub.

// proofRate bounds challenges from this computer, per identity: they are cheap, and unauthenticated. The operator's and each live session's have their own bucket, so one tool's flood cannot make
// another's challenges fail; every challenge Coral has no key for (a made-up hash) shares one small bucket.
var proofRate = newProofLimiter(200, 400, 20, 40, 1024)

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

// Proof: GET /api/system/proof?n=<32 hex>&k=operator, or &k=session&h=<sha256 of the session token>. Only this computer's own processes get an answer: anyone else (a host on the network, when
// remote access is on) gets a 404 before any rate bucket is touched, so nobody off this computer can use up the challenges its tools need.
func (h *CloudHandler) Proof() http.Handler {
	return h.proof(proofRate)
}

func (h *CloudHandler) proof(limiter *proofLimiter) http.Handler {
	inner := serverproof.Handler(h.ProofKeys, limiter.allow, func(w http.ResponseWriter) {
		httputil.WriteActionLimit(w, 1, "proof_limit", "too many proof requests from this computer right now; try again in a few seconds")
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cloud.Tunneled(r) {
			http.Error(w, "not available through Coral Hub", http.StatusForbidden)
			return
		}
		if !auth.IsThisComputerPeer(r) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		inner.ServeHTTP(w, r)
	})
}

// proofLimiter is a bucket for each identity a challenge names ("operator", "session:<hash>"; at most maxIdentities of them) and one for every challenge with no identity (""): when the map is full
// of identities that are still busy, a new one shares the unknown bucket rather than growing it.
type proofLimiter struct {
	mu            sync.Mutex
	known         map[string]*proofBucket
	unknown       *proofBucket
	perSec, burst float64
	maxIdentities int
}

func newProofLimiter(perSec, burst, unknownPerSec, unknownBurst float64, maxIdentities int) *proofLimiter {
	return &proofLimiter{known: map[string]*proofBucket{}, unknown: newProofBucket(unknownPerSec, unknownBurst), perSec: perSec, burst: burst, maxIdentities: maxIdentities}
}

func (l *proofLimiter) allow(identity string) bool {
	if identity == "" {
		return l.unknown.allow()
	}
	l.mu.Lock()
	b := l.known[identity]
	if b == nil {
		if len(l.known) >= l.maxIdentities {
			for id, old := range l.known { // a bucket that has refilled to its burst holds nothing: forget it
				if old.full() {
					delete(l.known, id)
				}
			}
		}
		if len(l.known) >= l.maxIdentities {
			l.mu.Unlock()
			return l.unknown.allow()
		}
		b = newProofBucket(l.perSec, l.burst)
		l.known[identity] = b
	}
	l.mu.Unlock()
	return b.allow()
}

func (b *proofBucket) full() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.tokens+time.Since(b.last).Seconds()*b.perSecond >= b.burst
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
