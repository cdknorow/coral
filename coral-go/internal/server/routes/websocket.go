package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/go-chi/chi/v5"

	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/httputil"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/safego"
	"github.com/subgentic/coral/internal/terminalinput"
)

// composerSendsPerSocket bounds the composer_send deliveries under way on one terminal socket (#4587, Security):
// each can wait seconds, and a flood of frames would otherwise start a goroutine each.
const composerSendsPerSocket = 4

// wsSlot counts the open sockets of one kind. The caps are generous (a dashboard tab or a hub viewer is one socket) and bound what a
// credentialed client that opens sockets without end can hold: a goroutine group, a subscription and buffers each (#5011). A var for the tests.
type wsSlot struct{ open atomic.Int32 }

var (
	wsCoralMax    int32 = 512
	wsTerminalMax int32 = 256
	wsCoralSlot   wsSlot
	wsTermSlot    wsSlot
)

// terminalFrameMax is the largest frame a terminal socket reads from its client (keystrokes, a resize, a composer message): the
// websocket library's own default, now written down; a bigger frame closes the connection (status 1009).
const terminalFrameMax = 32 << 10

// take reserves a place, or reports false when max sockets are open; release gives it back.
func (c *wsSlot) take(max int32) (release func(), ok bool) {
	if c.open.Add(1) > max {
		c.open.Add(-1)
		return nil, false
	}
	return func() { c.open.Add(-1) }, true
}

// wsTooMany answers an upgrade refused for the cap, before the socket exists (the dashboard backs off and reconnects).
func wsTooMany(w http.ResponseWriter, what string) {
	httputil.WriteActionLimitBody(w, 5, map[string]any{"error": "too many " + what + " connections are open", "code": "ws_limit"})
}

// wsAcceptOptions returns WebSocket accept options with origin validation: only
// this server's own page (the Origin's host is the request's Host). Other ports of
// localhost are not allowed: a page there could open /ws/terminal and type into an
// agent (#3511). The websocket library (github.com/coder/websocket) allows same-origin by itself; the
// request host is added for remote access by IP or hostname.
func (h *SessionsHandler) wsAcceptOptions(r *http.Request) *websocket.AcceptOptions {
	var patterns []string
	if host := r.Host; host != "" {
		patterns = append(patterns, host)
	}
	return &websocket.AcceptOptions{
		OriginPatterns: patterns,
	}
}

// ── /ws/coral — Diff-based session list streaming ────────────────────

