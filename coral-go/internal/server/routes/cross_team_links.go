package routes

// The cross-team link table (#5572, patch 2). Coral Hub's owner links a team on one Coral to a team on another; Coral Hub keeps those links and sends this Coral the whole list as a full snapshot
// over its own authenticated tunnel: on connect, when a link is made or revoked, and every few minutes. This file is the receiving end and the table. It changes nothing about who is admitted
// yet: the door switch to links is the next patch, which reads Admits and DoorMode from here.
//
// What is trusted, and why (Security's twelve conditions):
//   - A snapshot is accepted only on a request that is TunneledAuthentic (the per-process stamp), carries X-Coral-Hub-Links: snapshot (set only by Coral Hub, forwarded only on this path), and
//     carries the connector's own session and capability headers (the hub cannot send them). Anything else is refused whole.
//   - The first snapshot of every tunnel session is accepted whatever its version (a hub restore may lower it; nobody can replay across authenticated sessions); within a session an OLDER version
//     is ignored and an equal one refreshes (the hub re-sends on a timer).
//   - It must name THIS computer's server id. Caps (bytes, links, teams, links per team) refuse the whole snapshot; the table is left as it was and ages out by the TTL.
//   - An entry names ids in the generated shape (b- and ten characters), and its target must be a board of this computer with a row in the boards table: a name that merely looks like an id is
//     no team. An entry that fails is dropped and counted, never stored.
//   - Freshness is this process's own monotonic clock at receipt (time.Since on a stored reading), and nothing is admitted until a snapshot has been applied since this process started. What is
//     persisted is for the panel only.
//   - The capability is per connection. Once the hub has advertised it, a request without it is refused, never sent back to the old path (DoorMode).

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/cloud"
	setreg "github.com/subgentic/coral/internal/settings"
	"github.com/subgentic/coral/pkg/tunnel"
)

const (
	// linkTTL is how long a snapshot is good for after it was received; the hub re-sends every 3 minutes while connected.
	linkTTL = 10 * time.Minute
	// maxSnapshotBytes bounds the body; the caps below bound what it may hold.
	maxSnapshotBytes = 1 << 20
	maxSnapshotLinks = 5000
	maxLinksPerTeam  = 200
	maxLinkTeams     = 100
)

var (
	linkSessionRe = regexp.MustCompile(`^[0-9a-f]{32}$`)
	linkTeamRe    = regexp.MustCompile(`^b-[a-z2-7]{10}$`)
)

// teamLink is one link, in memory.
type teamLink struct {
	sourceServer, sourceTeam, target string
}

// linkTable is the authoritative table: in memory only.
type linkTable struct {
	mu        sync.Mutex
	session   string
	version   int64
	applied   bool      // a snapshot has been applied since this process started
	appliedAt time.Time // carries a monotonic reading; Since() is used, never a wall time
	set       map[teamLink]bool
	// counters, for the owner's log: never text from the hub.
	rejected, dropped, ignored int
}

// apply replaces the table with links when the snapshot may be applied: the first of a session, or at least the version already applied in this session. It reports whether it did.
func (t *linkTable) apply(session string, version int64, links []teamLink, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.applied && session == t.session && version < t.version {
		t.ignored++
		return false
	}
	set := make(map[teamLink]bool, len(links))
	for _, l := range links {
		set[l] = true
	}
	t.session, t.version, t.set, t.applied, t.appliedAt = session, version, set, true, now
	return true
}

// fresh says whether the table can admit anything now: a snapshot applied in this process, within linkTTL of this process's own clock.
func (t *linkTable) fresh(now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.applied && now.Sub(t.appliedAt) < linkTTL && now.Sub(t.appliedAt) >= 0
}

// admits says whether the team (srcServer, srcTeam) is linked to target in a fresh table.
func (t *linkTable) admits(srcServer, srcTeam, target string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.applied || now.Sub(t.appliedAt) >= linkTTL || now.Sub(t.appliedAt) < 0 {
		return false
	}
	return t.set[teamLink{srcServer, srcTeam, target}]
}

// linkedTo lists the live links to target, sorted.
func (t *linkTable) linkedTo(target string, now time.Time) []teamLink {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.applied || now.Sub(t.appliedAt) >= linkTTL || now.Sub(t.appliedAt) < 0 {
		return nil
	}
	var out []teamLink
	for l := range t.set {
		if l.target == target {
			out = append(out, l)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].sourceServer != out[j].sourceServer {
			return out[i].sourceServer < out[j].sourceServer
		}
		return out[i].sourceTeam < out[j].sourceTeam
	})
	return out
}

func (t *linkTable) count(field *int) { t.mu.Lock(); *field++; t.mu.Unlock() }

// doorMode is what the receiving door does with a relayed request, from the capability of the connection it arrived on and what this computer has seen before.
type doorMode int

const (
	// doorOld: the hub has never advertised cross_team_links/1 to this computer: today's rule (the board's list of servers).
	doorOld doorMode = iota
	// doorLinks: this connection's hub advertised it: only a fresh link admits.
	doorLinks
	// doorRefuse: the hub advertised it before and this connection's does not (a rollback, a bug, another peer): refuse, never the old path (Security's condition 2).
	doorRefuse
)

