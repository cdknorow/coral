package routes

// Per-user artifact ownership (#5576 note v2, #5577): who owns an artifact, and the ONE read check every route an agent can reach goes through.
//
// An owner is a principal: the machine owner ("operator": the local human, the signed command line, an API key, the hub owner) or a hub user (the stamped, validated id). It is recorded on
// each upload RECORD (an object is content-addressed, so two uploads of the same bytes are two records), from authentication and never from a name in a request. An agent's upload is owned by
// the owner of the agent's TEAM; a dashboard paste by the operator; a file received from another Coral by the receiving team's owner. A record with no owner (everything made before this
// release) is the machine owner's.
//
// An agent may read an artifact when some record of it is owned by the owner of the agent's team (the team the launch path put it on, live_sessions.board_name: an agent's own
// "coral-board subscribe" never changes it), or when its team is linked to the record's board (crossTeamReadOpen, patch C of #5577, waits for #5572's link state and reads as "no" until then).
// The machine owner, the dashboard and the signed command line read everything. Through Coral Hub the outer rule stays owner only (refuseNonOwner).
//
// Everything else answers the same 404 as an artifact that does not exist, before any other check (an existence probe gets nothing), and any error on the way is a refusal.
//
// Honest limit: every agent on this computer runs as the same OS user and can read Coral's data folder with its own shell. This is an API-level control, not an OS boundary.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/store"
)

// Principal kinds.
const (
	principalOperator = "operator"
	principalHubUser  = "hub_user"
)

// artifactPrincipal is who owns something: the machine owner, or one hub user.
type artifactPrincipal struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

var operatorPrincipal = artifactPrincipal{Kind: principalOperator, ID: "Operator"}

// principalOfCreator is the owner a board's created_by names: a validated hub-user id is that user; anything else (the Operator, "", an older board's creator) is the machine owner.
func principalOfCreator(createdBy string) artifactPrincipal {
	if hubUserRe.MatchString(createdBy) {
		return artifactPrincipal{Kind: principalHubUser, ID: createdBy}
	}
	return operatorPrincipal
}

// creator is what a board made for this owner stores in created_by.
func (p artifactPrincipal) creator() string {
	if p.Kind == principalHubUser && hubUserRe.MatchString(p.ID) {
		return p.ID
	}
	return "Operator"
}

// recordPrincipal is the owner an upload record names; absent or malformed is the machine owner's.
func recordPrincipal(rec agentUploadRecord) artifactPrincipal {
	if rec.OwnerKind == principalHubUser && hubUserRe.MatchString(rec.OwnerID) {
		return artifactPrincipal{Kind: principalHubUser, ID: rec.OwnerID}
	}
	return operatorPrincipal
}

// ArtifactCircle is what the read check needs to know about agents and teams. The server sets it once at startup (SetArtifactCircle). Unset, a request from an agent is refused:
// the check fails closed.
type ArtifactCircle struct {
	// SessionBoard is the board an agent session was launched onto ("" for none). It is the launch path's field: live_sessions.board_name.
	SessionBoard func(ctx context.Context, sessionID string) (string, error)
	// BoardOwner is who owns a board.
	BoardOwner func(ctx context.Context, boardKey string) (artifactPrincipal, error)
}

var artifactCircle ArtifactCircle

// SetArtifactCircle gives the read check its lookups.
func SetArtifactCircle(c ArtifactCircle) { artifactCircle = c }

var errNoSuchSession = errors.New("no such live session")

// NewArtifactCircle is the server's lookup over the session store and the board store.
func NewArtifactCircle(ss *store.SessionStore, bs *board.Store) ArtifactCircle {
	return ArtifactCircle{
		SessionBoard: func(ctx context.Context, sessionID string) (string, error) {
			if ss == nil {
				return "", errNoSuchSession
			}
			ls, err := ss.GetLiveSession(ctx, sessionID)
			if err != nil {
				return "", err
			}
			if ls == nil {
				return "", errNoSuchSession
			}
			if ls.BoardName == nil {
				return "", nil
			}
			return *ls.BoardName, nil
		},
		BoardOwner: func(ctx context.Context, boardKey string) (artifactPrincipal, error) {
			if bs == nil {
				return artifactPrincipal{}, errors.New("no board store")
			}
			b, err := bs.GetBoard(ctx, boardKey)
			if errors.Is(err, board.ErrBoardNotFound) {
				return operatorPrincipal, nil // a board made before ids is keyed by its name and has no row: the machine owner's
			}
			if err != nil {
				return artifactPrincipal{}, err
			}
			return principalOfCreator(b.CreatedBy), nil
		},
	}
}

