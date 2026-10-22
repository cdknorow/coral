package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/cliauth"
	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/httputil"
	setreg "github.com/subgentic/coral/internal/settings"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/pkg/tunnel"
)

// Who may message a board (#5508, coral-hub's spec): with the allowlist in force, a board takes a cross-team message, and the files that come with it, only from a sender on its list of
// {server, cross_team_id}; an empty list admits nobody. Every door (a message between two boards on this Coral, a message relayed from another Coral, a file pushed from another Coral)
// asks the same function, in the same order, and answers a refusal with the same bytes whatever the reason.

const (
	// crossTeamRefusedCode and crossTeamRefusedText are the ONE refusal of the local doors: the sender is not on the list, the board does not exist, is off, was deleted, or has nobody to
	// deliver to. Nothing in it depends on which, and it never echoes a board name or an id.
	crossTeamRefusedCode = "cross_team_refused"
	crossTeamRefusedText = "this board does not take messages from that sender"
)

// SetAllowlistFlag tells the handler how to read the global switch (cross_team_allowlist) that puts the allowlist in force. Off (the default of the release that adds it) leaves today's rules:
// the list is stored and editable, and what it would have refused is counted as "would refuse" (shadow rows).
func (h *BoardHandler) SetAllowlistFlag(fn func(ctx context.Context) bool) { h.allowlistFlag = fn }

func (h *BoardHandler) allowlistOn(ctx context.Context) bool {
	return h.allowlistFlag != nil && h.allowlistFlag(ctx)
}

// writeCrossTeamRefused is the local doors' refusal: the same status, code, text and size for every cause.
func writeCrossTeamRefused(w http.ResponseWriter) {
	writeError(w, http.StatusForbidden, crossTeamRefusedCode, crossTeamRefusedText)
}

// doorTrace, when set (tests), is told each step of crossTeamDoor as it runs, to prove every cause takes the same steps in the same order.
var doorTrace func(step string)

// doorSender is who is at the door: the Coral it is on (board.LocalServer, or the hub-verified server id), its board's cross-team id (read from this Coral's own row for a local sender, the
// sending Coral's claim for a relayed one), and whether it is the verified local Operator, who is a person and not a team.
type doorSender struct {
	server, crossTeamID string
	operator            bool
	relayed             bool // from another Coral: its server id is never "local", the word for a board on this Coral (#5511 review)
}

func (s doorSender) listed(settings board.BoardSettings) bool {
	if s.relayed {
		return settings.AllowsRelayedSender(s.server, s.crossTeamID)
	}
	return s.operator || settings.AllowsSender(s.server, s.crossTeamID)
}

// crossTeamDoor is the one decision for every door. It runs the same four reads in the same order whatever the answer (the board row, its settings, the list, the recipients), and
// admits only when the board exists, the sender is on its list (or is the local Operator), and an Orchestrator can be told (any failure to find one fails closed). A failure to read the board row or its settings is an error (a 500), never a refusal.
func (h *BoardHandler) crossTeamDoor(ctx context.Context, target string, s doorSender, to string) (recipients []string, settings board.BoardSettings, admitted bool, err error) {
	step := func(name string) {
		if doorTrace != nil {
			doorTrace(name)
		}
	}
	step("exists")
	exists, err := h.bs.BoardExists(ctx, target)
	if err != nil {
		return nil, settings, false, err
	}
	step("settings")
	settings, err = h.bs.GetBoardSettings(ctx, target)
	if err != nil {
		return nil, settings, false, err
	}
	step("list")
	listed := s.listed(settings)
	step("recipients")
	recipients, rerr := h.bs.TargetOrchestrators(ctx, target, to) // nobody to tell, or a name that is not an Orchestrator: the same refusal; failing closed on any error
	return recipients, settings, exists && listed && rerr == nil, nil
}

