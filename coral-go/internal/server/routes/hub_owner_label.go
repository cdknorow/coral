// Confirming who owns this computer on Coral Hub without linking it again (#6108). A computer linked before Coral recorded the owner label refuses the owner's own requests on the owner-only
// routes (hub_owner.go). The person at this computer presses "Confirm owner", which ARMS the adoption for a minute; the first owner-only request the Hub stamps for the owner alone, over this
// computer's own enrolled tunnel, in that minute is adopted as the owner label. Nothing replaces a recorded label, and nothing here is reachable through Coral Hub.

package routes

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/httputil"
)

const (
	ownerArmWindow = time.Minute // how long a press of Confirm owner is good for
	ownerArmsMax   = 3           // presses per hour
	ownerArmsSpan  = time.Hour
)

// ownerLabelStore is the part of the enrolment record this uses.
type ownerLabelStore interface {
	OwnerUser(ctx context.Context) (string, error)
	ServerID(ctx context.Context) (string, error)
	AdoptOwnerUser(ctx context.Context, serverID, user, at string) (bool, error)
	OwnerAdoptedAt(ctx context.Context) (string, error)
}

// ownerArm is the armed adoption: in memory only (a restart disarms it), one-shot, and the presses of the last hour for the rate limit.
var ownerArm = struct {
	sync.Mutex
	until time.Time
	arms  []time.Time
}{}

// ownerNow is the clock (a test replaces it).
var ownerNow = time.Now

// hubOwnerAdopt records a label for the enrolment when adoption is allowed (the connected hub is the enrolled one); set by SetOwnerLabelStore.
var hubOwnerAdopt func(ctx context.Context, user string) bool

// armOwnerAdoption arms the adoption for ownerArmWindow, unless it was pressed ownerArmsMax times in the last hour (then ok is false and retry says how long to wait).
func armOwnerAdoption(now time.Time) (until time.Time, ok bool, retry time.Duration) {
	ownerArm.Lock()
	defer ownerArm.Unlock()
	kept := ownerArm.arms[:0]
	for _, t := range ownerArm.arms {
		if now.Sub(t) < ownerArmsSpan {
			kept = append(kept, t)
		}
	}
	ownerArm.arms = kept
	if len(kept) >= ownerArmsMax {
		return time.Time{}, false, ownerArmsSpan - now.Sub(kept[0])
	}
	ownerArm.arms = append(ownerArm.arms, now)
	ownerArm.until = now.Add(ownerArmWindow)
	return ownerArm.until, true, 0
}

// takeOwnerArm reports whether the adoption is armed and not expired, and disarms it either way: one request gets the chance, whatever it turns out to be.
func takeOwnerArm(now time.Time) bool {
	ownerArm.Lock()
	defer ownerArm.Unlock()
	armed := !ownerArm.until.IsZero() && now.Before(ownerArm.until)
	ownerArm.until = time.Time{}
	return armed
}

func ownerArmedUntil(now time.Time) time.Time {
	ownerArm.Lock()
	defer ownerArm.Unlock()
	if !ownerArm.until.IsZero() && now.Before(ownerArm.until) {
		return ownerArm.until
	}
	return time.Time{}
}

// SetOwnerLabelStore attaches where the owner label is read and adopted.
func (h *CloudHandler) SetOwnerLabelStore(s ownerLabelStore) {
	h.ownerLabels = s
	hubOwnerAdopt = h.adoptOwner
}

// adoptOwner records user as the owner label when this computer is linked, the tunnel is up to the enrolled hub, and no label is recorded.
func (h *CloudHandler) adoptOwner(ctx context.Context, user string) bool {
	if h.ownerLabels == nil || !h.connectedToEnrolledHub() {
		slog.Warn("[hub] the owner was not adopted: Coral Hub's tunnel is not up to the hub this computer was linked to")
		return false
	}
	sid, err := h.ownerLabels.ServerID(ctx)
	if err != nil || sid == "" {
		return false
	}
	ok, err := h.ownerLabels.AdoptOwnerUser(ctx, sid, user, ownerNow().UTC().Format(time.RFC3339))
	if err != nil || !ok {
		return false
	}
	slog.Info("[hub] the owner of this computer was confirmed from Coral Hub's first owner request after Confirm owner") // never the label itself
	return true
}

// connectedToEnrolledHub: the connector is up and the enrolment is for the hub it is configured for.
func (h *CloudHandler) connectedToEnrolledHub() bool {
	if h.connected != nil {
		return h.connected()
	}
	m := h.manager()
	if m == nil {
		return false
	}
	st := m.Status()
	return st.State == cloud.StateConnected && !st.HubMoved
}