// WSCoral streams the coral-wide session list via WebSocket.
//
// First message is a full "coral_update" with all sessions.
// Subsequent messages are "coral_diff" with only changed/removed sessions
// to reduce bandwidth. By default a changed agent is sent as its full row. A local
// dashboard may ask for field-level diffs with ?diff=fields (#3281): a changed row it
// already has then carries only its identity, the fields that changed, and an explicit
// null for each field that went away, marked "_partial": true. A hub's feed (?feed=hub)
// always gets full rows.
func (h *SessionsHandler) WSCoral(w http.ResponseWriter, r *http.Request) {
	if debugEnabled() {
		slog.Info("[debug] ws/coral connection from", "remote", r.RemoteAddr, "origin", r.Header.Get("Origin"))
	}
	release, ok := wsCoralSlot.take(wsCoralMax)
	if !ok {
		wsTooMany(w, "dashboard")
		return
	}
	defer release()
	conn, err := websocket.Accept(w, r, h.wsAcceptOptions(r))
	if err != nil {
		slog.Debug("ws/coral accept failed", "error", err)
		return
	}
	remove, ok := wsOpen.add(conn)
	if !ok {
		conn.Close(websocket.StatusGoingAway, "Coral is stopping")
		return
	}
	defer remove()
	defer func() {
		if debugEnabled() {
			slog.Info("[debug] ws/coral disconnected", "remote", r.RemoteAddr)
		}
		conn.CloseNow()
	}()

	ctx := conn.CloseRead(r.Context())

	// Field-level diffs (#3281): opt-in, and never on a hub's machine feed.
	fieldDiff := r.URL.Query().Get("diff") == "fields" && r.URL.Query().Get("feed") != "hub"
	// Through Coral Hub, the frames' paths are in display form and log paths left out
	// (#3559); the rows are shared with other connections, so each frame is rewritten on a copy.
	forHub := func(msg any) any { return msg }
	if cloud.Tunneled(r) {
		forHub = hubFrame
	}

	// Per-connection state for diff calculation
	feed := &coralFeed{fieldDiff: fieldDiff, prevSessions: map[string]string{}, prevByKey: map[string]map[string]any{}, prevRunsJSON: "[]", prevHealth: map[string]string{}, firstMessage: true}
	// A hub's feed (?feed=hub) is a machine client, not a dashboard: it must
	// not receive, and so mark delivered, this server's UI notifications.
	notifications := h.notifications
	if r.URL.Query().Get("feed") == "hub" {
		notifications = nil
	}
	// The notifications are the Operator's too (a toast or an alert about an agent that needs them): a connection that reads one marks it
	// delivered, so an agent's token on this socket would both read them and keep the dashboard that opens next from ever seeing them
	// (#5011). An agent gets the feed (a read left open, see the route census) and no notifications.
	if _, agent := VerifiedSession(r.Context()); agent {
		notifications = nil
	}
	// backlog: notifications no dashboard had received when this one
	// connected; they go out with the first message.
	var notifCursor int64
	var backlog []Notification
	if notifications != nil {
		backlog, notifCursor = notifications.Connect()
	}

	pollInterval := time.Duration(h.cfg.WSPollIntervalS) * time.Second
	if h.cfg.WSPollInterval > 0 {
		pollInterval = h.cfg.WSPollInterval
	}
	if pollInterval == 0 {
		pollInterval = 5 * time.Second
	}
	// Remote feed changes rebuild at most once a second, or once a poll
	// interval when that is shorter (the ticker rebuilds that often anyway).
	// The limit counts only builds that carried a remote change: a kick or a
	// tick does not hold the next remote change back (#3142).
	remoteLimit := time.Second
	if h.cfg.WSRemoteDebounce > 0 {
		remoteLimit = h.cfg.WSRemoteDebounce // tests only
	}
	remoteDebounce := min(remoteLimit, pollInterval)
	after := h.remoteAfter
	if after == nil {
		after = time.After
	}

	// Every connection ticks on the same wall-clock boundaries (multiples of the interval), so
	// all the tabs, popouts and hub feeds ticking at once share one build of the list
	// (sharedWSTick reuses a build for wsListMaxAge). With their own tickers they ticked at
	// whatever phase they connected at, and each tick past wsListMaxAge built the list again
	// (#3833 item 6, #3846).
	ticker := time.NewTimer(untilTick(time.Now(), pollInterval))
	defer ticker.Stop()

	// Remote feed changes trigger an early rebuild (see remoteDebounce).
	var remoteChanged <-chan struct{}
	if h.remoteAgents != nil {
		ch, cancel := h.remoteAgents.Subscribe()
		defer cancel()
		remoteChanged = ch
	}
	var debounce <-chan time.Time
	// A kick (a state change on this server) rebuilds after wsKickCoalesce.
	kicked, cancelKick := h.wsKick.subscribe()
	defer cancelKick()
	var kickWait <-chan time.Time
	lastRemoteBuild := time.Time{} // last build that carried a remote change
	remotePending := false         // a remote change not yet in a build

	// The first coral_update goes out at once rather than after the first
	// tick, so a dashboard or hub feed is not empty for a poll interval. It
	// comes from the shared tick, so clients connecting together still share
	// one build.
	connected := make(chan struct{}, 1)
	connected <- struct{}{}

	for {
		select {
		case <-ctx.Done():
			conn.Close(websocket.StatusNormalClosure, "")
			return
		case <-connected:
		case <-ticker.C:
			ticker.Reset(untilTick(time.Now(), pollInterval))
		case <-remoteChanged:
			if feed.firstMessage {
				continue
			}
			remotePending = true
			if kickWait != nil {
				continue // rides the kick's build, due within wsKickCoalesce
			}
			if wait := remoteDebounce - time.Since(lastRemoteBuild); wait > 0 {
				if debounce == nil {
					debounce = after(wait)
				}
				continue
			}
		case <-debounce:
			debounce = nil
		case <-kicked:
			if feed.firstMessage {
				continue // the first coral_update is built fresh anyway
			}
			if kickWait == nil {
				kickWait = time.After(wsKickCoalesce)
			}
			continue
		case <-kickWait:
			kickWait = nil
		}
		if remotePending {
			// This build carries the remote change, whatever triggered it.
			lastRemoteBuild = time.Now()
			remotePending = false
			debounce = nil
		}

		tick, err := h.sharedWSTick(r)
		if err != nil {
			slog.Warn("ws/coral build session list failed", "error", err)
			continue
		}
		if owner, agent := VerifiedSession(r.Context()); agent {
			tick = scopeTickForAgent(tick, owner.SessionID) // as the HTTP list: other boards' rows keep the roster only (#5041)
		}
		// Notifications this connection has not seen yet (every dashboard
		// gets each one).
		var pending []Notification
		var nextCursor int64
		if notifications != nil {
			var fresh []Notification
			fresh, nextCursor = notifications.Since(notifCursor)
			pending = append(backlog, fresh...)
		}

		// The machine's health (#3444): in full in coral_update, then only the changed fields
		// (and null for one that went away, such as reason back at ok) in coral_diff.
		healthDelta, healthNow := h.machineHealthDelta(feed.prevHealth)

		msg, kind := feed.frame(tick, pending, healthDelta, healthNow, time.Now(), h.heartbeatInterval())
		if msg == nil {
			continue
		}
		var out any = msg
		if kind != coralHeartbeat {
			out = forHub(msg)
		}
		if err := wsWriteJSON(ctx, conn, out); err != nil {
			return
		}
		// Only a written update or diff counts its notifications as delivered and moves the cursor past them.
		if kind != coralHeartbeat && notifications != nil {
			notifications.Ack(pending)
			backlog, notifCursor = nil, nextCursor
		}
		feed.sent(kind, tick, healthDelta, healthNow, time.Now())
	}
}