// relayedDoor is the door for a message relayed from another Coral while the allowlist is off: the board exists, its cross-team switch is on, the source server is one it takes, and an
// Orchestrator can be told. It runs the same four reads in the same order whatever the answer, so no cause is told from another by what the request did (#5572, Security's condition 9).
func (h *BoardHandler) relayedDoor(ctx context.Context, target, src, to string) (recipients []string, settings board.BoardSettings, admitted bool, err error) {
	step := func(name string) {
		if doorTrace != nil {
			doorTrace(name)
		}
	}
	step("exists")
	exists, err := h.bs.BoardExists(ctx, target)
	if err != nil {
		return nil, settings, false, err
	}
	step("settings")
	settings, err = h.bs.GetBoardSettings(ctx, target)
	if err != nil {
		return nil, settings, false, err
	}
	step("list")
	linked := settings.ExplicitMessagingOn() && settings.AllowsServer(src) // the old door needs the owner's own on: the default does not widen it (#5916)
	step("recipients")
	recipients, rerr := h.bs.TargetOrchestrators(ctx, target, to)
	return recipients, settings, exists && linked && rerr == nil, nil
}

// linksDoor is the door for a message relayed through a hub that advertised cross_team_links/1 (#5862): the board exists, its cross-team switch is on, the sending team is linked to it
// in a fresh table of the hub's snapshots (linkTable.admits(the source server and the source team, both set by Coral Hub, this board), never the board's own server list, which the Messaging tab no longer
// edits), and an Orchestrator can be told. refuse is doorRefuse: a connection whose hub no longer advertises links, for a computer that has seen one that did, is never sent back to the
// old door; it runs the same four reads in the same order and admits nothing. Like relayedDoor, every cause takes the same steps in the same order (#5572, Security's condition 9).
func (h *BoardHandler) linksDoor(ctx context.Context, target string, srcServer, srcTeam, to string, refuse bool) (recipients []string, settings board.BoardSettings, admitted bool, err error) {
	step := func(name string) {
		if doorTrace != nil {
			doorTrace(name)
		}
	}
	step("exists")
	exists, err := h.bs.BoardExists(ctx, target)
	if err != nil {
		return nil, settings, false, err
	}
	step("settings")
	settings, err = h.bs.GetBoardSettings(ctx, target)
	if err != nil {
		return nil, settings, false, err
	}
	step("list")
	linked := h.links.admits(srcServer, srcTeam, target, h.linkNow()) // read whatever the other answers are: a stale or missing table, or no source team, admits nothing
	linked = linked && settings.CrossTeamMessaging && !refuse
	step("recipients")
	recipients, rerr := h.bs.TargetOrchestrators(ctx, target, to)
	return recipients, settings, exists && linked && rerr == nil, nil
}

// hubSourceTeam is the sending team of a relayed request as Coral Hub names it (X-Coral-Hub-Source-Team, the generated board id of the team in the link pair; the hub overwrites any value a
// sending Coral supplied, and the connector forwards it only where X-Coral-Hub-Server is): exactly one value in the generated shape, else "" (no team, which no link admits). Read only on a
// request carrying the per-process stamp, as the server header is; the body's own claim of its cross-team id is not this and is never used for a link.
func hubSourceTeam(r *http.Request) string {
	if !cloud.TunneledAuthentic(r) {
		return ""
	}
	v := r.Header.Values(tunnel.HubSourceTeamHeader)
	if len(v) != 1 || !linkTeamRe.MatchString(v[0]) {
		return ""
	}
	return v[0]
}

// writeRelayedRefused is the one refusal toward a sender on another Coral.
func writeRelayedRefused(w http.ResponseWriter) {
	relayRefuse(w, relayedRefusedStatus, relayedRefusedCode, relayedRefusedText)
}

// senderIDOf is the cross-team id of a board on this Coral, read from its own row (never from a request); ok is false when it has none.
func (h *BoardHandler) senderIDOf(ctx context.Context, key string) (string, bool) {
	id, ok, err := h.bs.CrossTeamID(ctx, key)
	if err != nil {
		return "", false
	}
	return id, ok
}

// noteDoor records a send the list refused (kind board.LogRefused, with the flag on) or would have refused (board.LogWouldRefuse, with it off): one row an hour per board, a count and ids only.
func (h *BoardHandler) noteDoor(ctx context.Context, target, kind string, s doorSender) {
	h.noteHourly(target, kind, board.AllowedSender{Server: s.server, CrossTeamID: validIDOrEmpty(s.crossTeamID)})
}

