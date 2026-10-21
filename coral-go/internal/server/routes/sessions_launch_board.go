// The board a launched agent joins: its role, labels and state file (moved out of sessions_launch.go, #5003; no behaviour change).

package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/hooks"
	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/testguard"
)

// setupBoardAndPrompt subscribes a session to a message board.
// The agent prompt is now passed directly as a CLI positional argument
// in launchSession, so no tmux-based prompt delivery is needed.
// subscriberID is the stable identity (role name), sessionName is the mutable tmux session.
func (h *SessionsHandler) setupBoardAndPrompt(sessionID, sessionName, agentType, boardName, displayName string) {
	role := naming.SubscriberID(displayName, agentType)
	subscriberID := role // stable board identity = role name
	ctx := context.Background()

	if boardName == "" {
		return
	}

	// Board subscription (immediate — no delay needed)
	if h.bs != nil {
		isOrchestrator := strings.Contains(strings.ToLower(role), "orchestrator")
		receiveMode := "mentions"
		if isOrchestrator {
			receiveMode = "all"
		}
		if agentType == "terminal" {
			receiveMode = "none"
		}

		// Preserve existing receive_mode on re-subscribe (e.g. restart).
		// Use session-name lookup to find the subscription for THIS session,
		// not a stale one from a previous board with the same subscriber_id.
		existing, err := h.bs.GetSubscriptionBySessionName(ctx, sessionName)
		if err == nil && existing != nil && existing.ReceiveMode != "" {
			receiveMode = existing.ReceiveMode
		}

		if _, err := h.bs.Subscribe(ctx, boardName, subscriberID, role, sessionName, nil, receiveMode, isOrchestrator); err != nil {
			log.Printf("Failed to subscribe session %s to board %s: %v", sessionID[:8], boardName, err)
		}
		// The subscription now holds the name; the reservation that covered the launch goes.
		h.releaseRoleReservation(boardName, subscriberID)
	}

	// Write local board state file so coral-board CLI can find its subscription
	writeBoardStateFile(sessionName, boardName, role, h.cfg)
}

// writeBoardStateFile writes the local board state file that coral-board CLI
// reads to determine which board a session is subscribed to.
func writeBoardStateFile(sessionName, boardName, role string, cfg *config.Config) {
	if !plainFileName(sessionName) {
		return // only a plain name can be part of a file name (#3602 W25)
	}
	stateDir := ""
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		stateDir = filepath.Join(home, ".coral")
	}
	if cfg != nil {
		stateDir = cfg.CoralDir()
	}
	if stateDir == "" {
		// No data dir (a Config not built by config.Load, or no home
		// directory): never resolve the file against the working directory.
		return
	}
	testguard.CheckDataPath(stateDir)
	os.MkdirAll(stateDir, 0o700)

	state := map[string]string{
		"project":   boardName,
		"job_title": role,
	}
	// Persist the explicit loopback URL even on the default port. Codex's
	// sandbox can strip CORAL_URL/CORAL_PORT from child processes; without
	// this persisted value coral-board falls back to localhost, which may
	// resolve to IPv6 ::1 and be denied while IPv4 loopback is available.
	if cfg != nil {
		host := cfg.AgentHost() // the loopback listener whenever there is one (#3594)
		state["server_url"] = fmt.Sprintf("http://%s", net.JoinHostPort(host, strconv.Itoa(cfg.Port)))
	}

	data, _ := json.Marshal(state)
	statePath := filepath.Join(stateDir, hooks.BoardStateFileName(sessionName))
	// 0600: it names the agent's board and server (#3581).
	if err := os.WriteFile(statePath, data, 0o600); err != nil {
		log.Printf("Failed to write board state file for %s: %v", sessionName, err)
	} else {
		os.Chmod(statePath, 0o600) // a file an older version wrote 0644
	}
}

// removeBoardStateFile deletes the board state file for a session.
// Called when a session is killed so state files don't accumulate.
func removeBoardStateFile(sessionName string, cfg *config.Config) {
	if cfg == nil || cfg.CoralDir() == "" {
		return // no data dir: nothing was written (see writeBoardStateFile)
	}
	if !plainFileName(sessionName) {
		return // nothing was written for it (writeBoardStateFile)
	}
	statePath := filepath.Join(cfg.CoralDir(), hooks.BoardStateFileName(sessionName))
	os.Remove(statePath) // Ignore error — file may not exist
}

// boardProject returns the board project name for a session, checking
// board subscriptions first, then falling back to live_sessions DB.
// boardSubscriberFlag exposes the authoritative CanPeek role without using
// mutable display names or job-title substring matching. Nil means no board.
func boardSubscriberFlag(sub *board.Subscriber) any {
	if sub == nil {
		return nil
	}
	return sub.CanPeek != 0
}

func boardProject(subs map[string]*board.Subscriber, fallback map[string][2]string, tmuxName, sessionID string) any {
	if sub, ok := subs[tmuxName]; ok {
		return sub.Project
	}
	if fb, ok := fallback[sessionID]; ok && fb[0] != "" {
		return fb[0]
	}
	return nil
}

