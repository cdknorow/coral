package routes

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"

	"github.com/subgentic/coral/internal/cloud"
	setreg "github.com/subgentic/coral/internal/settings"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/pkg/tunnel"
)

// The caller of a comments route is derived HERE and nowhere else, and fails closed (#5176, Security's conditions): the author of a comment, the key of its rate limit and every
// ownership check come from this function and never from the request body or a header the client chose.
//
//   - A request carrying the tunnel's stamp is never the owner, whatever else it carries (the operator's own credential included), unless the table's owner check passes on one of the three
//     owner write routes (a comment or reply, a resolve, a reopen: ownerCommentWrite, #7052): with the authentic stamp it is otherwise a Coral Hub viewer, with a forged one it is refused outright.
//   - X-Coral-Hub-User is read only on an authentic stamp, and only when it is exactly hub-user-<8 hex>; unstamped, the header is ignored. (The tunnel forwards it for these routes only
//     once Coral Hub has settled a stable subject; until then a hub viewer is read-only and has no subject.)
//   - An agent is the session its verified token names.
//   - The owner is the person at this computer, as the saved messages already decide it (operatorOrKeyCaller), and is identified by ownerIdentity.

// hubIssuer is the issuer stamped on a Coral Hub viewer's identity until the hub names its origin and a stable account (#5176): the subject then is today's hub-user id.
const hubIssuer = "coral-hub"

// commentTeamIssuerPrefix starts the issuer of a comment written on a team's machine (#7061): the prefix and the organisation's id, so the same hub user id in another organisation is another author.
// The issuer is stored, compared and rate-limited as it is; what a viewer is shown is commentTeamIssuerShown, never the organisation's id.
const (
	commentTeamIssuerPrefix = "coral-hub-team:"
	commentTeamIssuerShown  = "coral-hub-team"
)

func commentTeamIssuer(org string) string { return commentTeamIssuerPrefix + org }

func isCommentTeamIssuer(issuer string) bool {
	return strings.HasPrefix(issuer, commentTeamIssuerPrefix)
}

// commentOwnerIdentity names the owner for new comments: the account this Coral is signed in to Coral Hub with, when it has one (set by the server once the roll-up is on; nil until then), or
// this computer's user. It is the one place the owner's (issuer, subject) comes from.
type commentOwnerIdentity func(r *http.Request) (issuer, subject, name string)

// ArtifactCommentsOwnerIDSetting holds the random id of this install's owner, the subject the owner's comments are stored under (Coral's own bookkeeping; no client writes it). It is never the
// operating-system user name: that name must not reach a Coral Hub viewer or an agent, and an account renamed on the computer must not lose its own edit rights.
const ArtifactCommentsOwnerIDSetting = setreg.ArtifactCommentsOwnerID

// ownerIdentity names the owner: the hook's answer when the server set one (the account this Coral is signed in to Coral Hub with, once that is turned on), else issuer "local" with this install's
// random owner id, READ ONLY: a request that only reads must not write a row, so until the id exists the subject is empty (nothing is the owner's yet, so mine is false everywhere).
// The writes that need an identity (a comment, a resolve) call keepOwnerSubject.
func (h *SessionsHandler) ownerIdentity(r *http.Request) (issuer, subject, name string) {
	if h.commentOwner != nil {
		return h.commentOwner(r)
	}
	return "local", h.commentOwnerID(r.Context(), false), ""
}

// keepOwnerSubject gives an owner caller their install id, made and kept now if need be, for a write. If it cannot be kept the subject stays empty and the write is refused as unattributable.
func (h *SessionsHandler) keepOwnerSubject(ctx context.Context, c *commentCaller) {
	if c.Kind == commentCallerOwner && c.Subject == "" && h.commentOwner == nil {
		c.Subject = h.commentOwnerID(ctx, true)
	}
}

var commentOwnerIDMu sync.Mutex