// noteHourly counts one event for a board's log, after the answer and off the request: the request does the same thing for every cause (it only starts this), and no row is written for a board
// that does not exist: the board name is the sender's, so rows for invented names would grow the table without bound (#5511 review).
func (h *BoardHandler) noteHourly(project, kind string, sender board.AllowedSender) {
	h.goBG(func() {
		ctx := context.Background()
		if ok, err := h.bs.BoardExists(ctx, project); err != nil || !ok {
			return
		}
		if err := h.bs.NoteHourly(ctx, project, kind, sender, time.Now()); err != nil {
			slog.Warn("cross-team: could not count a refused send or edit", "kind", kind, "error", err)
		}
	})
}

// validIDOrEmpty is id when it is a well-formed cross-team id, else "": a claimed id that is anything else is never stored, shown or echoed.
func validIDOrEmpty(id string) string {
	if board.ValidCrossTeamID(id) {
		return id
	}
	return ""
}

// crossTeamAdmit is the same-Coral door (a message from one board of this Coral to another's Orchestrators). Flag off: today's rules unchanged, with a shadow check of the list ("would refuse"
// rows). Flag on: the sender's own switch (a message about its OWN settings, local senders only), the sender's rate token (taken whether the send is refused or not, SPEC-6), then the one
// door; any refusal is the same bytes.
func (h *BoardHandler) crossTeamAdmit(ctx context.Context, m *externalMessage, role, target string) (recipients []string, rateTaken bool, refused answer) {
	sender := doorSender{server: board.LocalServer, operator: role == crossTeamOperatorID}
	if !h.allowlistOn(ctx) {
		recipients, refused = h.crossTeamRecipients(ctx, m.FromBoard, target, m.To)
		if refused == nil {
			sender.crossTeamID, _ = h.senderIDOf(ctx, m.FromBoard)
			if settings, err := h.bs.GetBoardSettings(ctx, target); err == nil && !sender.listed(settings) {
				h.noteDoor(ctx, target, board.LogWouldRefuse, sender)
			}
		}
		return recipients, false, refused
	}
	if own, err := h.bs.GetBoardSettings(ctx, m.FromBoard); err != nil {
		return nil, false, func(w http.ResponseWriter) { errInternalServer(w, err.Error()) }
	} else if !own.ExplicitMessagingOn() {
		return nil, false, func(w http.ResponseWriter) {
			errForbidden(w, textCrossTeamOff([]string{strconv.Quote(m.FromBoard)}))
		}
	}
	if secs, limited := h.crossTeamWait(m.FromBoard + "\x00" + m.SubscriberID + "\x00" + target); limited {
		return nil, true, func(w http.ResponseWriter) {
			httputil.WriteActionLimit(w, secs, "rate_limited", fmt.Sprintf("too many messages to %q; retry in %ds", target, secs))
		}
	}
	sender.crossTeamID, _ = h.senderIDOf(ctx, m.FromBoard)
	recipients, _, admitted, err := h.crossTeamDoor(ctx, target, sender, m.To)
	if err != nil {
		return nil, true, func(w http.ResponseWriter) { errInternalServer(w, err.Error()) }
	}
	if !admitted {
		h.noteDoor(ctx, target, board.LogRefused, sender)
		return nil, true, writeCrossTeamRefused
	}
	return recipients, true, nil
}

// noteEditRefused counts a refused edit of the list: one audit row an hour per board (#5508).
func (h *BoardHandler) noteEditRefused(_ context.Context, project string) {
	h.noteHourly(project, board.LogEditRefused, board.AllowedSender{})
}

