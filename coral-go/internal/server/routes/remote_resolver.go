package routes

import (
	"context"
	"errors"

	"github.com/cdknorow/coral/internal/server/proxy"
	"github.com/cdknorow/coral/internal/store"
)

// RemoteResolver adapts the remote server registry to proxy.Resolver and
// proxy.StatusReporter. Plaintext keys exist only in the returned Target.
type RemoteResolver struct {
	store *store.RemoteServerStore
}

// NewRemoteResolver wraps the registry store for use by the proxy and poller.
func NewRemoteResolver(s *store.RemoteServerStore) *RemoteResolver {
	return &RemoteResolver{store: s}
}

func (r *RemoteResolver) Resolve(ctx context.Context, id string) (proxy.Target, error) {
	rs, err := r.store.Get(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrRemoteNotFound) {
			return proxy.Target{}, proxy.ErrUnknownServer
		}
		return proxy.Target{}, err
	}
	key, err := r.store.DecryptedKey(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrKeyUnreadable) {
			return proxy.Target{}, proxy.ErrKeyUnreadable
		}
		return proxy.Target{}, err
	}
	return proxy.Target{URL: rs.URL, APIKey: key, AllowPrivate: rs.AllowPrivate}, nil
}

func (r *RemoteResolver) MarkStatus(ctx context.Context, id, status, errMsg string) {
	_ = r.store.SetStatus(ctx, id, status, errMsg)
}