// LinksSeenSetting records, once, that a hub advertised cross_team_links/1 to this computer. Written by code only, never through the tunnel, and read to refuse a downgrade after a restart.
const LinksSeenSetting = "cross_team_links_seen"

// SetLinksSeen tells the handler how to read and write the one-way "a hub has advertised links" flag. Unreadable reads as seen (the strict side).
func (h *BoardHandler) SetLinksSeen(read func(ctx context.Context) (bool, error), write func(ctx context.Context) error) {
	h.linksSeenRead, h.linksSeenWrite = read, write
}

// SetOwnServerID tells the handler where this computer's Coral Hub server id is read from.
func (h *BoardHandler) SetOwnServerID(fn func(ctx context.Context) (string, error)) {
	h.ownServerID = fn
}

func (h *BoardHandler) linksSeen(ctx context.Context) bool {
	if h.linksSeenRead == nil {
		return false // not wired (a test handler): nothing says a hub ever advertised links. The server always wires it.
	}
	seen, err := h.linksSeenRead(ctx)
	return err != nil || seen
}

// linksCapOn is whether the request arrived on a connection whose hub advertised the capability: the connector's header, trusted only on a request carrying the per-process stamp.
func linksCapOn(r *http.Request) bool {
	return cloud.TunneledAuthentic(r) && len(r.Header.Values(tunnel.HubLinksCapHeader)) == 1 && r.Header.Get(tunnel.HubLinksCapHeader) == "1"
}

// DoorMode decides how the door treats a relayed request r: see doorMode. It also remembers, once, that a hub has advertised links.
func (h *BoardHandler) DoorMode(ctx context.Context, r *http.Request) doorMode {
	if linksCapOn(r) {
		if h.linksSeenWrite != nil && !h.linksSeen(ctx) {
			_ = h.linksSeenWrite(ctx)
		}
		return doorLinks
	}
	if h.links.everApplied() || h.linksSeen(ctx) {
		return doorRefuse
	}
	return doorOld
}

func (t *linkTable) everApplied() bool { t.mu.Lock(); defer t.mu.Unlock(); return t.applied }

// appliedEmpty says the last snapshot applied in this process had no link in it: with it nothing is admitted whether it is fresh or not, so an old one has nothing to say is paused (#6377).
func (t *linkTable) appliedEmpty() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.applied && len(t.set) == 0
}

func (h *BoardHandler) linkNow() time.Time {
	if h.linkClock != nil {
		return h.linkClock()
	}
	return time.Now()
}

type snapshotLink struct {
	SourceServer      string `json:"source_server"`
	SourceTeam        string `json:"source_team"`
	TargetTeam        string `json:"target_team"`
	TargetServer      string `json:"target_server"`
	SourceLabel       string `json:"source_label"`
	TargetLabel       string `json:"target_label"`
	SourceServerLabel string `json:"source_server_label"`
}

type snapshotBody struct {
	Version    int64          `json:"version"`
	ThisServer string         `json:"this_server"`
	Links      []snapshotLink `json:"links"`
}

func writeLinksRefusal(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"ok": false, "error": code, "message": msg})
}

