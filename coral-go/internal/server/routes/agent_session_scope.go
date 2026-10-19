package routes

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/cdknorow/coral/internal/naming"
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

// agentMaySeeSession is whether the agent whose token this is may read the session the request is about: itself, a session of its own resume
// lineage, or a session on the same board. The request names it by the path (a name or an id) and, on the live routes, by session_id, which the
// handlers check against the name (validateExactLiveTarget: the name is the stored agent name or the tmux name of that exact session). So:
//   - the session_id's session and a name that is that session's own name (or its tmux name) are ONE target: judged once, however many other
//     sessions share the folder name (#5019: two boards can share a folder name);
//   - any other name is judged as the session it resolves to: an id, a tmux-style name (claude-<uuid>: the id is parsed out), or the FIRST live
//     session of that name (the stored agent name), which is the one a handler would pick. A display name or a board title is NOT resolved: no
//     handler resolves one, and either is set by the agent or a person, so counting it as "me" would let a name that is also another board's
//     folder name read that board (#5019 review);
//   - a name that resolves to nothing known reads nothing by itself, so it passes only beside a target that is the agent's own or allowed.
//
// An error is a lookup that failed: the request is refused, never let through.
func (o *IdentityObserver) agentMaySeeSession(ctx context.Context, r *http.Request, owner *store.SessionTokenOwner, key string) (bool, error) {
	ss := store.NewSessionStore(o.db)
	all, err := ss.GetAllLiveSessions(ctx)
	if err != nil {
		return false, err
	}
	var own *store.LiveSession
	if own, err = ss.GetLiveSession(ctx, owner.SessionID); err != nil {
		return false, err
	}
	lookup := func(c string) (*store.LiveSession, error) {
		if ls, err := ss.GetLiveSession(ctx, c); err != nil || ls != nil {
			return ls, err
		}
		if id := naming.SessionIDFromName(c); id != c { // claude-<uuid>: the tmux name of a session
			if _, perr := uuid.Parse(id); perr == nil {
				if ls, err := ss.GetLiveSession(ctx, id); err != nil || ls != nil {
					return ls, err
				}
			}
		}
		for i := range all {
			if all[i].AgentName == c {
				return &all[i], nil
			}
		}
		return nil, nil
	}

	candidates := []string{key}
	if sid := r.URL.Query().Get("session_id"); sid != "" && sid != key {
		exact, err := ss.GetLiveSession(ctx, sid)
		if err != nil {
			return false, err
		}
		if exact != nil && (key == exact.AgentName || key == naming.SessionName(exact.AgentType, exact.SessionID)) {
			candidates = []string{sid} // the pair names one session
		} else {
			candidates = append(candidates, sid)
		}
	}
	var lineage map[string]bool
	allowed, unknown := 0, 0
	for _, c := range candidates {
		if c == owner.SessionID || c == owner.SessionName {
			allowed++
			continue
		}
		target, err := lookup(c)
		if err != nil {
			return false, err
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
