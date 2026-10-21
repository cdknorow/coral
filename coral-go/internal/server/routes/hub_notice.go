package routes

import (
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/httputil"
	"github.com/subgentic/coral/pkg/tunnel"
)

// Coral Hub's notices about team links (#3489, coral-hub PR #260 section 3.2): when the
// account owner links or unlinks a team on another Coral, the hub tells both boards. The
// text is Coral's own fixed template, filled with a kind and role from a fixed list and a
// peer address checked against the address pattern, so no free text from the hub reaches
// a board. It is posted as "Coral Hub" with origin hub_notice, never as an agent or the
// Operator, and it is information, not an instruction.

// isAuthenticHubNotice reports whether r is Coral Hub's notice: POST .../hub-notice with
// X-Coral-Hub-Notice, stamped by this process's connector.
func isAuthenticHubNotice(r *http.Request) bool {
	// The same route rule the connector forwards the header on.
	return tunnel.ForwardsHubNotice(r.Method, r.URL.Path) &&
		r.Header.Get(tunnel.HubNoticeHeader) != "" && cloud.TunneledAuthentic(r)
}

// Notices are rare (an owner linking or unlinking teams), so a board takes a few at most
// in a burst; and a notice that only repeats the LAST one posted about the same peer
// within hubNoticeRepeat (a hub retry) is not posted again, so a looping hub can't keep
// waking the board's agents. Keyed by board and peer, not by text: a change of state
// (created, revoked, created again) always posts, so the board's last word about a peer
// is always the current one.
const hubNoticeRepeat = 10 * time.Minute

// hubNoticeSeen remembers, per board and peer, the last notice posted and when.
type hubNoticeSeen struct {
	mu   sync.Mutex
	last map[string]struct {
		text string
		at   time.Time
	}
}

// repeats reports whether text is the same as the last notice posted for key within
// hubNoticeRepeat. It doesn't record anything.
func (n *hubNoticeSeen) repeats(key, text string, now time.Time) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	l, ok := n.last[key]
	return ok && l.text == text && now.Sub(l.at) < hubNoticeRepeat
}

// record notes the notice posted for key (only after it is posted), dropping entries
// past the window, so the map stays at most boards x peers.
func (n *hubNoticeSeen) record(key, text string, now time.Time) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.last == nil {
		n.last = map[string]struct {
			text string
			at   time.Time
		}{}
	}
	for k, l := range n.last {
		if now.Sub(l.at) >= hubNoticeRepeat {
			delete(n.last, k)
		}
	}
	n.last[key] = struct {
		text string
		at   time.Time
	}{text, now}
}

// hubNoticeText is the notice for a link kind and this board's role in it.
func hubNoticeText(kind, role, peer string) (string, bool) {
	switch kind + "/" + role {
	case "created/source":
		return fmt.Sprintf("You can now message %s (via Coral Hub).", peer), true
	case "created/target":
		return fmt.Sprintf("%s can now message you (via Coral Hub).", peer), true
	case "revoked/source":
		return fmt.Sprintf("You can no longer message %s (via Coral Hub).", peer), true
	case "revoked/target":
		return fmt.Sprintf("%s can no longer message you (via Coral Hub).", peer), true
	}
	return "", false
}

// PostHubNotice posts Coral Hub's notice about a team link.
// POST /api/board/{project}/hub-notice with X-Coral-Hub-Notice, through Coral Hub only
// {"kind": "created"|"revoked", "role": "source"|"target", "peer": "<server>/<team>"}
func (h *BoardHandler) PostHubNotice(w http.ResponseWriter, r *http.Request) {
	// Only Coral Hub itself: the header, on a request this connector carried. Anyone
	// else, the dashboard and the Operator through Coral Hub included, is refused, so a
	// "Coral Hub" notice can never come from a person.
	if !isAuthenticHubNotice(r) {
		writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "hub_notice_only", "message": "only Coral Hub posts notices here"})
		return
	}
	if r.Header.Get(tunnel.HubUserHeader) != "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "ambiguous_origin", "message": "a notice comes from Coral Hub, never a Coral Hub user"})
		return
	}
	project := urlParam(r, "project")
	var body struct {
		Kind string `json:"kind"`
		Role string `json:"role"`
		Peer string `json:"peer"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad_request", "message": "invalid JSON"})
		return
	}
	if !relayAddressRe.MatchString(body.Peer) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad_request", "message": "peer must be <server>/<team>"})
		return
	}
	text, ok := hubNoticeText(body.Kind, body.Role, body.Peer)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad_request", "message": "kind must be created or revoked, role source or target"})
		return
	}
	ctx := r.Context()
	if exists, err := h.bs.BoardExists(ctx, project); err != nil {
		errInternalServer(w, err.Error())
		return
	} else if !exists {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "not_found"})
		return
	}
	now := time.Now()
	if h.crossTeam.now != nil {
		now = h.crossTeam.now()
	}
	// A repeat of this peer's last notice is checked first (without recording it), then
	// the board's rate limit, and the notice is recorded only once it is posted: a notice
	// refused with 429 posts on the hub's retry instead of being taken for a duplicate.
	// Board + role + peer: a board can be linked both ways with the same peer (one notice
	// per direction), and each direction keeps its own last notice.
	key := project + "\x00" + body.Role + "\x00" + body.Peer
	if h.hubNotices.repeats(key, text, now) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "duplicate": true})
		return
	}
	if ok, wait := h.crossTeam.allow("hub-notice\x00" + project); !ok {
		secs := int(math.Ceil(wait.Seconds()))
		httputil.WriteActionLimitBody(w, secs, map[string]any{"ok": false, "error": "rate_limited", "message": fmt.Sprintf("retry in %ds", secs)})
		return
	}
	msg, err := h.bs.PostHubNotice(ctx, project, text)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	h.hubNotices.record(key, text, now)
	slog.Info("[cloud] Coral Hub notice", "board", project, "kind", body.Kind, "role", body.Role)
	if h.notifyFn != nil {
		h.notifyFn(project)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message_id": msg.ID})
}