// boardJobTitle returns the board job title for a session.
func boardJobTitle(subs map[string]*board.Subscriber, fallback map[string][2]string, tmuxName, sessionID string) any {
	if sub, ok := subs[tmuxName]; ok {
		return sub.JobTitle
	}
	if fb, ok := fallback[sessionID]; ok && fb[1] != "" {
		return fb[1]
	}
	return nil
}

// boardLabel is a board's display name for an agent's prompt (#3536); "" if unknown.
func (h *SessionsHandler) boardLabel(key string) string {
	if h.bs == nil || key == "" {
		return ""
	}
	return h.bs.BoardLabel(context.Background(), key)
}

var launchBoardMu sync.Mutex

// launchBoard decides the board a launch uses (#3536, the operator's decision): board_id
// joins that board; board_name starts a NEW board (a fresh b- id, that label) unless a board
// with that id or label already exists, which is answered 409 board_name_exists with the
// candidates (join one by its board_id, or send new_board to start another with the same
// name). A new board's name follows the #3534 rule. It returns the board's id, or false
// after answering.
func (h *SessionsHandler) launchBoard(w http.ResponseWriter, r *http.Request, boardID, boardName string, newBoard bool) (string, bool) {
	key, _, ok := h.launchBoardCreated(w, r, boardID, boardName, newBoard)
	return key, ok
}

// launchBoardCreated is launchBoard that also says whether this call made the board (a team launch that starts nobody takes back
// the board it made, and only that one).
func (h *SessionsHandler) launchBoardCreated(w http.ResponseWriter, r *http.Request, boardID, boardName string, newBoard bool) (key string, created, ok bool) {
	ctx := r.Context()
	if h.bs == nil {
		if boardID != "" {
			return boardID, false, true
		}
		return boardName, false, true
	}
	if boardID != "" {
		b, err := h.bs.GetBoard(ctx, boardID)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "board_not_found", "code": "board_not_found", "message": "No board has that id"})
			return "", false, false
		}
		// A launch is how an agent is given a team, and a team decides whose artifacts an agent reads (#5577): an agent may put a new agent only onto a team its own owner owns.
		if !mayLaunchOnto(r, b.ID) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden_for_agent", "code": "forbidden_for_agent", "message": "An agent can start agents only on teams of its own owner"})
			return "", false, false
		}
		return b.ID, false, true
	}
	boardName = strings.TrimSpace(boardName)
	// One check-then-create at a time, so two launches of the same new name can't both see
	// it free and start two boards with one label.
	launchBoardMu.Lock()
	defer launchBoardMu.Unlock()
	var existing []board.Board
	if b, err := h.bs.GetBoard(ctx, boardName); err == nil {
		if inUse, _ := h.bs.BoardExists(ctx, b.ID); inUse {
			existing = append(existing, *b)
		}
	} else if inUse, _ := h.bs.BoardExists(ctx, boardName); inUse {
		existing = append(existing, board.Board{ID: boardName, Label: boardName})
	}
	if named, err := h.bs.BoardsLabelledForLaunch(ctx, boardName); err == nil {
		for _, b := range named {
			if b.ID != boardName {
				existing = append(existing, b)
			}
		}
	}
	// Another board with a name that's taken makes that name ambiguous for every caller who
	// uses it: the Operator's choice (Security's #3536 review). An agent's token is refused
	// with strict local auth, and recorded otherwise.
	if len(existing) > 0 && newBoard && h.agentOperatorGate != nil && h.agentOperatorGate(r, "POST /api/sessions/launch new_board") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden_for_agent", "code": "forbidden_for_agent",
			"message": "An agent can't start another board with a name that's taken: that is for the Operator. Join it with board_id, or pick a new name"})
		return "", false, false
	}
	if len(existing) > 0 && !newBoard {
		// Next to the member count, how many of the board's agents are running (#3543).
		cands := boardCandidates(r, h.bs, existing)
		live := h.liveSessionNames(ctx)
		for _, c := range cands {
			c["live_agents"] = h.liveAgentsOn(ctx, c["board_id"].(string), live)
		}
		writeJSON(w, http.StatusConflict, map[string]any{"error": "board_name_exists", "code": "board_name_exists",
			"message":    "A board named \"" + boardName + "\" already exists: join it (board_id) or start another (new_board)",
			"candidates": cands})
		return "", false, false
	}
	if problem := h.bs.LabelProblem(boardName); problem != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_board_name", "code": "invalid_board_name", "message": problem})
		return "", false, false
	}
	// A board made by an agent is its owner's, never the machine owner's by default; one a hub user launched through Coral Hub is that user's (#5577).
	owner, ok := launchOwner(r)
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden_for_agent", "code": "forbidden_for_agent", "message": "This agent's team could not be established"})
		return "", false, false
	}
	b, err := h.bs.CreateBoard(ctx, boardName, owner.creator())
	if err != nil {
		errInternalServer(w, err.Error())
		return "", false, false
	}
	return b.ID, true, true
}