// allowedForLog is a list as compact JSON for the log: the server and id of each entry, and the label the owner saw (a cleaned, bounded claim, so that a removed board is still
// recognisable in the history).
func allowedForLog(list []board.AllowedSender) string {
	if len(list) == 0 {
		return "[]"
	}
	b, err := json.Marshal(list)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// maxListEditsPerHour is how many edits of the list one owner session may make in an hour (coral-hub's SPEC-2).
const maxListEditsPerHour = 30

// editLimiter counts list edits per owner session per clock hour; its size is bounded.
type editLimiter struct {
	mu     sync.Mutex
	window time.Time
	counts map[string]int
}

// allowEditSlot takes one of the owner session's maxListEditsPerHour edits for this hour; false when they are used up.
func (h *BoardHandler) allowEditSlot(a Actor) bool {
	key := a.Name + "\x00" + a.Session
	l := &h.listEdits
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now().UTC().Truncate(time.Hour)
	if !l.window.Equal(now) || l.counts == nil || len(l.counts) > 1000 {
		l.window, l.counts = now, map[string]int{}
	}
	if l.counts[key] >= maxListEditsPerHour {
		return false
	}
	l.counts[key]++
	return true
}

// requireListOwner lets only the verified Operator on this computer through, for the routes that read or edit who may message a board (#5508): never an agent token (an Orchestrator's
// included), never through Coral Hub.
func (h *BoardHandler) requireListOwner(w http.ResponseWriter, r *http.Request) bool {
	if cloud.Tunneled(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"code": "local_only", "error": "who may message a board is read and changed on this computer only, not through Coral Hub"})
		return false
	}
	allowed := !agentCaller(r) && !(isAgentTool(r) && !cliauth.Verified(r))
	if identityActorEnforced() {
		allowed = callerActor(r).Kind == ActorOperator
	}
	if !allowed {
		errForbidden(w, "only the Operator can read or change who may message a board")
		return false
	}
	return true
}

// seenWindow is how far back the owner's suggestions look.
const seenWindow = 30 * 24 * time.Hour

type crossTeamStateSender struct {
	Server      string `json:"server"`
	CrossTeamID string `json:"cross_team_id"`
	Label       string `json:"label,omitempty"` // the sender's own claim, or this Coral's label for a board here (cleaned); never an id
	Known       bool   `json:"known_here"`      // the id is a board of this Coral
}

// GetCrossTeamState is what Team settings shows for who may message a board: whether the allowlist is in force, the list (with labels beside the ids), this board's own id, whether the board
// was open before (its old switch is on), and suggestions: the senders its stored messages show from the last 30 days, never applied by this. A label is a claim by whoever sent it.
// GET /api/board/{project}/cross-team-state (the Operator, this computer only)
func (h *BoardHandler) GetCrossTeamState(w http.ResponseWriter, r *http.Request) {
	if !h.requireListOwner(w, r) {
		return
	}
	project := urlParam(r, "project")
	ctx := r.Context()
	settings, err := h.bs.GetBoardSettings(ctx, project)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	self, _ := h.senderIDOf(ctx, project)
	listed := map[string]bool{}
	entries := make([]crossTeamStateSender, 0, len(settings.CrossTeamAllowed))
	for _, e := range settings.CrossTeamAllowed {
		listed[e.Server+"\x00"+e.CrossTeamID] = true
		item := crossTeamStateSender{Server: e.Server, CrossTeamID: e.CrossTeamID, Label: e.Label} // the label the owner saw when listing it, until the board's current one is known
		if e.Server == board.LocalServer {
			if _, label, ok, err := h.bs.BoardByCrossTeamID(ctx, e.CrossTeamID); err == nil && ok {
				item.Label, item.Known = board.CleanClaimedLabel(label), true
			}
		}
		entries = append(entries, item)
	}
	seen, err := h.bs.RecentSenders(ctx, project, time.Now().Add(-seenWindow))
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	suggestions := make([]map[string]any, 0, len(seen))
	for _, sn := range seen {
		if listed[sn.Server+"\x00"+sn.CrossTeamID] {
			continue
		}
		suggestions = append(suggestions, map[string]any{"server": sn.Server, "cross_team_id": sn.CrossTeamID, "label": sn.Label, "label_is_a_claim": sn.Server != board.LocalServer,
			"last_seen": sn.LastSeen, "count": sn.Count, "listable": sn.CrossTeamID != ""})
	}
	// What the owner reads at the top of the Messaging tab: the state and its words (one table, cross_team_text.go), who is linked (the servers the board takes today; the hub's link list
	// replaces this when it lands, #5572), and whether the team can be linked at all.
	linkable := board.IsGeneratedBoardID(project)
	links := make([]map[string]any, 0, len(settings.CrossTeamServers))
	stale, heard := false, ""
	if h.links.everApplied() || h.linksSeen(ctx) {
		// A hub that keeps the link table (#5572): who is linked comes from its last snapshot. A snapshot older than the TTL, or none yet since this process started, is stale: the rows shown are
		// the last known ones, and nothing is admitted (the door says the same).
		now := h.linkNow()
		stale = !h.links.fresh(now)
		live := map[teamLink]bool{}
		for _, l := range h.links.linkedTo(project, now) {
			live[l] = true
		}
		var latest time.Time
		if stored, err := h.bs.TeamLinksTo(ctx, project); err == nil {
			for _, l := range stored {
				if !stale && !live[teamLink{l.SourceServer, l.SourceTeam, l.TargetTeam}] {
					continue
				}
				links = append(links, map[string]any{"server_id": l.SourceServer, "server_label": l.SourceServerLabel, "team_id": l.SourceTeam, "team_label": l.SourceLabel, "refreshed": l.AppliedAt})
				if t, err := time.Parse(time.RFC3339, l.AppliedAt); err == nil && t.After(latest) {
					latest = t
				}
			}
		}
		heard = heardWords(latest, now)
	} else {
		for _, srv := range settings.CrossTeamServers {
			links = append(links, map[string]any{"server_id": srv, "server_label": "", "team_label": "", "refreshed": ""})
		}
	}
	// What this team may message elsewhere (links where it is the source): display only, never read by the door.
	linksOut := []map[string]any{}
	if stored, err := h.bs.TeamLinksFrom(ctx, project); err == nil {
		for _, l := range stored {
			linksOut = append(linksOut, map[string]any{"server_id": l.TargetServer, "team_id": l.TargetTeam, "team_label": l.TargetLabel, "refreshed": l.AppliedAt})
		}
	}
	// Whether Coral Hub's owner can change the two switches of this team (#5572): this request (the owner on this computer) always can; owner_path_live says this build has the Coral Hub owner's routes;
	// hub_may_change is the local lock "Let Coral Hub change cross-team settings" (a lock that cannot be read reads as off); last_changed is who changed each switch last, by hub user or on this computer.
	lastChanged := h.lastChangedOf(ctx, project)
	writeJSON(w, http.StatusOK, map[string]any{
		"can_change": true, "owner_path_live": true, "hub_may_change": h.hubMayChange(ctx), "last_changed": lastChanged,
		"enforced": h.allowlistOn(ctx), "allowed": entries, "max_entries": board.MaxAllowedSenders, "self_cross_team_id": self,
		"was_open": settings.CrossTeamMessaging && len(settings.CrossTeamAllowed) == 0, "suggestions": suggestions,
		"status": statusFor(linkable, settings.CrossTeamMessaging, len(links), stale, heard),
		// The two reaches of the messaging switch, shown apart (#5916): teams on other computers (on unless the owner turned it off; they also need a Coral Hub link) and teams on this
		// computer (an explicit on only).
		"messaging_elsewhere": settings.CrossTeamMessaging, "messaging_here": settings.ExplicitMessagingOn(),
		"links":     links,
		"links_out": linksOut,
		"linkable":  linkable,
	})
}

