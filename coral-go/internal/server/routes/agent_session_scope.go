package routes

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/cdknorow/coral/internal/store"
)

// An agent's token reads its own session and the sessions on its own board, not any other (#5013). These routes are about ONE session
// (its chat, terminal capture, files, diffs, events, history, usage): none is called by an agent tool, and without this check any agent
// could read every other agent's work by name or id, on any board, as an agent could once read other teams' boards by name (#3869).
// The value is the index of the session's name or id in the path ("/api/sessions/live/{name}": api, sessions, live, name -> 3).
// The Operator, the dashboard and the command line (no agent token) are not affected.
var agentSessionRoutes = map[string]int{
	"GET /api/sessions/live/{name}":                        3,
	"GET /api/sessions/live/{name}/capture":                3,
	"GET /api/sessions/live/{name}/chat":                   3,
	"GET /api/sessions/live/{name}/changes.diff":           3,
	"GET /api/sessions/live/{name}/diff":                   3,
	"GET /api/sessions/live/{name}/events":                 3,
	"DELETE /api/sessions/live/{name}/events":              3,
	"GET /api/sessions/live/{name}/events/counts":          3,
	"GET /api/sessions/live/{name}/file-content":           3,
	"GET /api/sessions/live/{name}/file-original":          3,
	"GET /api/sessions/live/{name}/files":                  3,
	"GET /api/sessions/live/{name}/git":                    3,
	"GET /api/sessions/live/{name}/history/context":        3,
	"GET /api/sessions/live/{name}/history/search":         3,
	"GET /api/sessions/live/{name}/info":                   3,
	"GET /api/sessions/live/{name}/poll":                   3,
	"GET /api/sessions/live/{name}/resolve-path":           3,
	"GET /api/sessions/live/{name}/search-files":           3,
	"GET /api/sessions/live/{name}/subagents":              3,
	"GET /api/sessions/live/{name}/subagents/{subagentID}": 3,
	"GET /api/sessions/history/{sessionID}":                3,
	"GET /api/sessions/history/{sessionID}/agent-notes":    3,
	"GET /api/sessions/history/{sessionID}/events":         3,
	"GET /api/sessions/history/{sessionID}/files":          3,
	"GET /api/sessions/history/{sessionID}/git":            3,
	"GET /api/sessions/history/{sessionID}/notes":          3,
	"GET /api/sessions/history/{sessionID}/resume-info":    3,
	"GET /api/sessions/history/{sessionID}/tags":           3,
	"GET /api/sessions/history/{sessionID}/tasks":          3,
	"GET /api/sessions/{sessionID}/changes":                2,
	"GET /api/sessions/{sessionID}/changes.diff":           2,
	"GET /api/sessions/{sessionID}/status":                 2,
	"GET /api/token-usage/session/{sessionID}/turns":       3,
}

// scopedSessionKey says whether r is one of agentSessionRoutes and gives the name or id of the session it is about.
func scopedSessionKey(r *http.Request) (key string, ok bool) {
	p := routedPath(r)
	if !strings.HasPrefix(p, "/api/sessions/") && !strings.HasPrefix(p, "/api/token-usage/session/") {
		return "", false
	}
	rc := chi.RouteContext(r.Context())
	if rc == nil || rc.Routes == nil {
		return "", false
	}
	tctx := chi.NewRouteContext()
	if !rc.Routes.Match(tctx, r.Method, p) {
		return "", false
	}
	idx, ok := agentSessionRoutes[r.Method+" "+tctx.RoutePattern()]
	if !ok {
		return "", false
	}
	seg := strings.Split(strings.Trim(p, "/"), "/")
	if idx >= len(seg) {
		return "", false
	}
	key = seg[idx]
	if d, err := url.PathUnescape(key); err == nil {
		key = d
	}
	return key, key != ""
}

// agentMaySeeSession is whether the agent whose token this is may read the session named by key (and by the request's session_id, which the
// live routes check against the name): itself, a session of its own resume lineage, or a session on the same board. An error is a lookup that
// failed: the request is refused, never let through.
func (o *IdentityObserver) agentMaySeeSession(ctx context.Context, r *http.Request, owner *store.SessionTokenOwner, key string) (bool, error) {
	candidates := []string{key}
	if sid := r.URL.Query().Get("session_id"); sid != "" && sid != key {
		candidates = append(candidates, sid)
	}
	ss := store.NewSessionStore(o.db)
	all, err := ss.GetAllLiveSessions(ctx)
	if err != nil {
		return false, err
	}
	var own *store.LiveSession
	var lineage map[string]bool
	// A candidate that names a session we know must be one the agent may read. One that names nothing we know (a made-up name beside the
	// agent's own session_id) reads nothing by itself, so it passes only when another candidate is the agent's own or allowed.
	allowed, unknown := 0, 0
	for _, c := range candidates {
		if c == owner.SessionID || c == owner.SessionName {
			allowed++
			continue
		}
		target, err := ss.GetLiveSession(ctx, c)
		if err != nil {
			return false, err
		}
		if target == nil {
			for i := range all {
				if all[i].AgentName == c || all[i].SessionID == c {
					target = &all[i]
					break
				}
			}
		}
		if target != nil && target.SessionID == owner.SessionID {
			allowed++
			continue
		}
		if lineage == nil {
			lineage = map[string]bool{}
			anc, err := ss.GetSessionAncestry(ctx, owner.SessionID)
			if err != nil {
				return false, err
			}
			for _, a := range anc {
				lineage[a.SessionID] = true
			}
		}
		if lineage[c] || (target != nil && lineage[target.SessionID]) {
			allowed++
			continue
		}
		if target == nil {
			unknown++
			continue
		}
		if own == nil {
			if own, err = ss.GetLiveSession(ctx, owner.SessionID); err != nil {
				return false, err
			}
		}
		ob, tb := boardOfLive(own), boardOfLive(target)
		if ob == "" || ob != tb {
			return false, nil
		}
		allowed++
	}
	return allowed > 0, nil
}

func boardOfLive(ls *store.LiveSession) string {
	if ls == nil || ls.BoardName == nil {
		return ""
	}
	return *ls.BoardName
}