// coralFeed is one /ws/coral connection's view of what it was last sent, for the diffs.
type coralFeed struct {
	fieldDiff       bool                      // field-level diffs (#3281)
	prevSessions    map[string]string         // session key -> json string
	prevByKey       map[string]map[string]any // session key -> the row last sent (read-only tick data)
	prevRunsJSON    string
	prevHealth      map[string]string // machine_health: each top-level field as last sent (#3444)
	prevServersJSON string
	firstMessage    bool
	lastSent        time.Time // last message written to this connection
}

// coralFrame is the kind of frame coralFeed.frame built.
type coralFrame int

const (
	coralUpdate coralFrame = iota
	coralDiff
	coralHeartbeat
)

// frame is what the connection is sent for tick (shared with every connection: read, never modified): the first
// coral_update in full; then a coral_diff of the rows changed (whole, or field by field) or gone, the servers, the
// runs, the machine health's changed fields and the pending notifications; a coral_heartbeat after heartbeat of
// silence; or nil for nothing. sent records it once written.
func (f *coralFeed) frame(tick *wsTick, pending []Notification, healthDelta map[string]any, healthNow map[string]string, now time.Time, heartbeat time.Duration) (map[string]any, coralFrame) {
	if f.firstMessage {
		msg := map[string]any{
			"type":        "coral_update",
			"sessions":    tick.sessions,
			"active_runs": tick.activeRuns,
		}
		if healthNow != nil {
			msg["machine_health"] = healthDelta
		}
		if len(pending) > 0 {
			msg["notifications"] = pending
		}
		if tick.hasRemotes {
			msg["servers"] = tick.servers
		}
		return msg, coralUpdate
	}

	// Calculate diff
	var changed []map[string]any
	for key, sJSON := range tick.current {
		if f.prevSessions[key] != sJSON {
			if prev, ok := f.prevByKey[key]; ok && f.fieldDiff {
				changed = append(changed, fieldDelta(prev, tick.byKey[key]))
			} else {
				changed = append(changed, tick.byKey[key])
			}
		}
	}

	var removed []string
	var removedRemote []map[string]string
	for key := range f.prevSessions {
		if _, exists := tick.current[key]; !exists {
			if srv, k, ok := splitRemoteKey(key); ok {
				removedRemote = append(removedRemote, map[string]string{"server": srv, "key": k})
			} else {
				removed = append(removed, key)
			}
		}
	}
	serversChanged := tick.serversJSON != f.prevServersJSON
	runsChanged := tick.runsJSON != f.prevRunsJSON
	hasNotifications := len(pending) > 0
	healthChanged := len(healthDelta) > 0
	if len(changed) > 0 || len(removed) > 0 || len(removedRemote) > 0 || serversChanged || runsChanged || hasNotifications || healthChanged {
		payload := map[string]any{"type": "coral_diff"}
		if healthChanged {
			payload["machine_health"] = healthDelta
		}
		if len(changed) > 0 {
			payload["changed"] = changed
		}
		if len(removed) > 0 {
			payload["removed"] = removed
		}
		if len(removedRemote) > 0 {
			payload["removed_remote"] = removedRemote
		}
		if tick.hasRemotes && serversChanged {
			payload["servers"] = tick.servers
		}
		if runsChanged {
			payload["active_runs"] = tick.activeRuns
		}
		if hasNotifications {
			payload["notifications"] = pending
		}
		return payload, coralDiff
	}
	if now.Sub(f.lastSent) >= heartbeat {
		// Nothing changed for a while. Say so, so a hub holding this feed
		// can tell an idle server from one whose tick loop is stuck (it
		// falls back to polling the list when the feed goes quiet).
		// Dashboards ignore the type.
		return map[string]any{"type": "coral_heartbeat"}, coralHeartbeat
	}
	return nil, 0
}

// sent records a frame of kind built from tick as written at now: an update or a diff is now what the connection
// has (the machine health only when it was sent: in full, or changed); a heartbeat only resets the silence.
func (f *coralFeed) sent(kind coralFrame, tick *wsTick, healthDelta map[string]any, healthNow map[string]string, now time.Time) {
	f.lastSent = now
	if kind == coralHeartbeat {
		return
	}
	switch {
	case kind == coralUpdate && healthNow != nil:
		f.prevHealth = healthNow
	case kind == coralDiff && len(healthDelta) > 0:
		f.prevHealth = healthNow
	}
	f.prevServersJSON = tick.serversJSON
	f.prevSessions = tick.current
	f.prevByKey = tick.byKey
	f.prevRunsJSON = tick.runsJSON
	f.firstMessage = false
}

// wsHeartbeatInterval is how long /ws/coral may stay silent before it sends a
// coral_heartbeat. It is half the hub's default FeedQuietRefresh (30s).
const wsHeartbeatInterval = 15 * time.Second

