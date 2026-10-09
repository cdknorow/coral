package routes

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/cdknorow/coral/internal/server/proxy"
)

// MountRemoteProxy registers the multi-server hub proxy on r:
//
//	/api/remote/{server}/*  ->  {remote url}/*   (HTTP, SSE and WebSocket)
//
// resolver maps a server id to its URL and decrypted key (see proxy.Resolver);
// if it also implements proxy.StatusReporter, proxy failures update the
// server's status. The route must be registered behind the hub's normal auth
// middleware.
func MountRemoteProxy(r chi.Router, resolver proxy.Resolver) {
	h := proxy.New(resolver)
	r.Handle("/api/remote/{server}/*", http.Handler(h))
}
