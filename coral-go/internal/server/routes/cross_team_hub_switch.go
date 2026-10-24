package routes

// Coral Hub's owner turns a team's two cross-team switches on or off (#5572, Security's owner-path conditions): four dedicated routes, each one team and one switch, no body.
//
//	POST /api/board/{project}/cross-team/enable        cross-team messaging on
//	POST /api/board/{project}/cross-team/disable       cross-team messaging off
//	POST /api/board/{project}/cross-team/files/enable  file sharing with other teams on
//	POST /api/board/{project}/cross-team/files/disable file sharing with other teams off
//
// Who: the Coral Hub owner only (the connector's stamp, the owner marker stamped on these routes only, the enrolled owner label: hubOwnerRequest). A person on this computer uses the settings route;
// an agent, the dashboard and everyone else are refused here. The local lock "Let Coral Hub change cross-team settings" (HubCrossTeamSetting, default on) refuses BOTH directions when it is off; a lock
// that cannot be read refuses too. The change and its Operator-actions row are one transaction (board.SetSwitchByHub): if either cannot be written the switch is unchanged. Every request that
// gets this far is announced on the team's board by Coral from a fixed template with the validated hub user (a request for the value the switch already has too). At most 10 a minute per team.
// The settings PUT stays local-only for these two keys.

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/httputil"
	"github.com/subgentic/coral/pkg/tunnel"
)

// isHubSwitchRoute says whether r is a POST to one of the four switch routes: the POST routes the route table marks owner-only (the only other owner POST, the update relaunch, is
// owner-update, not owner-only). A census test pins the set to the four.
func isHubSwitchRoute(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	_, mode := tunnel.OwnerRoute(r.Method, r.URL.Path)
	return mode.Refuses()
}

// hubSwitchPerMinute is how many requests one team takes from Coral Hub in a minute.
const hubSwitchPerMinute = 10

// hubSwitchLimiter counts requests per team per clock minute; its size is bounded.
type hubSwitchLimiter struct {
	mu     sync.Mutex
	window time.Time
	counts map[string]int
}

// allowHubSwitch takes one of the team's hubSwitchPerMinute requests for this minute; false, and the seconds until the next minute, when they are used up.
func (h *BoardHandler) allowHubSwitch(project string) (bool, int) {
	now := time.Now()
	if h.crossTeam.now != nil {
		now = h.crossTeam.now()
	}
	now = now.UTC()
	win := now.Truncate(time.Minute)
	l := &h.hubSwitches
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.window.Equal(win) || l.counts == nil || len(l.counts) > 4096 {
		l.window, l.counts = win, map[string]int{}
	}
	if l.counts[project] >= hubSwitchPerMinute {
		wait := int(win.Add(time.Minute).Sub(now).Seconds()) + 1
		return false, wait
	}
	l.counts[project]++
	return true, 0
}

// SetHubCrossTeamLock tells the handler how to read the local lock. Unset, or a read that fails, refuses the Hub (fail closed).
func (h *BoardHandler) SetHubCrossTeamLock(fn func(ctx context.Context) (bool, error)) {
	h.hubCrossTeamLock = fn
}

func (h *BoardHandler) hubMayChange(ctx context.Context) bool {
	if h.hubCrossTeamLock == nil {
		return false
	}
	ok, err := h.hubCrossTeamLock(ctx)
	return err == nil && ok
}

// HubCrossTeamLockFromSettings reads the lock from the settings store on every request.
func HubCrossTeamLockFromSettings(read func(ctx context.Context) (map[string]string, error)) func(context.Context) (bool, error) {
	return func(ctx context.Context) (bool, error) {
		s, err := read(ctx)
		if err != nil {
			return false, err
		}
		return HubCrossTeamAllowed(s), nil
	}
}

// HubCrossTeamEnable, HubCrossTeamDisable, HubCrossTeamFilesEnable and HubCrossTeamFilesDisable are the four routes.
func (h *BoardHandler) HubCrossTeamEnable(w http.ResponseWriter, r *http.Request) {
	h.setSwitchForHub(w, r, board.SwitchMessaging, true)
}
func (h *BoardHandler) HubCrossTeamDisable(w http.ResponseWriter, r *http.Request) {
	h.setSwitchForHub(w, r, board.SwitchMessaging, false)
}
func (h *BoardHandler) HubCrossTeamFilesEnable(w http.ResponseWriter, r *http.Request) {
	h.setSwitchForHub(w, r, board.SwitchFiles, true)
}
func (h *BoardHandler) HubCrossTeamFilesDisable(w http.ResponseWriter, r *http.Request) {
	h.setSwitchForHub(w, r, board.SwitchFiles, false)
}

func (h *BoardHandler) setSwitchForHub(w http.ResponseWriter, r *http.Request, key string, on bool) {
	w.Header().Set("Cache-Control", "no-store")
	if refuseNonOwner(w, r) { // the table's owner route: the owner's alone
		return
	}
	id := hubOwnerRequest(r)
	project := urlParam(r, "project")
	if ok, wait := h.allowHubSwitch(project); !ok {
		httputil.WriteActionLimit(w, wait, "hub_switch_rate_limited", textHubSwitchRateLimited)
		return
	}
	if r.URL.RawQuery != "" {
		errBadRequest(w, textHubSwitchNoBody)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 33))
	if err != nil || len(raw) > 32 || (strings.TrimSpace(string(raw)) != "" && strings.TrimSpace(string(raw)) != "{}") {
		errBadRequest(w, textHubSwitchNoBody)
		return
	}
	ctx := r.Context()
	if !h.hubMayChange(ctx) {
		writeError(w, http.StatusForbidden, "hub_changes_off", textHubChangesOff)
		return
	}
	if exists, err := h.bs.BoardExists(ctx, project); err != nil {
		errInternalServer(w, err.Error())
		return
	} else if !exists {
		writeError(w, http.StatusNotFound, "not_found", "no such team")
		return
	}
	change, changed, err := h.bs.SetSwitchByHub(context.WithoutCancel(ctx), project, key, on, id.pseudo, id.hubUserID, r.Method, r.URL.Path)
	if err != nil {
		slog.Warn("[cloud] Coral Hub's change of a cross-team switch was not recorded and not applied", "board", project, "key", key, "error", err)
		writeError(w, http.StatusInternalServerError, "record_failed", textHubSwitchNotRecorded)
		return
	}
	announced := false
	if _, err := h.bs.PostHubNotice(context.WithoutCancel(ctx), project, textHubSwitchNotice(id.pseudo, key == board.SwitchFiles, on, changed)); err != nil {
		slog.Warn("[cloud] could not post the notice of a Coral Hub switch change", "board", project, "error", err)
	} else {
		announced = true
		if h.notifyFn != nil {
			h.notifyFn(project)
		}
	}
	slog.Info("[cloud] Coral Hub's owner set a cross-team switch", "board", project, "key", key, "on", on, "changed", changed, "hub_user", id.pseudo)
	settings, err := h.bs.GetBoardSettings(ctx, project)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "changed": changed, "announced": announced, "at": change.At})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "changed": changed, "announced": announced, "at": change.At,
		"cross_team_messaging": settings.CrossTeamMessaging, "cross_team_artifacts": settings.CrossTeamArtifacts})
}