// ownerLabelState says whether this enrolment recorded an owner label. known is false when it could not be read; missing is true only for a linked computer whose record holds none.
func (h *CloudHandler) ownerLabelState(ctx context.Context) (missing, known bool) {
	if h.ownerLabels == nil {
		return false, false
	}
	l, err := h.ownerLabels.OwnerUser(ctx)
	if err != nil {
		return false, false
	}
	if h.linkedNow() {
		return l == "", true
	}
	return false, true
}

func (h *CloudHandler) linkedNow() bool {
	if h.linked != nil {
		return h.linked()
	}
	if m := h.manager(); m != nil {
		return m.Status().State != cloud.StateOff
	}
	return false
}

// hubHost is the host of the hub this computer is linked to, for the person to read ("" when unknown).
func (h *CloudHandler) hubHost() string {
	m := h.manager()
	if m == nil {
		return ""
	}
	if u, err := url.Parse(m.Status().HubURL); err == nil {
		return u.Host
	}
	return ""
}

// OwnerLabel: GET /api/cloud/owner-label (this computer's dashboard only). {"label_missing": bool, "armed_until": RFC 3339 or "", "hub": host, "candidate": "hub-user-xxxxxxxx" or "",
// "adopted_at": RFC 3339 or ""}: the candidate is the one hub user the Hub stamped as the owner in the last hour, shown so the person knows who Confirm owner will trust; with several, none.
func (h *CloudHandler) OwnerLabel(w http.ResponseWriter, r *http.Request) {
	if h.refuseCloud(w, r, allowDashboard) {
		return
	}
	missing, known := h.ownerLabelState(r.Context())
	if !known {
		errInternalServer(w, "the owner record could not be read")
		return
	}
	now := ownerNow()
	reply := map[string]any{"label_missing": missing, "armed_until": "", "hub": h.hubHost(), "candidate": "", "candidate_seen_at": "", "several": false, "adopted_at": ""}
	if at, err := h.ownerLabels.OwnerAdoptedAt(r.Context()); err == nil && at != "" {
		reply["adopted_at"] = at
	}
	if missing {
		user, at, several := ownerCandidate(now)
		reply["candidate"], reply["several"] = user, several
		if user != "" {
			reply["candidate_seen_at"] = at.UTC().Format(time.RFC3339)
		}
		if until := ownerArmedUntil(now); !until.IsZero() {
			reply["armed_until"] = until.UTC().Format(time.RFC3339)
		}
	}
	writeJSON(w, http.StatusOK, reply)
}

// ArmOwnerLabel: POST /api/cloud/owner-label/arm {} (this computer's dashboard only; never through Coral Hub). It arms the adoption for a minute, only while no owner label is recorded and the
// tunnel is up to the enrolled hub, and at most three times an hour. The label itself is recorded by the first owner-only request the Hub stamps for the owner in that minute (adoptOwnerFromRequest).
func (h *CloudHandler) ArmOwnerLabel(w http.ResponseWriter, r *http.Request) {
	if h.refuseCloud(w, r, allowDashboard) || !requireJSON(w, r) {
		return
	}
	var raw map[string]any
	if err := decodeJSONMax(w, r, &raw, 1<<10); err != nil || len(raw) != 0 {
		errBadRequest(w, `the body is exactly {}`)
		return
	}
	missing, known := h.ownerLabelState(r.Context())
	switch {
	case !known:
		errInternalServer(w, "the owner record could not be read")
		return
	case !missing:
		writeJSON(w, http.StatusConflict, map[string]string{"code": "owner_label_set", "error": "this computer already has an owner recorded, or is not linked to Coral Hub"})
		return
	case !h.connectedToEnrolledHub():
		writeJSON(w, http.StatusConflict, map[string]string{"code": "hub_not_connected", "error": "Coral Hub is not connected to the hub this computer was linked to"})
		return
	}
	until, ok, retry := armOwnerAdoption(ownerNow())
	if !ok {
		httputil.WriteActionLimit(w, int(retry/time.Second)+1, "too_soon", "Confirm owner was pressed a few times already: try again later")
		return
	}
	user, _, _ := ownerCandidate(ownerNow())
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "armed_until": until.UTC().Format(time.RFC3339), "seconds": int(ownerArmWindow / time.Second), "hub": h.hubHost(), "candidate": user})
}