// heartbeatInterval is wsHeartbeatInterval, or the handler's own (tests; a field,
// not the package value, so a test changing it can't race another test's handler
// still running: #3474).
func (h *SessionsHandler) heartbeatInterval() time.Duration {
	if d := time.Duration(h.wsHeartbeat.Load()); d > 0 {
		return d
	}
	return wsHeartbeatInterval
}

// buildSessionListForWS is the /ws/coral feed's session list: the live list's rows (liveSessionRows), every one
// enriched now.
func (h *SessionsHandler) buildSessionListForWS(r *http.Request) ([]map[string]any, error) {
	trace := newPhaseTrace("ws-list")
	defer trace.finish()
	rows, err := h.liveSessionRows(r, trace, true)
	if err == nil {
		h.orderLiveRows(r.Context(), rows)
	}
	return rows, err
}

// getActiveRuns fetches active job runs for the Jobs sidebar.
func (h *SessionsHandler) getActiveRuns(ctx context.Context) []map[string]any {
	if h.schedStore == nil {
		return []map[string]any{}
	}
	runs, err := h.schedStore.ListActiveRuns(ctx)
	if err != nil {
		return []map[string]any{}
	}
	result := make([]map[string]any, 0, len(runs))
	for _, r := range runs {
		entry := map[string]any{
			"id":           r.ID,
			"job_id":       r.JobID,
			"status":       r.Status,
			"scheduled_at": r.ScheduledAt,
			"created_at":   r.CreatedAt,
		}
		if r.JobName != nil {
			entry["job_name"] = *r.JobName
		}
		if r.SessionID != nil {
			entry["session_id"] = *r.SessionID
		}
		if r.StartedAt != nil {
			entry["started_at"] = *r.StartedAt
		}
		if r.DisplayName != nil {
			entry["display_name"] = *r.DisplayName
		}
		result = append(result, entry)
	}
	return result
}

// ── /ws/terminal/{name} — Bidirectional terminal WebSocket ──────────

// WSTerminal provides bidirectional terminal WebSocket with unified streaming.
// Both PTY and tmux backends use the same code path: Attach for live output,
// Replay for initial seed. Stream data is sent as binary WebSocket frames;
// control messages (terminal_closed) remain JSON text frames.
func (h *SessionsHandler) WSTerminal(w http.ResponseWriter, r *http.Request) {
	name, agentType, sessionID, admitted := h.wsTerminalAdmit(w, r)
	if !admitted {
		return
	}

	if debugEnabled() {
		slog.Info("[debug] ws/terminal connect", "name", name, "remote", r.RemoteAddr)
	}

	release, ok := wsTermSlot.take(wsTerminalMax)
	if !ok {
		wsTooMany(w, "terminal")
		return
	}
	defer release()
	conn, err := websocket.Accept(w, r, h.wsAcceptOptions(r))
	if err != nil {
		slog.Debug("ws/terminal accept failed", "error", err)
		return
	}
	remove, ok := wsOpen.add(conn)
	if !ok {
		conn.Close(websocket.StatusGoingAway, "Coral is stopping")
		return
	}
	defer remove()
	conn.SetReadLimit(terminalFrameMax)
	defer func() {
		if debugEnabled() {
			slog.Info("[debug] ws/terminal disconnected", "name", name)
		}
		conn.CloseNow()
	}()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	if h.backend == nil {
		conn.Close(websocket.StatusInternalError, "no backend")
		return
	}

	subID := fmt.Sprintf("ws-%d", time.Now().UnixNano())
	ch, err := h.backend.Attach(name, subID)
	if err != nil {
		if debugEnabled() {
			slog.Info("[debug] ws/terminal attach failed", "name", name, "error", err)
		}
		// The terminal is already gone: say so as when it ends while open, so the client
		// shows the session ended instead of retrying a broken connection (#3276).
		if errors.Is(err, ptymanager.ErrTerminalGone) {
			wsWriteJSON(ctx, conn, map[string]any{"type": "terminal_closed"})
			conn.Close(websocket.StatusNormalClosure, "session ended")
			return
		}
		conn.Close(websocket.StatusInternalError, "attach failed")
		return
	}

	defer h.backend.Unsubscribe(name, subID)

	t := &terminalStream{h: h, ctx: ctx, conn: conn, name: name, agentType: agentType, sessionID: sessionID, viaHub: cloud.Tunneled(r), subID: subID,
		sizeCheck: make(chan int64, 1)}
	t.sizer, _ = h.backend.(ptymanager.PaneSizer)
	t.sendReplay()

	// This server answers every terminal_input that carries an id (#3276): the client then
	// settles a send on the answer instead of on a quiet window. Sent after the replay seed,
	// which clients expect first.
	wsWriteJSON(ctx, conn, map[string]any{"type": "terminal_hello", "input_acks": true})

	t.sendSize(0, true)
	var sizeTick <-chan time.Time
	if t.sizer != nil {
		tk := time.NewTicker(terminalSizeEvery)
		defer tk.Stop()
		sizeTick = tk.C
	}

	safego.Go("server/routes/websocket:787", func() { t.readFrames(cancel) })
	t.writeLoop(ch, sizeTick)
}