// GetCrossTeamLog is the board's settings log: the owner's list edits (who, when, before and after, ids only), and the hourly counts of sends the list refused (or, with the allowlist off,
// would have refused) and of refused edits. Never message content.
// GET /api/board/{project}/settings/log (the Operator, this computer only)
func (h *BoardHandler) GetCrossTeamLog(w http.ResponseWriter, r *http.Request) {
	if !h.requireListOwner(w, r) {
		return
	}
	log, err := h.bs.ListLog(r.Context(), urlParam(r, "project"))
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": log})
}

// GetCrossTeamCandidates lists the boards of this Coral the owner may put on this board's list: id, label and creation time, never subscribers or content. Labels are cleaned.
// GET /api/board/{project}/cross-team-candidates (the Operator, this computer only)
func (h *BoardHandler) GetCrossTeamCandidates(w http.ResponseWriter, r *http.Request) {
	if !h.requireListOwner(w, r) {
		return
	}
	out, err := h.bs.CrossTeamCandidates(r.Context(), urlParam(r, "project"))
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"candidates": out})
}

// AllowlistEnabled reads the global switch (setting cross_team_allowlist): off when it is not set, and when the settings cannot be read (the release that adds it leaves today's rules).
func AllowlistEnabled(ss *store.SessionStore) func(context.Context) bool {
	return func(ctx context.Context) bool {
		s, err := ss.GetSettings(ctx)
		if err != nil {
			return false
		}
		return setreg.Bool(s, setreg.CrossTeamAllowlist)
	}
}