// addBoardLabels gives every session row with a board ("board_project") its board's display
// name as "board_label" (#3536), with one lookup for the whole list.
func (h *SessionsHandler) addBoardLabels(ctx context.Context, rows []map[string]any) {
	if h.bs == nil {
		return
	}
	var ids []string
	for _, row := range rows {
		if id := boardKeyOf(row["board_project"]); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return
	}
	labels := h.bs.BoardLabels(ctx, ids)
	for _, row := range rows {
		if id := boardKeyOf(row["board_project"]); id != "" {
			row["board_label"] = labels[id]
		}
	}
}

func boardKeyOf(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case *string:
		if t != nil {
			return *t
		}
	}
	return ""
}

// liveSessionNames is the set of session names of every live (running or sleeping) session.
func (h *SessionsHandler) liveSessionNames(ctx context.Context) map[string]bool {
	live := map[string]bool{}
	if h.ss == nil {
		return live
	}
	sessions, err := h.ss.GetAllLiveSessions(ctx)
	if err != nil {
		return live
	}
	for _, ls := range sessions {
		live[naming.SessionName(ls.AgentType, ls.SessionID)] = true
	}
	return live
}

// uniqueBoardRole returns displayName, or "<role> N" (the lowest N from 2) when another
// live agent already holds that role on the board (#3543). A subscription left by an agent
// that is no longer running doesn't count: a relaunched or resurrected member keeps its name.
//
// Names picked in the last two minutes count as taken too (roleReservations): the board
// subscription is written only after the launch, so two launches of one role close together
// (two tabs, a double click) would otherwise both see the role free.
func (h *SessionsHandler) uniqueBoardRole(ctx context.Context, boardName, displayName, agentType string) string {
	if h.bs == nil {
		return displayName
	}
	h.roleRes.mu.Lock()
	defer h.roleRes.mu.Unlock()
	if h.roleRes.at == nil {
		h.roleRes.at = map[string]time.Time{}
	}
	now := time.Now()
	taken := map[string]bool{}
	for key, at := range h.roleRes.at {
		if now.Sub(at) > roleReservationTTL {
			delete(h.roleRes.at, key)
			continue
		}
		if b, name, ok := strings.Cut(key, "\x00"); ok && b == boardName {
			taken[name] = true
		}
	}
	if subs, err := h.bs.ListSubscribers(ctx, boardName); err == nil && len(subs) > 0 {
		live := h.liveSessionNames(ctx)
		for _, s := range subs {
			if s.SessionName != "" && live[s.SessionName] {
				taken[strings.ToLower(s.SubscriberID)] = true
			}
		}
	}
	role := naming.SubscriberID(displayName, agentType)
	pick := displayName
	if taken[strings.ToLower(role)] {
		// A taken numbered name is renumbered from its base role: "Backend Dev 2" becomes
		// "Backend Dev 3", not "Backend Dev 2 2" (QA's #3543 review).
		base := role
		if i := strings.LastIndexByte(role, ' '); i > 0 {
			if _, err := strconv.Atoi(role[i+1:]); err == nil {
				base = role[:i]
			}
		}
		for n := 2; n < 1000; n++ {
			if candidate := fmt.Sprintf("%s %d", base, n); !taken[strings.ToLower(candidate)] {
				pick = candidate
				break
			}
		}
	}
	h.roleRes.at[boardName+"\x00"+strings.ToLower(naming.SubscriberID(pick, agentType))] = now
	return pick
}

// roleReservations holds the names uniqueBoardRole picked recently, per board; one per
// handler (QA's #3543 review: a package-level map leaked between tests).
type roleReservations struct {
	mu sync.Mutex
	at map[string]time.Time
}

const roleReservationTTL = 2 * time.Minute

// releaseRoleReservation drops a name's reservation: its subscription now holds it, or its
// launch failed.
func (h *SessionsHandler) releaseRoleReservation(boardName, role string) {
	h.roleRes.mu.Lock()
	delete(h.roleRes.at, boardName+"\x00"+strings.ToLower(role))
	h.roleRes.mu.Unlock()
}

// liveAgentsOn counts the board's subscribers whose session is live.
func (h *SessionsHandler) liveAgentsOn(ctx context.Context, boardID string, live map[string]bool) int {
	subs, err := h.bs.ListSubscribers(ctx, boardID)
	if err != nil {
		return 0
	}
	n := 0
	for _, s := range subs {
		if s.SessionName != "" && live[s.SessionName] {
			n++
		}
	}
	return n
}

// launchedName is the display name launchSession gave the agent (numbered when its role was
// taken on the board, #3543), else the one asked for.
func launchedName(result map[string]any, asked string) string {
	if n, _ := result["display_name"].(string); n != "" {
		return n
	}
	return asked
}