// wsTerminalAdmit checks a terminal stream request before its socket is kept: never an agent's session (the
// stream is the Operator's, #4512, Security's #4508 H2), never a bare name (it could attach to and type into another
// agent's terminal, #3211), and an exact target that is live. A refused request is answered here.
func (h *SessionsHandler) wsTerminalAdmit(w http.ResponseWriter, r *http.Request) (name, agentType, sessionID string, ok bool) {
	// The terminal stream is the Operator's (#4512, Security's #4508 H2): an agent's session token reads and types
	// into no terminal through it, whatever the identity middleware did before. No agent tool opens it.
	if _, agent := VerifiedSession(r.Context()); agent {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden_for_agent",
			"message": "An agent can't read or type into a terminal through the terminal stream: that is for the Operator (the dashboard)"})
		return "", "", "", false
	}
	name = chi.URLParam(r, "name")
	agentType = r.URL.Query().Get("agent_type")
	sessionID = r.URL.Query().Get("session_id")
	if namesNoSession(name, sessionID) { // a bare name could attach to (and type into) another agent's terminal (#3211)
		if conn, err := websocket.Accept(w, r, h.wsAcceptOptions(r)); err == nil {
			conn.Close(websocket.StatusPolicyViolation, "session_id is required")
		}
		return "", "", "", false
	}
	if shouldValidateExactTarget(agentType, sessionID) {
		if _, status, message := h.validateExactLiveTarget(r.Context(), name, agentType, sessionID, true); status != 0 {
			conn, err := websocket.Accept(w, r, h.wsAcceptOptions(r))
			if err == nil {
				conn.Close(websocket.StatusPolicyViolation, message)
			}
			return "", "", "", false
		}
	}
	return name, agentType, sessionID, true
}

// terminalStream is one open terminal stream: the socket, the terminal it shows, and the pane size and the
// deliveries under way on it.
type terminalStream struct {
	h                          *SessionsHandler
	ctx                        context.Context
	conn                       *websocket.Conn
	name, agentType, sessionID string
	viaHub                     bool // the socket came through Coral Hub
	subID                      string
	sizer                      ptymanager.PaneSizer
	// sizeCheck carries a resize's seq to the writer, which answers it with the pane's size.
	sizeCheck          chan int64
	lastCols, lastRows int
	composing          atomic.Int32 // composer_send deliveries under way on this socket
	resyncs            resyncBudget
}

// sendReplay sends the replay seed as a binary frame, prefixed with clear codes so
// reconnects don't stack duplicate scrollback. Also sent again to a viewer that fell
// behind (#4161); false when the write failed.
func (t *terminalStream) sendReplay() bool {
	replay, err := t.h.backend.Replay(t.name)
	if err != nil || len(replay) == 0 {
		return true
	}
	clearPrefix := []byte("\x1b[2J\x1b[3J\x1b[H")
	seed := make([]byte, len(clearPrefix)+len(replay))
	copy(seed, clearPrefix)
	copy(seed[len(clearPrefix):], replay)
	return wsWrite(t.ctx, t.conn, websocket.MessageBinary, seed) == nil
}

// sendSize sends the pane's real size (#3422): at the start and whenever it changes, whoever changed it
// (another dashboard, a phone, a tmux attach). A viewer that does not set the size
// renders at this one instead of wrapping the pane's lines at its own width.
// The first report is marked initial: it is written before any frame from the client is read,
// so it never answers the client's own resize. A resize that carries a seq is answered with
// the size the pane has after it (tmux may clamp), echoing the seq, so the client can tell an
// answer to its latest resize from a change made elsewhere.
func (t *terminalStream) sendSize(seq int64, initial bool) {
	if t.sizer == nil {
		return
	}
	cols, rows, err := t.sizer.PaneSize(t.name)
	if err != nil || (seq == 0 && cols == t.lastCols && rows == t.lastRows) {
		return
	}
	t.lastCols, t.lastRows = cols, rows
	frame := map[string]any{"type": "terminal_size", "cols": cols, "rows": rows}
	if seq > 0 {
		frame["seq"] = seq
	}
	if initial {
		frame["initial"] = true
	}
	wsWriteJSON(t.ctx, t.conn, frame)
}