// circleOf is the owner and team of an agent session: its team's owner, or the machine owner for a session on no team.
func circleOf(ctx context.Context, sessionID string) (artifactPrincipal, string, error) {
	if artifactCircle.SessionBoard == nil || artifactCircle.BoardOwner == nil {
		return artifactPrincipal{}, "", errors.New("the artifact circle is not set")
	}
	boardKey, err := artifactCircle.SessionBoard(ctx, sessionID)
	if err != nil {
		return artifactPrincipal{}, "", err
	}
	if boardKey == "" {
		return operatorPrincipal, "", nil
	}
	owner, err := artifactCircle.BoardOwner(ctx, boardKey)
	if err != nil {
		return artifactPrincipal{}, "", err
	}
	return owner, boardKey, nil
}

// crossTeamReadOpen says whether the agents of team from may read the artifacts of team to through a link: one-way (the sender of the link reads the receiver), messaging on, live and failing
// closed (#5576 note v2, section 2). It waits for #5572's link state: until then no cross-team read exists, which is the strict side.
func crossTeamReadOpen(ctx context.Context, from, to string) bool { return false }

// artifactRecord is one record of an artifact: who owns it and the board it is on ("" when unknown).
type artifactRecord struct {
	owner artifactPrincipal
	board string
}

// artifactRecordsOf lists the records of artifact id (wantID != "") or of every artifact (wantID == "") by digest: the sessions' upload records, and the boards' received rows, whose owner is the
// receiving team's. An error means the answer is unknown.
func artifactRecordsOf(ctx context.Context, coralDir, wantID string) (map[string][]artifactRecord, error) {
	out := map[string][]artifactRecord{}
	sessions, err := os.ReadDir(filepath.Join(coralDir, "artifacts", "uploads"))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, s := range sessions {
		if !s.IsDir() || uploadsDir(coralDir, s.Name()) == "" {
			continue
		}
		if wantID != "" {
			if rec, ok := readUploadRecord(coralDir, s.Name(), wantID); ok {
				out[wantID] = append(out[wantID], artifactRecord{owner: recordPrincipal(rec), board: rec.Board})
			}
			continue
		}
		files, err := os.ReadDir(uploadsDir(coralDir, s.Name()))
		if err != nil {
			continue
		}
		for _, f := range files {
			name := f.Name()
			if len(name) != 64+len(".json") || name[64:] != ".json" || !objectNameRe.MatchString(name[:64]) {
				continue
			}
			if rec, ok := readUploadRecord(coralDir, s.Name(), name[:64]); ok {
				out[name[:64]] = append(out[name[:64]], artifactRecord{owner: recordPrincipal(rec), board: rec.Board})
			}
		}
	}
	received, err := os.ReadDir(filepath.Join(coralDir, "artifacts", "received"))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, b := range received {
		if !b.IsDir() || !boardDirName.MatchString(b.Name()) {
			continue
		}
		dir := filepath.Join(coralDir, "artifacts", "received", b.Name())
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, f := range files {
			name := f.Name()
			if len(name) != 64+len(".json") || name[64:] != ".json" || !objectNameRe.MatchString(name[:64]) || (wantID != "" && name[:64] != wantID) {
				continue
			}
			data, err := readSmallFile(filepath.Join(dir, name), 64<<10)
			if err != nil {
				continue
			}
			var row crossTeamArtifactRecord
			if json.Unmarshal(data, &row) != nil || row.Board == "" {
				continue
			}
			owner, err := artifactCircle.BoardOwner(ctx, row.Board)
			if err != nil {
				return nil, err
			}
			out[name[:64]] = append(out[name[:64]], artifactRecord{owner: owner, board: row.Board})
		}
	}
	return out, nil
}

// readableByCircle is the rule itself, for an agent whose team is board and whose team's owner is owner, over the records of one artifact.
func readableByCircle(ctx context.Context, recs []artifactRecord, owner artifactPrincipal, boardKey string) bool {
	if len(recs) == 0 {
		return owner == operatorPrincipal // a legacy object with no record at all is the machine owner's
	}
	for _, rec := range recs {
		if rec.owner == owner {
			return true
		}
		if rec.board != "" && boardKey != "" && crossTeamReadOpen(ctx, boardKey, rec.board) {
			return true
		}
	}
	return false
}

