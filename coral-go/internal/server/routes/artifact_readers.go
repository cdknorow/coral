package routes

// Who can read an artifact, for its owner to see (#5577, note #5576 v2 section 6). GET /api/artifacts/{id}/readers says, from the same records the read check uses, which owners hold the
// artifact and who therefore reads it: the machine owner, the agents of the teams each owner owns, and (once Coral Hub links exist, #5572) the teams linked to the board. Owner only: the
// Operator on this computer, or through Coral Hub the computer's owner. An agent, a script and any other hub caller are refused; an artifact that does not exist is 404.

import (
	"net/http"
	"path/filepath"
	"sort"

	"github.com/go-chi/chi/v5"

	"github.com/subgentic/coral/internal/cloud"
)

// artifactReaderTeam is a team a record sits on.
type artifactReaderTeam struct {
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
}

// artifactReaderOwner is one owner that holds the artifact, with what that means.
type artifactReaderOwner struct {
	Kind    string               `json:"kind"`
	ID      string               `json:"id"`
	Records int                  `json:"records"`
	Teams   []artifactReaderTeam `json:"teams"`
	Reason  string               `json:"reason"`
}

// artifactReaders: GET /api/artifacts/{id}/readers
func (h *SessionsHandler) artifactReaders(w http.ResponseWriter, r *http.Request) {
	if cloud.Tunneled(r) {
		if refuseNonOwner(w, r) {
			return
		}
	} else if refuseNonOperator(w, r, "see who can read an artifact") {
		return
	}
	id := chi.URLParam(r, "id")
	if !validArtifactID(id) {
		errBadRequest(w, "invalid artifact id")
		return
	}
	if _, ok := readSidecar(filepath.Join(h.artifactDir(), id+".json")); !ok {
		errNotFound(w, "artifact not found")
		return
	}
	ctx := r.Context()
	all, err := artifactRecordsOf(ctx, h.cfg.CoralDir(), id)
	if err != nil {
		errInternalServer(w, "could not read this artifact's records")
		return
	}
	recs := all[id]
	if len(recs) == 0 { // an object with no record is the machine owner's
		recs = []artifactRecord{{owner: operatorPrincipal}}
	}
	byOwner := map[artifactPrincipal]*artifactReaderOwner{}
	var ids []string
	for _, rec := range recs {
		o := byOwner[rec.owner]
		if o == nil {
			o = &artifactReaderOwner{Kind: rec.owner.Kind, ID: rec.owner.ID, Teams: []artifactReaderTeam{}}
			byOwner[rec.owner] = o
		}
		o.Records++
		if rec.board != "" {
			o.Teams = append(o.Teams, artifactReaderTeam{ID: rec.board})
			ids = append(ids, rec.board)
		}
	}
	labels := map[string]string{}
	if h.bs != nil && len(ids) > 0 {
		labels = h.bs.BoardLabels(ctx, ids)
	}
	owners := make([]artifactReaderOwner, 0, len(byOwner))
	for _, o := range byOwner {
		seen := map[string]bool{}
		teams := o.Teams[:0]
		for _, t := range o.Teams {
			if seen[t.ID] {
				continue
			}
			seen[t.ID] = true
			t.Label = labels[t.ID]
			teams = append(teams, t)
		}
		o.Teams = teams
		o.Reason = "the agents of every team this owner owns read it"
		owners = append(owners, *o)
	}
	sort.Slice(owners, func(i, j int) bool {
		if owners[i].Kind != owners[j].Kind {
			return owners[i].Kind < owners[j].Kind
		}
		return owners[i].ID < owners[j].ID
	})
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"id":            id,
		"machine_owner": "reads, manages and deletes every artifact on this computer",
		"owners":        owners,
		// Teams linked to the owner's through Coral Hub read the board named in the link, one way, while messaging is on and the link is fresh: none exists until #5572.
		"linked_teams": []artifactReaderTeam{},
		"note":         "This is a control on Coral's API, not an operating-system boundary: every agent on this computer runs as the same user and can read Coral's data folder with its own shell.",
	})
}