// PostCrossTeamLinks receives Coral Hub's full link snapshot. POST /api/cloud/cross-team-links (only through this computer's own tunnel connector, only from Coral Hub).
func (h *BoardHandler) PostCrossTeamLinks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !cloud.TunneledAuthentic(r) || len(r.Header.Values(tunnel.HubLinksHeader)) != 1 || r.Header.Get(tunnel.HubLinksHeader) != tunnel.HubLinksSnapshot {
		writeLinksRefusal(w, http.StatusForbidden, "hub_only", "only Coral Hub sends the link list")
		return
	}
	if r.Header.Get(tunnel.HubUserHeader) != "" {
		writeLinksRefusal(w, http.StatusBadRequest, "ambiguous_origin", "the link list comes from Coral Hub, never a Coral Hub user")
		return
	}
	session := r.Header.Get(tunnel.HubLinksSessionHeader)
	if len(r.Header.Values(tunnel.HubLinksSessionHeader)) != 1 || !linkSessionRe.MatchString(session) || !linksCapOn(r) {
		writeLinksRefusal(w, http.StatusForbidden, "links_not_advertised", "this connection's hub did not advertise cross_team_links/1")
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxSnapshotBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			h.links.count(&h.links.rejected)
			writeLinksRefusal(w, http.StatusRequestEntityTooLarge, "too_large", "the link list is over the size limit")
			return
		}
		writeLinksRefusal(w, http.StatusBadRequest, "bad_request", "the body is unreadable")
		return
	}
	var body snapshotBody
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.Version < 0 {
		writeLinksRefusal(w, http.StatusBadRequest, "bad_request", "invalid JSON")
		return
	}
	if _, err := dec.Token(); err != io.EOF {
		writeLinksRefusal(w, http.StatusBadRequest, "bad_request", "one object only")
		return
	}
	ctx := r.Context()
	if h.ownServerID == nil {
		writeLinksRefusal(w, http.StatusServiceUnavailable, "unavailable", "this computer's server id is unknown")
		return
	}
	own, err := h.ownServerID(ctx)
	own = strings.ToLower(own)
	if err != nil || own == "" || !strings.EqualFold(own, body.ThisServer) {
		writeLinksRefusal(w, http.StatusForbidden, "wrong_server", "this link list is for another computer")
		return
	}
	if len(body.Links) > maxSnapshotLinks {
		h.links.count(&h.links.rejected)
		slog.Warn("[cloud] a link snapshot was refused whole: too many links", "links", len(body.Links))
		writeLinksRefusal(w, http.StatusRequestEntityTooLarge, "too_large", "too many links")
		return
	}
	perTeam := map[string]int{}
	for _, l := range body.Links {
		perTeam[l.TargetTeam]++
	}
	if len(perTeam) > maxLinkTeams {
		h.links.count(&h.links.rejected)
		slog.Warn("[cloud] a link snapshot was refused whole: too many teams", "teams", len(perTeam))
		writeLinksRefusal(w, http.StatusRequestEntityTooLarge, "too_large", "too many teams")
		return
	}
	for _, n := range perTeam {
		if n > maxLinksPerTeam {
			h.links.count(&h.links.rejected)
			slog.Warn("[cloud] a link snapshot was refused whole: too many links to one team", "links", n)
			writeLinksRefusal(w, http.StatusRequestEntityTooLarge, "too_large", "too many links to one team")
			return
		}
	}
	// Each entry: the shapes, then a target that is a real, generated board of this computer. One that fails is dropped and counted, never stored.
	kept := make([]teamLink, 0, len(body.Links))
	stored := make([]board.TeamLink, 0, len(body.Links))
	targetOK := map[string]bool{}
	for _, l := range body.Links {
		src := strings.ToLower(strings.TrimSpace(l.SourceServer))
		tgtServer := strings.ToLower(strings.TrimSpace(l.TargetServer))
		if tgtServer == own {
			tgtServer = "" // a target on this computer, said out loud
		}
		if !relayIDRe.MatchString(src) || !linkTeamRe.MatchString(l.SourceTeam) || !linkTeamRe.MatchString(l.TargetTeam) || (tgtServer != "" && !relayIDRe.MatchString(tgtServer)) {
			h.links.count(&h.links.dropped)
			continue
		}
		// The team here is the SOURCE when the link names another computer as its target: kept to show "this team can message X", never read by the door.
		local := l.TargetTeam
		if tgtServer != "" {
			local = l.SourceTeam
			if src != own {
				h.links.count(&h.links.dropped) // neither end is on this computer
				continue
			}
		}
		ok, seen := targetOK[local]
		if !seen {
			b, err := h.bs.GetBoard(ctx, local)
			ok = err == nil && b != nil && b.ID == local && board.IsGeneratedBoardID(b.ID)
			targetOK[local] = ok
		}
		if !ok {
			h.links.count(&h.links.dropped)
			continue
		}
		if tgtServer == "" {
			kept = append(kept, teamLink{src, l.SourceTeam, l.TargetTeam})
		}
		stored = append(stored, board.TeamLink{SourceServer: src, SourceTeam: l.SourceTeam, TargetTeam: l.TargetTeam, TargetServer: tgtServer,
			SourceLabel: board.CleanClaimedLabel(l.SourceLabel), TargetLabel: board.CleanClaimedLabel(l.TargetLabel), SourceServerLabel: board.CleanClaimedLabel(l.SourceServerLabel)})
	}
	now := h.linkNow()
	applied := h.links.apply(session, body.Version, kept, now)
	if applied {
		// For the panel only; the door never reads it. A failure to keep it changes nothing about who is admitted.
		if err := h.bs.ReplaceTeamLinks(context.WithoutCancel(ctx), stored, time.Now()); err != nil {
			slog.Warn("[cloud] could not keep the link list for the panel", "error", err)
		}
		if h.linksSeenWrite != nil && !h.linksSeen(ctx) {
			_ = h.linksSeenWrite(context.WithoutCancel(ctx))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "applied": applied, "links": len(kept)})
}

// LinksSeenFromSettings reads the one-way flag from the settings store.
func LinksSeenFromSettings(read func(ctx context.Context) (map[string]string, error)) func(context.Context) (bool, error) {
	return func(ctx context.Context) (bool, error) {
		s, err := read(ctx)
		if err != nil {
			return true, err
		}
		return strings.EqualFold(strings.TrimSpace(setreg.Raw(s, LinksSeenSetting)), "true"), nil
	}
}

// MarkLinksSeen writes the flag.
func MarkLinksSeen(set func(ctx context.Context, key, value string) error) func(context.Context) error {
	return func(ctx context.Context) error { return set(ctx, LinksSeenSetting, "true") }
}
