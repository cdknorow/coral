package routes

import (
	"context"
	"errors"

	"github.com/subgentic/coral/internal/store"
	proxy "github.com/subgentic/coral/pkg/hubproxy"
)

// RemoteResolver adapts the remote server registry to proxy.Resolver and
// proxy.StatusReporter. Plaintext keys exist only in the returned Target.
type RemoteResolver struct {
	store    *store.RemoteServerStore
	failures FailureSink
}

// FailureSink takes a failure the proxy saw (background.RemoteAgentHub). It
// returns false when it does not track id, and the resolver writes the row.
type FailureSink interface {
	ReportFailure(ctx context.Context, id, status, errMsg string) bool
}

// SetFailureSink routes the proxy's failures through the remote workers, so the
// registry row and the worker's status cannot disagree (#3202).
func (r *RemoteResolver) SetFailureSink(s FailureSink) { r.failures = s }

// NewRemoteResolver wraps the registry store for use by the proxy and poller.
func NewRemoteResolver(s *store.RemoteServerStore) *RemoteResolver {
	return &RemoteResolver{store: s}
}

func (r *RemoteResolver) Resolve(ctx context.Context, id string) (proxy.Target, error) {
	rs, err := r.store.Get(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrRemoteNotFound) {
			return proxy.HTTPTarget{}, proxy.ErrUnknownServer
		}
		return proxy.HTTPTarget{}, err
	}
	key, err := r.store.DecryptedKey(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrKeyUnreadable) {
			return proxy.HTTPTarget{}, proxy.ErrKeyUnreadable
		}
		return proxy.HTTPTarget{}, err
	}
	return proxy.HTTPTarget{URL: rs.URL, APIKey: key, AllowPrivate: rs.AllowPrivate}, nil
}

func (r *RemoteResolver) MarkStatus(ctx context.Context, id, status, errMsg string) {
	if r.failures != nil && r.failures.ReportFailure(ctx, id, status, errMsg) {
		return
	}
	_ = r.store.SetStatus(ctx, id, status, errMsg)
}
