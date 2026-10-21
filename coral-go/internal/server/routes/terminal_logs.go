package routes

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/safefs"
	"github.com/subgentic/coral/internal/store"
)

// The terminal logs of ended agents (#4447): since #4437 Kill keeps an agent's terminal log on both backends, so the only way to remove those logs
// was to delete files by hand from the logs folder. Settings > Storage shows how many logs belong to agents that have ended and how big they are, and
// the Operator can delete them after a confirmation. Never automatic. A log is an ended agent's only when the sessions database has that session,
// of the same agent type, stopped, not sleeping (wake needs the log) and not archived; anything else, or anything the database cannot say, is kept.
// Files are removed by exact name through safefs.Root, inside the logs folder only, never by a pattern.

// terminalLogName reads <agent type>_coral_<session id>.log: the name naming.LogFile builds, and nothing else.
var terminalLogName = regexp.MustCompile(`^([A-Za-z0-9_]{1,64}?)_coral_(.+)\.log$`)

type terminalLog struct {
	Name string
	Size int64
}

// terminalLogScan is what a look at the logs folder found: every terminal log, and those of ended agents.
type terminalLogScan struct {
	Files, Bytes int64
	Ended        []terminalLog
	EndedBytes   int64
}

// RegisterTerminalLogs adds the terminal-log cleanup routes.
func (h *SessionsHandler) RegisterTerminalLogs(r chi.Router) {
	r.Get("/api/system/terminal-logs", h.terminalLogsStatus)
	r.Post("/api/system/terminal-logs/delete-ended", h.deleteEndedTerminalLogs)
}

var errTerminalLogsUnavailable = errors.New("could not check the terminal logs")

// scanTerminalLogs lists the logs folder and decides, from the sessions database, which logs belong to ended agents. Any error fails the whole scan
// (nothing is deleted on a guess); a logs folder that is not there is an empty result.
func (h *SessionsHandler) scanTerminalLogs(ctx context.Context) (*terminalLogScan, error) {
	out := &terminalLogScan{}
	dir := h.cfg.LogDir
	if dir == "" || h.ss == nil {
		return nil, errTerminalLogsUnavailable
	}
	root, err := safefs.OpenRoot(dir)
	if errors.Is(err, safefs.ErrRootGone) {
		return out, nil
	}
	if err != nil {
		return nil, errTerminalLogsUnavailable
	}
	defer root.Close()
	entries, err := os.ReadDir(root.Dir())
	if err != nil {
		return nil, errTerminalLogsUnavailable
	}
	states, err := h.ss.LiveSessionStates(ctx)
	if err != nil {
		return nil, errTerminalLogsUnavailable
	}
	for _, e := range entries {
		m := terminalLogName.FindStringSubmatch(e.Name())
		if m == nil || !e.Type().IsRegular() {
			continue
		}
		agentType, id := m[1], m[2]
		if naming.LogFile(root.Dir(), agentType, id) == "" || !strings.HasSuffix(e.Name(), ".log") {
			continue // not a name Coral builds
		}
		fi, err := root.Lstat(e.Name())
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		out.Files++
		out.Bytes += fi.Size()
		if st, ok := states[id]; ok && st.AgentType == agentType && st.Status == "inactive" && !st.Sleeping && !st.Archived {
			out.Ended = append(out.Ended, terminalLog{Name: e.Name(), Size: fi.Size()})
			out.EndedBytes += fi.Size()
		}
	}
	return out, nil
}

// terminalLogsStatus: GET /api/system/terminal-logs, the logs' count and size, and those of ended agents. Operator only, like the delete (the names and sizes of other agents'
// logs are not an agent's to read), and this computer's dashboard only (not in the tunnel table).
func (h *SessionsHandler) terminalLogsStatus(w http.ResponseWriter, r *http.Request) {
	if refuseNonOperator(w, r, "see the terminal logs") {
		return
	}
	scan, err := h.scanTerminalLogs(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "logs_unavailable", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": scan.Files, "bytes": scan.Bytes, "ended": map[string]any{"count": len(scan.Ended), "bytes": scan.EndedBytes}})
}

// deleteEndedTerminalLogs: POST /api/system/terminal-logs/delete-ended {"count": n, "bytes": b}: the Operator confirms what Settings showed. If the logs
// of ended agents are not exactly that many and that big any more, nothing is deleted and the answer says so (409), so the confirmation is always for what
// is removed. Operator only (a person: the dashboard or the signed command line), never through Coral Hub.
func (h *SessionsHandler) deleteEndedTerminalLogs(w http.ResponseWriter, r *http.Request) {
	if refuseNonOperator(w, r, "delete terminal logs") {
		return
	}
	var body struct {
		Count *int   `json:"count"`
		Bytes *int64 `json:"bytes"`
	}
	if err := decodeJSONLimit(r, &body, 4096); err != nil || body.Count == nil || body.Bytes == nil {
		errBadRequest(w, "send the count and size you were shown")
		return
	}
	scan, err := h.scanTerminalLogs(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "logs_unavailable", err.Error()+", so nothing was deleted")
		return
	}
	if *body.Count != len(scan.Ended) || *body.Bytes != scan.EndedBytes {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "the logs of ended agents changed since you looked: look again", "code": "changed", "ended": map[string]any{"count": len(scan.Ended), "bytes": scan.EndedBytes}})
		return
	}
	root, err := safefs.OpenRoot(h.cfg.LogDir)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "logs_unavailable", errTerminalLogsUnavailable.Error()+", so nothing was deleted")
		return
	}
	defer root.Close()
	var freed int64
	deleted := 0
	failed := []string{}
	// Asked again for each log just before it is removed: an agent woken, or archived, since the scan (or since the previous removal) keeps its log.
	for _, l := range scanEndedIDs(scan) {
		if terminalLogsBeforeRecheck != nil {
			terminalLogsBeforeRecheck(l.Name)
		}
		if !h.logStillEnded(r.Context(), l.Name) {
			failed = append(failed, l.Name+": the agent is not ended any more")
			continue
		}
		if err := root.Remove(l.Name); err != nil {
			failed = append(failed, l.Name+": could not be removed")
			continue
		}
		deleted++
		freed += l.Size
	}
	slog.Info("[logs] terminal logs of ended agents deleted by the Operator", "deleted", deleted, "bytes", freed, "failed", len(failed))
	writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted, "bytes": freed, "failed": failed})
}

// terminalLogsBeforeRecheck, when set (tests), runs before each log's second look at its session: where an agent wakes up meanwhile.
var terminalLogsBeforeRecheck func(name string)

// logStillEnded reads the session of the log called name afresh. A log it cannot check is kept.
func (h *SessionsHandler) logStillEnded(ctx context.Context, name string) bool {
	m := terminalLogName.FindStringSubmatch(name)
	if m == nil {
		return false
	}
	st, ok, err := h.ss.LiveSessionStateByID(ctx, m[2])
	if err != nil || !ok {
		return false
	}
	return stillEnded(map[string]store.LiveSessionState{m[2]: st}, name)
}

func scanEndedIDs(scan *terminalLogScan) []terminalLog { return scan.Ended }

// stillEnded repeats the ended test for the log called name against fresh session states.
func stillEnded(states map[string]store.LiveSessionState, name string) bool {
	m := terminalLogName.FindStringSubmatch(name)
	if m == nil {
		return false
	}
	st, ok := states[m[2]]
	return ok && st.AgentType == m[1] && st.Status == "inactive" && !st.Sleeping && !st.Archived
}