// agentSessionOf is the verified agent session behind a request, if the request came from an agent.
func agentSessionOf(r *http.Request) (sessionID string, isAgent bool) {
	if so, ok := VerifiedSession(r.Context()); ok && so != nil {
		return so.SessionID, true
	}
	if callerActor(r).Kind == ActorToken {
		return "", true // a token actor the identity layer did not bind to a session: no circle, so refused
	}
	return "", false
}

// artifactReadable is the one read check: whether the caller of r may read artifact id. The machine owner, the dashboard, the signed command line, an API key and a request with no credential
// (an install that opted out of strict local auth) read everything; through Coral Hub refuseNonOwner has already decided. An agent reads what its team's owner owns.
func artifactReadable(r *http.Request, coralDir, id string) bool {
	if cloud.Tunneled(r) {
		return true
	}
	sid, isAgent := agentSessionOf(r)
	if !isAgent {
		return true
	}
	if sid == "" {
		return false
	}
	ctx := r.Context()
	owner, boardKey, err := circleOf(ctx, sid)
	if err != nil {
		slog.Warn("[artifacts] the read check could not place an agent; refused", "error", err)
		return false
	}
	all, err := artifactRecordsOf(ctx, coralDir, id)
	if err != nil {
		slog.Warn("[artifacts] the read check could not read the records; refused", "error", err)
		return false
	}
	return readableByCircle(ctx, all[id], owner, boardKey)
}

// filterReadable drops the items an agent may not read from a list, with one pass over the records for the whole list. Items with no artifact id of the store (a link the team never
// uploaded) stay: there is nothing of the store to read. Not an agent (or Coral Hub, already decided): the list is unchanged. An error empties an agent's list.
func filterReadable(r *http.Request, coralDir string, items []teamArtifact) []teamArtifact {
	if cloud.Tunneled(r) {
		return items
	}
	sid, isAgent := agentSessionOf(r)
	if !isAgent {
		return items
	}
	if sid == "" {
		return nil
	}
	ctx := r.Context()
	owner, boardKey, err := circleOf(ctx, sid)
	if err != nil {
		slog.Warn("[artifacts] the read check could not place an agent; its list is empty", "error", err)
		return nil
	}
	all, err := artifactRecordsOf(ctx, coralDir, "")
	if err != nil {
		slog.Warn("[artifacts] the read check could not read the records; the list is empty", "error", err)
		return nil
	}
	out := make([]teamArtifact, 0, len(items))
	for _, it := range items {
		id := it.managed
		if id == "" && objectNameRe.MatchString(it.ID) {
			id = it.ID
		}
		if id == "" || readableByCircle(ctx, all[id], owner, boardKey) {
			out = append(out, it)
		}
	}
	return out
}

// launchOwner is who a board made by this request is owned by: an agent's own team's owner (an agent never makes a board its owner does not own), a hub user who launched it through
// Coral Hub, else the machine owner. ok is false when an agent could not be placed.
func launchOwner(r *http.Request) (artifactPrincipal, bool) {
	if sid, isAgent := agentSessionOf(r); isAgent {
		if sid == "" {
			return artifactPrincipal{}, false
		}
		owner, _, err := circleOf(r.Context(), sid)
		if err != nil {
			return artifactPrincipal{}, false
		}
		return owner, true
	}
	if cloud.Tunneled(r) {
		if a := callerActor(r); a.Kind == ActorHub && a.Name != "Operator" && hubUserRe.MatchString(a.HubUser) {
			return artifactPrincipal{Kind: principalHubUser, ID: a.HubUser}, true
		}
	}
	return operatorPrincipal, true
}

// mayLaunchOnto says whether the caller may put a new agent on board boardKey: an agent only onto a team its own owner owns (a launch is how an agent is given a team, so an agent that could name any
// board could give itself another owner's artifacts). Everyone else is as before.
func mayLaunchOnto(r *http.Request, boardKey string) bool {
	sid, isAgent := agentSessionOf(r)
	if !isAgent {
		return true
	}
	if sid == "" {
		return false
	}
	ctx := r.Context()
	owner, _, err := circleOf(ctx, sid)
	if err != nil {
		return false
	}
	target, err := artifactCircle.BoardOwner(ctx, boardKey)
	if err != nil {
		return false
	}
	return target == owner
}