// commentOwnerID is the install's owner id; with create it is made and kept the first time it is needed. Empty means none yet (or none can be kept): an empty subject owns nothing.
func (h *SessionsHandler) commentOwnerID(ctx context.Context, create bool) string {
	commentOwnerIDMu.Lock()
	defer commentOwnerIDMu.Unlock()
	st, err := h.ss.GetSettings(ctx)
	if err != nil {
		return ""
	}
	if v := setreg.Raw(st, ArtifactCommentsOwnerIDSetting); len(v) == 32 {
		return v
	}
	if !create {
		return ""
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	id := hex.EncodeToString(b)
	if err := h.ss.SetSetting(ctx, ArtifactCommentsOwnerIDSetting, id); err != nil {
		return ""
	}
	return id
}

// artifactCommentCaller is the single derivation. A false ok means the request is refused with the given status and code.
func (h *SessionsHandler) artifactCommentCaller(r *http.Request) (c commentCaller, status int, code string) {
	if r.Header.Get(tunnel.ViaHeader) != "" {
		if !cloud.TunneledAuthentic(r) {
			return commentCaller{}, http.StatusForbidden, "forged_tunnel_stamp"
		}
		if ownerCommentRequest(r) {
			if id := hubOwnerRequest(r); id.owner && id.team {
				// A team's machine (#7061): the owner is several people, so the author is the stamped person, not the machine's one owner identity: the organisation (the record's, which the stamp matched) and
				// the person's stable id in it are the identity, and the label they are shown by is the display name. The same person in two organisations is two identities.
				return commentCaller{Kind: commentCallerOwner, Issuer: commentTeamIssuer(id.org), Subject: id.hubUserID, Name: cleanCommentName(id.pseudo), ViaHub: true}, 0, ""
			} else if id.owner {
				// The Hub's owner, by every check of hubOwnerRequest (the stamp, the owner marker, the enrolled label), on an owner write route: the owner's own identity, the server's (ownerIdentity), never the
				// stamped hub user: that is used for the check only (#7052).
				issuer, subject, name := h.ownerIdentity(r)
				return commentCaller{Kind: commentCallerOwner, Issuer: issuer, Subject: subject, Name: cleanCommentName(name), ViaHub: true}, 0, ""
			}
		}
		c = commentCaller{Kind: commentCallerHub, Issuer: hubIssuer}
		if u := r.Header.Get(tunnel.HubUserHeader); tunnel.ValidHubUser(u) {
			c.Subject = u
		}
		return c, 0, ""
	}
	if owner, ok := VerifiedSession(r.Context()); ok && owner != nil {
		return commentCaller{Kind: commentCallerAgent, Session: owner.SessionName, Issuer: "agent", Subject: owner.SessionID}, 0, ""
	}
	if r.Header.Get(store.SessionTokenHeader) != "" {
		return commentCaller{}, http.StatusForbidden, "bad_agent_token"
	}
	if operatorOrKeyCaller(r) {
		issuer, subject, name := h.ownerIdentity(r)
		return commentCaller{Kind: commentCallerOwner, Issuer: issuer, Subject: subject, Name: cleanCommentName(name)}, 0, ""
	}
	return commentCaller{}, http.StatusForbidden, "sign_in_required"
}

// cleanCommentName is a display name as text: printable, one line, at most 32 characters. It is never an identity.
func cleanCommentName(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range strings.TrimSpace(s) {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == 0x200b || r == 0x200c || r == 0x200d || r == 0x2060 || r == 0xfeff || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) {
			continue
		}
		if n++; n > 32 {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

// ownerCommentRequest is ownerCommentWrite or the owner's read of an artifact's comments (GET /api/artifacts/{hex64}/comments, the table's owner-only read): the requests on which the stamped owner is
// the owner kind, so the answer says what the owner may do (comment, reply, resolve) and which comments are theirs. It adds nothing the table does not list: the owner's edit, delete and lock stay
// the computer's (commentCaller.ViaHub keeps them off the answer).
func ownerCommentRequest(r *http.Request) bool {
	if r.Method == http.MethodGet {
		pattern, mode := tunnel.OwnerRoute(r.Method, r.URL.Path)
		return mode != tunnel.OwnerNone && mode.Refuses() && pattern == "/api/artifacts/{hex64}/comments"
	}
	return ownerCommentWrite(r)
}

// ownerCommentWrite reports whether the request is a POST to a route the tunnel table lists as an owner-only comment write (a comment or reply, a resolve, a reopen). The table decides: a route it does not list
// as owner-only, a PATCH or DELETE, the lock and the agent's reply are never one.
func ownerCommentWrite(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	pattern, mode := tunnel.OwnerRoute(r.Method, r.URL.Path)
	if mode == tunnel.OwnerNone || !mode.Refuses() {
		return false
	}
	switch pattern {
	case "/api/artifacts/{hex64}/comments", "/api/artifacts/{hex64}/comments/{id}/resolve", "/api/artifacts/{hex64}/comments/{id}/reopen":
		return true
	}
	return false
}