// terminalFrame is a frame from the client (JSON text).
type terminalFrame struct {
	Type string `json:"type"`
	Data string `json:"data"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
	// Seq, on terminal_resize, is echoed on the terminal_size that answers it (#3422).
	Seq int64 `json:"seq"`
	// ID, on terminal_input, asks for an answer carrying it (#3276): a
	// terminal_input_ack once the input is delivered, or the refusal with the id.
	// Decoded leniently: a frame whose id is not an integer (or is null) is
	// still typed, as before ids existed, and gets no answer.
	RawID json.RawMessage `json:"id"`
	ID    *int64          `json:"-"`
}

// parseTerminalFrame decodes a client frame; ok is false for one that isn't JSON (it is skipped).
func parseTerminalFrame(raw []byte) (msg terminalFrame, ok bool) {
	if err := json.Unmarshal(raw, &msg); err != nil {
		return msg, false
	}
	if len(msg.RawID) > 0 && string(bytes.TrimSpace(msg.RawID)) != "null" {
		var id int64
		if json.Unmarshal(msg.RawID, &id) == nil {
			msg.ID = &id
		}
	}
	return msg, true
}

// readFrames receives the client's frames until the socket closes, then cancels the stream.
func (t *terminalStream) readFrames(cancel context.CancelFunc) {
	defer cancel()
	for {
		_, raw, err := t.conn.Read(t.ctx)
		if err != nil {
			return
		}
		if msg, ok := parseTerminalFrame(raw); ok {
			t.handleFrame(msg)
		}
	}
}

// handleFrame types input, resizes the pane, or delivers a composer message, and answers as terminalReply says.
func (t *terminalStream) handleFrame(msg terminalFrame) {
	// Input or a resize for a terminal that is gone is not dropped silently: the
	// client is told, like the 409 terminal_gone of POST /send and /keys (#3218).
	var sendErr error
	switch msg.Type {
	case "composer_send":
		t.composerSend(msg)
		return
	case "terminal_input":
		if msg.Data != "" {
			// A person's keystrokes: the PTY backend records them for the gate
			// (no automated text joins a draft it can't read, #3482).
			if hs, ok := t.h.backend.(ptymanager.HumanInputSender); ok {
				sendErr = hs.SendHumanInput(t.name, []byte(msg.Data))
			} else {
				sendErr = t.h.backend.SendInput(terminalinput.Person, t.name, []byte(msg.Data)) // still a person's keystrokes
			}
		}
	case "terminal_resize":
		if msg.Cols >= minTerminalCols {
			cols, rows := clampTerminalSize(msg.Cols, msg.Rows)
			sendErr = t.h.backend.Resize(t.name, cols, rows)
			// Answer with the size the pane now has (it may differ: tmux clamps). Only
			// the latest resize needs its answer: an unread earlier one is replaced.
			select {
			case <-t.sizeCheck:
			default:
			}
			t.sizeCheck <- msg.Seq
		}
	}
	if reply := terminalReply(msg.Type, msg.ID, sendErr); reply != nil {
		wsWriteJSON(t.ctx, t.conn, reply)
	}
}

// terminalReply is the answer to a terminal_input or terminal_resize frame, or nil for none: terminal_gone for a
// terminal that is gone (not_sent for input), and for input that carries an id its ack or terminal_input_failed,
// with the id. Resizes and input without an id get no other answer (older clients).
func terminalReply(msgType string, id *int64, sendErr error) map[string]any {
	withID := func(frame map[string]any) map[string]any {
		if msgType == "terminal_input" && id != nil {
			frame["id"] = *id
		}
		return frame
	}
	switch {
	case errors.Is(sendErr, ptymanager.ErrTerminalGone):
		return withID(map[string]any{"type": "terminal_gone", "code": "terminal_gone", "error": terminalGoneMessage, "not_sent": msgType == "terminal_input"})
	case msgType != "terminal_input" || id == nil:
		// Resizes and input without an id get no answer (older clients).
		return nil
	case sendErr != nil:
		return withID(map[string]any{"type": "terminal_input_failed", "error": "The input could not be sent to the agent's terminal.", "not_sent": true})
	}
	return withID(map[string]any{"type": "terminal_input_ack"})
}

// composerSend delivers a message from Chat's composer (#4587): typed and submitted with deliverHumanSend's care (it
// waits for a starting agent's input box and confirms the text left it), and answered by id only
// once it was submitted. In its own goroutine: it can wait seconds, and the terminal's own
// keystrokes and resizes go on meanwhile.
// At most composerSendsPerSocket of them under way per socket: over that, refused at once.
func (t *terminalStream) composerSend(msg terminalFrame) {
	if msg.Data == "" || msg.ID == nil {
		return
	}
	id := *msg.ID
	if t.composing.Load() >= composerSendsPerSocket {
		_, hs, _ := humanSendError(errSendBusy)
		wsWriteJSON(t.ctx, t.conn, map[string]any{"type": "terminal_input_failed", "error": hs["error"], "code": hs["code"], "not_sent": true, "id": id})
		return
	}
	t.composing.Add(1)
	go func() {
		defer safego.Recover("server/routes/websocket:composer_send")
		defer t.composing.Add(-1)
		// A message held behind the agent's box (#6610) is answered held now, and acked or refused by this id when it is typed.
		reply := func(err error) { t.composerAnswer(id, err) }
		heldID, err := t.h.deliverPersonSend(t.ctx, t.name, t.agentType, t.sessionID, msg.Data, t.viaHub, reply)
		if heldID != "" {
			wsWriteJSON(t.ctx, t.conn, map[string]any{"type": "terminal_input_held", "id": id, "held_id": heldID, "note": heldNote})
			return
		}
		t.composerAnswer(id, err)
	}()
}

// composerAnswer tells the composer how its message ended: delivered, the terminal gone, or refused.
func (t *terminalStream) composerAnswer(id any, err error) {
	switch {
	case err == nil:
		wsWriteJSON(t.ctx, t.conn, map[string]any{"type": "terminal_input_ack", "id": id})
	case errors.Is(err, ptymanager.ErrTerminalGone):
		wsWriteJSON(t.ctx, t.conn, map[string]any{"type": "terminal_gone", "code": "terminal_gone", "error": terminalGoneMessage, "not_sent": true, "id": id})
	default:
		out := map[string]any{"type": "terminal_input_failed", "error": "The message could not be sent to the agent's terminal.", "not_sent": true, "id": id}
		if _, hs, ok := humanSendError(err); ok {
			out["error"], out["code"], out["not_sent"] = hs["error"], hs["code"], hs["not_sent"]
		}
		wsWriteJSON(t.ctx, t.conn, out)
	}
}

// writeLoop forwards the terminal's live output as binary frames, answers resizes with the pane's size, and ends the
// stream when the socket or the terminal does.
func (t *terminalStream) writeLoop(ch <-chan []byte, sizeTick <-chan time.Time) {
	for {
		select {
		case <-t.ctx.Done():
			t.conn.Close(websocket.StatusNormalClosure, "")
			return
		case seq := <-t.sizeCheck:
			t.sendSize(seq, false)
		case <-sizeTick:
			t.sendSize(0, false)
		case data, ok := <-ch:
			if !ok {
				next, keepGoing := t.outputEnded()
				if !keepGoing {
					return
				}
				ch = next
				continue
			}
			if err := wsWrite(t.ctx, t.conn, websocket.MessageBinary, data); err != nil {
				return
			}
		}
	}
}

// outputEnded handles the terminal's output channel closing: a viewer that fell a whole buffer behind (#4161) starts
// again from a fresh replay rather than show the output with frames missing (one that keeps falling behind is closed;
// the dashboard reconnects after a pause); otherwise the session ended. keepGoing is true with the new channel.
func (t *terminalStream) outputEnded() (next <-chan []byte, keepGoing bool) {
	if lr, isLR := t.h.backend.(ptymanager.LagReporter); isLR && lr.Lagged(t.name, t.subID) {
		if !t.resyncs.allow(time.Now()) {
			t.conn.Close(websocket.StatusTryAgainLater, "the terminal's output is faster than this connection")
			return nil, false
		}
		next, err := t.h.backend.Attach(t.name, t.subID)
		if err != nil {
			wsWriteJSON(t.ctx, t.conn, map[string]any{"type": "terminal_closed"})
			t.conn.Close(websocket.StatusNormalClosure, "session ended")
			return nil, false
		}
		if !t.sendReplay() {
			return nil, false
		}
		return next, true
	}
	wsWriteJSON(t.ctx, t.conn, map[string]any{"type": "terminal_closed"})
	t.conn.Close(websocket.StatusNormalClosure, "session ended")
	return nil, false
}

// wsWriteTimeout bounds every write to a dashboard socket (#4161). A client that stops reading
// (a tab in the background, a stalled link or Coral Hub stream) blocks a write once the
// connection's buffers are full; after this long the socket is closed, which frees its
// goroutines, its terminal subscription and its Coral Hub stream. The dashboard reconnects
// when it reads again and starts from a fresh replay.
var wsWriteTimeout = 15 * time.Second

func wsWrite(ctx context.Context, conn *websocket.Conn, typ websocket.MessageType, data []byte) error {
	ctx, cancel := context.WithTimeout(ctx, wsWriteTimeout)
	defer cancel()
	return conn.Write(ctx, typ, data)
}

func wsWriteJSON(ctx context.Context, conn *websocket.Conn, v any) error {
	ctx, cancel := context.WithTimeout(ctx, wsWriteTimeout)
	defer cancel()
	return wsjson.Write(ctx, conn, v)
}

// resyncBudget limits how often a terminal socket starts again from a replay because its viewer
// fell behind (#4161): resyncLimit times in resyncWindow, then the socket is closed and the
// dashboard's reconnect pause slows it down. A replay is larger than what was missed, so a
// viewer too slow for the output would otherwise be sent replays back to back.
type resyncBudget struct{ times []time.Time }

const (
	resyncLimit  = 3
	resyncWindow = time.Minute
)

func (b *resyncBudget) allow(now time.Time) bool {
	kept := b.times[:0]
	for _, t := range b.times {
		if now.Sub(t) < resyncWindow {
			kept = append(kept, t)
		}
	}
	b.times = kept
	if len(b.times) >= resyncLimit {
		return false
	}
	b.times = append(b.times, now)
	return true
}

// terminalSizeEvery is how often an open terminal socket checks the pane's size for changes
// made elsewhere (#3422); a resize sent on the socket itself is answered at once.
const terminalSizeEvery = time.Second

const (
	// notStartedThresholdSeconds is how long an agent may produce nothing before
	// we tell the user to look at its terminal. Long enough that a slow start is
	// not mistaken for a stall; short enough that a new user has not given up.
	notStartedThresholdSeconds = 25

	// notStartedWindowSeconds bounds how long after launch this is reported, so
	// a long-lived agent that is simply unused does not carry the label forever.
	notStartedWindowSeconds = 15 * 60
)

// agentNeverStarted reports that an agent has written terminal output, has
// never emitted a single agent event, and has been silent since — within a
// window after launch.
//
// What it does NOT claim is that the agent is blocked. Measured against a real
// agent: it is true while the agent sits on Claude Code's trust-folder prompt,
// and equally true once that prompt is answered and the agent waits idle at its
// own REPL having been given nothing to do. From outside the CLI those two are
// indistinguishable — both are a live process waiting on stdin — and pretending
// otherwise would mean matching one CLI's prompt wording, which breaks the
// first time that wording changes and covers only the CLI we encoded.
//
// So this reports the union, which is a true statement about both: this agent
// has not done anything yet, and its terminal is where the answer is. It stops
// the moment the agent emits its first event, and after the window regardless.
//
// Terminal sessions are excluded: they are a shell, they never emit agent
// events, and sitting at a prompt is what they are for.
func agentNeverStarted(agentType, latestEvent string, stalenessSeconds, sessionAgeSeconds float64) bool {
	if agentType == at.Terminal {
		return false
	}
	if latestEvent != "" {
		return false // it started; whatever it is doing now, it is not this
	}
	if sessionAgeSeconds <= 0 || sessionAgeSeconds > notStartedWindowSeconds {
		return false
	}
	return stalenessSeconds >= notStartedThresholdSeconds
}

// agentStarting reports that an agent's CLI has not emitted its first event yet (#6381): a signal, the absence of the first hook event, and not a timer, so the composer can say
// "Starting" from the status and not from a clock. It is bounded by the same window as agentNeverStarted so an agent whose hooks never report is not "starting" forever, and it is
// never true for a terminal, a sleeping or an archived session.
func agentStarting(agentType, latestEvent string, sessionAgeSeconds float64, sleeping, archived bool) bool {
	if agentType == at.Terminal || sleeping || archived || latestEvent != "" {
		return false
	}
	return sessionAgeSeconds > 0 && sessionAgeSeconds <= notStartedWindowSeconds
}

// sessionAgeSeconds returns how long ago a session was created, or 0 when the
// timestamp is missing or unparseable.
func sessionAgeSeconds(createdAt string) float64 {
	if createdAt == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return 0
	}
	age := time.Since(t).Seconds()
	if age < 0 {
		return 0
	}
	return age
}

// feedIdentityFields name a feed row (the dashboard keys rows by server and session_id,
// or name): a field-level diff always carries them.
var feedIdentityFields = []string{"server", "session_id", "name", "agent_type"}

// fieldDelta is a changed row as a field-level diff (#3281): its identity, every field
// whose value changed, and null for every field it no longer has, never omitted (an
// omitted field keeps its old value on the dashboard), marked "_partial" so a dashboard
// that lost the row knows it can't use it alone.
func fieldDelta(prev, curr map[string]any) map[string]any {
	out := map[string]any{"_partial": true}
	for _, k := range feedIdentityFields {
		if v, ok := curr[k]; ok {
			out[k] = v
		}
	}
	for k, v := range curr {
		if old, ok := prev[k]; !ok || !sameJSON(old, v) {
			out[k] = v
		}
	}
	for k := range prev {
		if _, ok := curr[k]; !ok {
			out[k] = nil
		}
	}
	return out
}

// sameJSON reports whether a and b encode to the same JSON (the feed's own notion of equal).
func sameJSON(a, b any) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(ja, jb)
}

// machineHealthDelta is the machine's health as top-level fields (JSON) and the fields that differ
// from prev, with null for a field that went away (#3444). Both are nil without a sample.
func (h *SessionsHandler) machineHealthDelta(prev map[string]string) (map[string]any, map[string]string) {
	if h.machineHealth == nil {
		return nil, nil
	}
	cur, ok := h.machineHealth.Current()
	if !ok {
		return nil, nil
	}
	b, err := json.Marshal(cur)
	if err != nil {
		return nil, nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(b, &fields) != nil {
		return nil, nil
	}
	now := make(map[string]string, len(fields))
	delta := map[string]any{}
	for k, v := range fields {
		now[k] = string(v)
		if prev[k] != string(v) {
			delta[k] = v
		}
	}
	for k := range prev {
		if _, still := now[k]; !still {
			delta[k] = nil
		}
	}
	return delta, now
}

// untilTick is how long from now to the next multiple of interval since the Unix epoch.
func untilTick(now time.Time, interval time.Duration) time.Duration {
	if interval <= 0 {
		return time.Second
	}
	d := interval - time.Duration(now.UnixNano()%int64(interval))
	if d < interval/100 {
		d += interval // too close to the boundary to tick twice in a row
	}
	return d
}
