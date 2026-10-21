package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/safego"
	"github.com/subgentic/coral/internal/store/storetest"
	"github.com/subgentic/coral/internal/terminalinput"
)

// WSTerminal, split into its admission, a per-connection stream (replay, size, frames, composer, output) and the
// pure frame parsing and reply (#4713), streams as wsTerminalBefore4713 (the handler at v1.4.92, below) does. Each
// seed is a backend (replay, attach errors, a pane size or none, lag, a person's keystrokes or plain input) and a
// script of client frames; both handlers run it on identical backends, and what the client receives (each step's
// frames, and how the socket closes) and what the backend was asked to do are compared. Each step ends with a probe
// (an empty input with an id, always acknowledged) so the transcript doesn't depend on timing; a step's frames are
// compared as a set (a size answer comes from the writer, an ack from the reader). Each outcome has a floor.

type wstBackend struct {
	mu        sync.Mutex
	replay    []byte
	attachErr error // the first attach's
	resyncErr error // a resync's attach's
	attaches  int
	ch        chan []byte
	sendErr   error
	resizeErr error
	cols      int
	rows      int
	sizeErr   bool
	lagged    bool
	log       []string
}

func (b *wstBackend) note(s string) { b.log = append(b.log, s) }

func (b *wstBackend) Spawn(string, string, string, string, string, uint16, uint16) error { return nil }
func (b *wstBackend) Kill(string) error                                                  { return nil }
func (b *wstBackend) Restart(string, string) error                                       { return nil }
func (b *wstBackend) ListSessions() []ptymanager.SessionInfo                             { return nil }
func (b *wstBackend) IsRunning(string) bool                                              { return true }
func (b *wstBackend) LogPath(string) string                                              { return "" }
func (b *wstBackend) Close() error                                                       { return nil }

func (b *wstBackend) SendInput(_ terminalinput.Origin, name string, data []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.note("input " + string(data))
	return b.sendErr
}

func (b *wstBackend) Resize(name string, cols, rows uint16) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.note(fmt.Sprintf("resize %dx%d", cols, rows))
	if b.resizeErr == nil {
		b.cols, b.rows = int(cols), int(rows)
	}
	return b.resizeErr
}

func (b *wstBackend) Attach(name, sub string) (<-chan []byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.attaches++
	b.note(fmt.Sprintf("attach %d", b.attaches))
	if b.attaches == 1 && b.attachErr != nil {
		return nil, b.attachErr
	}
	if b.attaches > 1 && b.resyncErr != nil {
		return nil, b.resyncErr
	}
	b.ch = make(chan []byte, 4)
	return b.ch, nil
}

func (b *wstBackend) Unsubscribe(string, string) {}

func (b *wstBackend) Replay(string) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.replay, nil
}

// wstSized adds the pane's size and the lag record.
type wstSized struct{ *wstBackend }

func (s wstSized) PaneSize(string) (int, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sizeErr {
		return 0, 0, errors.New("no size")
	}
	return s.cols, s.rows, nil
}

func (s wstSized) Lagged(string, string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.lagged
	s.lagged = false // once: the record is cleared
	return l
}

// wstHuman also takes a person's keystrokes on their own path.
type wstHuman struct{ wstSized }

func (h wstHuman) SendHumanInput(name string, data []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.note("human " + string(data))
	return h.sendErr
}

type wstStep struct {
	frame   string // what the client sends
	sendErr error
	resize  error
	resizes bool // a resize at or over the minimum: answered with the size when seq > 0 or it changed
	cols    int  // its size
	rows    int
	seq     int64 // its seq
	compose int64 // a composer_send's id (its answer is awaited)
}

type wstCase struct {
	replay    []byte
	attachErr error
	resyncErr error
	sizer     bool
	human     bool
	sizeErr   bool
	cols      int
	steps     []wstStep
	end       string // client, ended, lagged
	refuse    string // "", bare, exact, nobackend
}

var errWSTOther = errors.New("write failed")

func randomWSTCase(rng *rand.Rand) wstCase {
	c := wstCase{sizer: rng.Intn(4) > 0, cols: 80}
	c.human = c.sizer && rng.Intn(3) == 0
	c.sizeErr = c.sizer && rng.Intn(10) == 0
	if rng.Intn(3) > 0 {
		c.replay = []byte("earlier output\r\n")
	}
	switch rng.Intn(12) {
	case 0:
		c.attachErr = ptymanager.ErrTerminalGone
	case 1:
		c.attachErr = errWSTOther
	}
	switch rng.Intn(16) {
	case 0:
		c.refuse = "bare"
	case 1:
		c.refuse = "exact"
	case 2:
		c.refuse = "nobackend"
	}
	c.end = []string{"client", "ended", "lagged", "lagged"}[rng.Intn(4)]
	if c.end == "lagged" && rng.Intn(3) == 0 {
		c.resyncErr = ptymanager.ErrTerminalGone // the resync's attach fails
	}
	cols := c.cols
	for k := 2 + rng.Intn(8); k > 0; k-- {
		var st wstStep
		errs := []error{nil, nil, nil, ptymanager.ErrTerminalGone, errWSTOther}
		switch rng.Intn(8) {
		case 0, 1, 2:
			data := []string{"ls\r", "", "q"}[rng.Intn(3)]
			id := []string{`, "id": 7`, `, "id": 9`, `, "id": null`, `, "id": "7"`, ``}[rng.Intn(5)]
			st.frame = fmt.Sprintf(`{"type":"terminal_input","data":%q%s}`, data, id)
			st.sendErr = errs[rng.Intn(len(errs))]
		case 3, 4:
			nc := []int{minTerminalCols - 1, minTerminalCols, 100, 120, cols}[rng.Intn(5)]
			nr := []int{10, 24, 40}[rng.Intn(3)]
			st.seq = []int64{0, 0, 1, 5, 6}[rng.Intn(5)]
			id := []string{``, ``, `,"id":4`}[rng.Intn(3)] // an id on a resize is never echoed
			st.frame = fmt.Sprintf(`{"type":"terminal_resize","cols":%d,"rows":%d,"seq":%d%s}`, nc, nr, st.seq, id)
			st.resize = errs[rng.Intn(len(errs))]
			if nc >= minTerminalCols {
				st.resizes, st.cols, st.rows = true, nc, nr
			}
		case 5:
			st.compose = int64(100 + rng.Intn(50))
			st.frame = fmt.Sprintf(`{"type":"composer_send","data":"hello agent","id":%d}`, st.compose)
		case 6:
			st.frame = []string{`not json`, `{"type":"mystery"}`, `{"type":"composer_send","data":"","id":3}`}[rng.Intn(3)]
		case 7:
			st.frame = `{"type":"terminal_input","data":"x","id":12}`
			st.sendErr = errs[rng.Intn(len(errs))]
		}
		c.steps = append(c.steps, st)
	}
	return c
}

type wstServer struct {
	srv *httptest.Server
	h   *SessionsHandler
}

func newWSTServer(t *testing.T) *wstServer {
	db, err := storetest.Open(t, t.TempDir()+"/test.db")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	term := newMockTerminal()
	term.addSession("claude-1d2e3f40-4713-4000-8000-000000000001", "/tmp/project") // the composer's sends go through it
	h := NewSessionsHandler(db, &config.Config{WSPollIntervalS: 1, LogDir: t.TempDir()}, nil, term, nil)
	r := chi.NewRouter()
	r.Get("/old/ws/terminal/{name}", h.wsTerminalBefore4713)
	r.Get("/new/ws/terminal/{name}", h.WSTerminal)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return &wstServer{srv: srv, h: h}
}

func wstFrame(typ websocket.MessageType, data []byte) string {
	if typ == websocket.MessageBinary {
		return fmt.Sprintf("bin %q", data)
	}
	var m map[string]any
	if json.Unmarshal(data, &m) != nil {
		return "text " + string(data)
	}
	b, _ := json.Marshal(m) // sorted keys
	return "json " + string(b)
}

// wstRun runs c against one handler and describes everything the client saw and the backend was asked.
func wstRun(t *testing.T, s *wstServer, c wstCase, prefix string) string {
	b := &wstBackend{replay: c.replay, attachErr: c.attachErr, resyncErr: c.resyncErr, cols: c.cols, rows: 24, sizeErr: c.sizeErr}
	var backend ptymanager.TerminalBackend = b
	if c.sizer {
		backend = wstSized{b}
	}
	if c.human {
		backend = wstHuman{wstSized{b}}
	}
	if c.refuse == "nobackend" {
		backend = nil
	}
	s.h.backend = backend
	name := "claude-1d2e3f40-4713-4000-8000-000000000001"
	query := ""
	switch c.refuse {
	case "bare":
		name = "just-a-name"
	case "exact":
		query = "?agent_type=claude&session_id=1d2e3f40-4713-4000-8000-000000000001" // no live session: refused
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out strings.Builder
	conn, _, err := websocket.Dial(ctx, "ws"+s.srv.URL[4:]+"/"+prefix+"/ws/terminal/"+name+query, nil)
	if err != nil {
		return "dial failed: " + err.Error()
	}
	defer conn.CloseNow()
	closed := ""
	read := func() (string, bool) {
		rctx, rcancel := context.WithTimeout(ctx, 3*time.Second)
		defer rcancel()
		typ, data, err := conn.Read(rctx)
		if err != nil {
			closed = fmt.Sprintf("closed %d %q", websocket.CloseStatus(err), closeReason(err))
			return "", false
		}
		return wstFrame(typ, data), true
	}
	// The opening: up to the hello and, with a pane size, the first size report.
	var opening []string
	for {
		f, ok := read()
		if !ok {
			break
		}
		opening = append(opening, f)
		if strings.Contains(f, `"terminal_hello"`) {
			if c.sizer && !c.sizeErr {
				if f2, ok := read(); ok {
					opening = append(opening, f2)
				}
			}
			break
		}
	}
	fmt.Fprintf(&out, "opening %v\n", opening)
	lastCols, lastRows := c.cols, 24 // the size the stream last reported
	if closed == "" {
		for i, st := range c.steps {
			b.mu.Lock()
			b.sendErr, b.resizeErr = st.sendErr, st.resize
			b.mu.Unlock()
			if err := conn.Write(ctx, websocket.MessageText, []byte(st.frame)); err != nil {
				break
			}
			probe := int64(1000 + i) // an empty input: it reaches no backend, so the step's errors don't touch it
			if err := conn.Write(ctx, websocket.MessageText, []byte(fmt.Sprintf(`{"type":"terminal_input","data":"","id":%d}`, probe))); err != nil {
				break
			}
			wantSize := false
			if st.resizes && c.sizer && !c.sizeErr {
				cc, rr := clampTerminalSize(st.cols, st.rows)
				changed := st.resize == nil && (int(cc) != lastCols || int(rr) != lastRows)
				wantSize = st.seq > 0 || changed
				if wantSize || changed {
					if st.resize == nil {
						lastCols, lastRows = int(cc), int(rr)
					}
				}
			}
			var frames []string
			gotProbe, gotSize, gotCompose := false, !wantSize, st.compose == 0
			for !(gotProbe && gotSize && gotCompose) {
				f, ok := read()
				if !ok {
					break
				}
				frames = append(frames, f)
				switch {
				case strings.Contains(f, fmt.Sprintf(`"id":%d`, probe)):
					gotProbe = true
				case strings.Contains(f, `"terminal_size"`):
					gotSize = true
				case st.compose != 0 && strings.Contains(f, fmt.Sprintf(`"id":%d`, st.compose)):
					gotCompose = true
				}
			}
			sort.Strings(frames)
			fmt.Fprintf(&out, "step %d %s: %v\n", i, st.frame, frames)
			if closed != "" {
				break
			}
		}
	}
	if closed == "" {
		switch c.end {
		case "client":
			conn.Close(websocket.StatusNormalClosure, "bye")
			closed = "client closed"
		case "ended", "lagged":
			b.mu.Lock()
			b.lagged = c.end == "lagged"
			ch := b.ch
			b.mu.Unlock()
			close(ch)
			var tail []string
			if c.end == "lagged" && c.resyncErr == nil && c.sizer {
				// resynced (only a backend that reports lag): the replay again, if there is one, then the terminal
				// ends for good
				if len(c.replay) > 0 {
					if f, ok := read(); ok {
						tail = append(tail, f)
					}
				}
				for wait := 0; wait < 400; wait++ { // the resync's attach, before its channel is ended
					b.mu.Lock()
					n := b.attaches
					b.mu.Unlock()
					if n >= 2 {
						break
					}
					time.Sleep(5 * time.Millisecond)
				}
				b.mu.Lock()
				next := b.ch
				b.mu.Unlock()
				if next != ch { // a resync attached again: that terminal ends too
					close(next)
				}
			}
			for {
				f, ok := read()
				if !ok {
					break
				}
				tail = append(tail, f)
			}
			fmt.Fprintf(&out, "ending %v\n", tail)
		}
	}
	b.mu.Lock()
	fmt.Fprintf(&out, "%s\nbackend %v", closed, b.log)
	b.mu.Unlock()
	return out.String()
}

func closeReason(err error) string {
	var ce websocket.CloseError
	if errors.As(err, &ce) {
		return ce.Reason
	}
	return ""
}

func TestWSTerminalAgreesWithBefore4713(t *testing.T) {
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(nopLog{}, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	s := newWSTServer(t)
	kinds := map[string]int{}
	for seed := int64(1); seed <= 200; seed++ {
		rng := rand.New(rand.NewSource(seed))
		c := randomWSTCase(rng)
		began := time.Now()
		old := wstRun(t, s, c, "old")
		got := wstRun(t, s, c, "new")
		if d := time.Since(began); d > time.Second {
			t.Logf("seed %d took %s: %+v", seed, d, c)
		}
		require.Equal(t, old, got, "seed %d: %+v", seed, c)
		for _, k := range wstKinds(got) {
			kinds[k]++
		}
	}
	t.Logf("compared, per kind: %v", kinds)
	for _, k := range []string{"replay seed", "hello", "initial size", "ack", "input failed", "input gone", "resize gone",
		"size answer with seq", "size change", "composer ack", "a person's keystrokes", "plain input", "terminal closed",
		"resynced", "resync failed", "attach gone", "attach failed", "refused: bare name", "refused: exact target", "refused: no backend", "client closed"} {
		assert.GreaterOrEqual(t, kinds[k], 3, "%q went (almost) unexercised", k)
	}
}

func wstKinds(tr string) []string {
	var out []string
	add := func(on bool, k string) {
		if on {
			out = append(out, k)
		}
	}
	add(strings.Contains(tr, `bin "\x1b[2J`), "replay seed")
	add(strings.Contains(tr, `"terminal_hello"`), "hello")
	add(strings.Contains(tr, `"initial":true`), "initial size")
	add(strings.Count(tr, `"terminal_input_ack"`) > strings.Count(tr, "step "), "ack")
	add(strings.Contains(tr, `"The input could not be sent`), "input failed")
	add(strings.Contains(tr, `"not_sent":true,"type":"terminal_gone"`) || strings.Contains(tr, `"id":7,"not_sent":true,"type":"terminal_gone"`), "input gone")
	add(strings.Contains(tr, `"not_sent":false,"type":"terminal_gone"`), "resize gone")
	add(strings.Contains(tr, `"seq":`), "size answer with seq")
	add(strings.Contains(tr, `"type":"terminal_size"}`) && strings.Count(tr, `"terminal_size"`) > strings.Count(tr, `"initial":true`), "size change")
	add(strings.Contains(tr, `"id":1`) && regexpComposerAck(tr), "composer ack")
	add(strings.Contains(tr, "human "), "a person's keystrokes")
	add(strings.Contains(tr, "input ls") || strings.Contains(tr, "input q") || strings.Contains(tr, "input x"), "plain input")
	add(strings.Contains(tr, `"terminal_closed"`), "terminal closed")
	add(strings.Contains(tr, "attach 2"), "resynced")
	add(strings.Contains(tr, "attach 2") && strings.Count(tr, `"terminal_closed"`) == 1 && !strings.Contains(tr, `ending [bin`), "resync failed")
	add(strings.Contains(tr, `"terminal_closed"`) && strings.Contains(tr, "attach 1]") && !strings.Contains(tr, "step 0"), "attach gone")
	add(strings.Contains(tr, `"attach failed"`), "attach failed")
	add(strings.Contains(tr, `"session_id is required"`), "refused: bare name")
	add(strings.Contains(tr, "closed 1008") && !strings.Contains(tr, `"session_id is required"`), "refused: exact target")
	add(strings.Contains(tr, `"no backend"`), "refused: no backend")
	add(strings.Contains(tr, "client closed"), "client closed")
	return out
}

func regexpComposerAck(tr string) bool {
	for i := 100; i < 150; i++ {
		if strings.Contains(tr, fmt.Sprintf(`{"id":%d,"type":"terminal_input_ack"}`, i)) {
			return true
		}
	}
	return false
}

// wsTerminalBefore4713 is WSTerminal before #4713 split it (websocket.go at v1.4.92), kept to compare with.
func (h *SessionsHandler) wsTerminalBefore4713(w http.ResponseWriter, r *http.Request) {
	// The terminal stream is the Operator's (#4512, Security's #4508 H2): an agent's session token reads and types
	// into no terminal through it, whatever the identity middleware did before. No agent tool opens it.
	if _, agent := VerifiedSession(r.Context()); agent {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden_for_agent",
			"message": "An agent can't read or type into a terminal through the terminal stream: that is for the Operator (the dashboard)"})
		return
	}
	name := chi.URLParam(r, "name")
	agentType := r.URL.Query().Get("agent_type")
	sessionID := r.URL.Query().Get("session_id")
	if namesNoSession(name, sessionID) { // a bare name could attach to (and type into) another agent's terminal (#3211)
		if conn, err := websocket.Accept(w, r, h.wsAcceptOptions(r)); err == nil {
			conn.Close(websocket.StatusPolicyViolation, "session_id is required")
		}
		return
	}
	if shouldValidateExactTarget(agentType, sessionID) {
		if _, status, message := h.validateExactLiveTarget(r.Context(), name, agentType, sessionID, true); status != 0 {
			conn, err := websocket.Accept(w, r, h.wsAcceptOptions(r))
			if err == nil {
				conn.Close(websocket.StatusPolicyViolation, message)
			}
			return
		}
	}

	if debugEnabled() {
		slog.Info("[debug] ws/terminal connect", "name", name, "remote", r.RemoteAddr)
	}

	conn, err := websocket.Accept(w, r, h.wsAcceptOptions(r))
	if err != nil {
		slog.Debug("ws/terminal accept failed", "error", err)
		return
	}
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

	// Send replay seed as a binary frame, prefixed with clear codes so
	// reconnects don't stack duplicate scrollback. Also sent again to a viewer that fell
	// behind (#4161); false when the write failed.
	sendReplay := func() bool {
		replay, err := h.backend.Replay(name)
		if err != nil || len(replay) == 0 {
			return true
		}
		clearPrefix := []byte("\x1b[2J\x1b[3J\x1b[H")
		seed := make([]byte, len(clearPrefix)+len(replay))
		copy(seed, clearPrefix)
		copy(seed[len(clearPrefix):], replay)
		return wsWrite(ctx, conn, websocket.MessageBinary, seed) == nil
	}
	sendReplay()
	var resyncs resyncBudget

	// This server answers every terminal_input that carries an id (#3276): the client then
	// settles a send on the answer instead of on a quiet window. Sent after the replay seed,
	// which clients expect first.
	wsWriteJSON(ctx, conn, map[string]any{"type": "terminal_hello", "input_acks": true})

	// The pane's real size (#3422): sent now and whenever it changes, whoever changed it
	// (another dashboard, a phone, a tmux attach). A viewer that does not set the size
	// renders at this one instead of wrapping the pane's lines at its own width.
	// The first report is marked initial: it is written before any frame from the client is read,
	// so it never answers the client's own resize. A resize that carries a seq is answered with
	// the size the pane has after it (tmux may clamp), echoing the seq, so the client can tell an
	// answer to its latest resize from a change made elsewhere.
	sizer, _ := h.backend.(ptymanager.PaneSizer)
	sizeCheck := make(chan int64, 1)
	lastCols, lastRows := 0, 0
	sendSize := func(seq int64, initial bool) {
		if sizer == nil {
			return
		}
		cols, rows, err := sizer.PaneSize(name)
		if err != nil || (seq == 0 && cols == lastCols && rows == lastRows) {
			return
		}
		lastCols, lastRows = cols, rows
		frame := map[string]any{"type": "terminal_size", "cols": cols, "rows": rows}
		if seq > 0 {
			frame["seq"] = seq
		}
		if initial {
			frame["initial"] = true
		}
		wsWriteJSON(ctx, conn, frame)
	}
	sendSize(0, true)
	var sizeTick <-chan time.Time
	if sizer != nil {
		t := time.NewTicker(terminalSizeEvery)
		defer t.Stop()
		sizeTick = t.C
	}

	// Reader goroutine: receives terminal input from the client (JSON text frames)
	var composing atomic.Int32 // composer_send deliveries under way on this socket
	go func() {
		defer safego.Recover("server/routes/websocket:787")
		defer cancel()
		for {
			_, raw, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var msg struct {
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
			if err := json.Unmarshal(raw, &msg); err != nil {
				continue
			}
			if len(msg.RawID) > 0 && string(bytes.TrimSpace(msg.RawID)) != "null" {
				var id int64
				if json.Unmarshal(msg.RawID, &id) == nil {
					msg.ID = &id
				}
			}
			// Input or a resize for a terminal that is gone is not dropped silently: the
			// client is told, like the 409 terminal_gone of POST /send and /keys (#3218).
			var sendErr error
			switch msg.Type {
			case "composer_send":
				// A message from Chat's composer (#4587): typed and submitted with deliverHumanSend's care (it
				// waits for a starting agent's input box and confirms the text left it), and answered by id only
				// once it was submitted. In its own goroutine: it can wait seconds, and the terminal's own
				// keystrokes and resizes go on meanwhile.
				// At most composerSendsPerSocket of them under way per socket: over that, refused at once.
				if msg.Data != "" && msg.ID != nil {
					id := *msg.ID
					if composing.Load() >= composerSendsPerSocket {
						_, hs, _ := humanSendError(errSendBusy)
						wsWriteJSON(ctx, conn, map[string]any{"type": "terminal_input_failed", "error": hs["error"], "code": hs["code"], "not_sent": true, "id": id})
						continue
					}
					composing.Add(1)
					go func() {
						defer safego.Recover("server/routes/websocket:composer_send")
						defer composing.Add(-1)
						err := h.deliverHumanSend(ctx, name, agentType, sessionID, msg.Data)
						switch {
						case err == nil:
							wsWriteJSON(ctx, conn, map[string]any{"type": "terminal_input_ack", "id": id})
						case errors.Is(err, ptymanager.ErrTerminalGone):
							wsWriteJSON(ctx, conn, map[string]any{"type": "terminal_gone", "code": "terminal_gone", "error": terminalGoneMessage, "not_sent": true, "id": id})
						default:
							out := map[string]any{"type": "terminal_input_failed", "error": "The message could not be sent to the agent's terminal.", "not_sent": true, "id": id}
							if _, hs, ok := humanSendError(err); ok {
								out["error"], out["code"], out["not_sent"] = hs["error"], hs["code"], hs["not_sent"]
							}
							wsWriteJSON(ctx, conn, out)
						}
					}()
				}
				continue
			case "terminal_input":
				if msg.Data != "" {
					// A person's keystrokes: the PTY backend records them for the gate
					// (no automated text joins a draft it can't read, #3482).
					if hs, ok := h.backend.(ptymanager.HumanInputSender); ok {
						sendErr = hs.SendHumanInput(name, []byte(msg.Data))
					} else {
						sendErr = h.backend.SendInput(terminalinput.Unclassified, name, []byte(msg.Data))
					}
				}
			case "terminal_resize":
				if msg.Cols >= minTerminalCols {
					cols, rows := clampTerminalSize(msg.Cols, msg.Rows)
					sendErr = h.backend.Resize(name, cols, rows)
					// Answer with the size the pane now has (it may differ: tmux clamps). Only
					// the latest resize needs its answer: an unread earlier one is replaced.
					select {
					case <-sizeCheck:
					default:
					}
					sizeCheck <- msg.Seq
				}
			}
			withID := func(frame map[string]any) map[string]any {
				if msg.Type == "terminal_input" && msg.ID != nil {
					frame["id"] = *msg.ID
				}
				return frame
			}
			switch {
			case errors.Is(sendErr, ptymanager.ErrTerminalGone):
				wsWriteJSON(ctx, conn, withID(map[string]any{"type": "terminal_gone", "code": "terminal_gone", "error": terminalGoneMessage, "not_sent": msg.Type == "terminal_input"}))
			case msg.Type != "terminal_input" || msg.ID == nil:
				// Resizes and input without an id get no answer (older clients).
			case sendErr != nil:
				wsWriteJSON(ctx, conn, withID(map[string]any{"type": "terminal_input_failed", "error": "The input could not be sent to the agent's terminal.", "not_sent": true}))
			default:
				wsWriteJSON(ctx, conn, withID(map[string]any{"type": "terminal_input_ack"}))
			}
		}
	}()

	// Writer loop: forward live output as binary WebSocket frames
	for {
		select {
		case <-ctx.Done():
			conn.Close(websocket.StatusNormalClosure, "")
			return
		case seq := <-sizeCheck:
			sendSize(seq, false)
		case <-sizeTick:
			sendSize(0, false)
		case data, ok := <-ch:
			if !ok {
				if lr, isLR := h.backend.(ptymanager.LagReporter); isLR && lr.Lagged(name, subID) {
					// This viewer fell a whole buffer behind (#4161): start it again from a fresh
					// replay rather than show the output with frames missing. One that keeps
					// falling behind is closed; the dashboard reconnects after a pause.
					if !resyncs.allow(time.Now()) {
						conn.Close(websocket.StatusTryAgainLater, "the terminal's output is faster than this connection")
						return
					}
					next, err := h.backend.Attach(name, subID)
					if err != nil {
						wsWriteJSON(ctx, conn, map[string]any{"type": "terminal_closed"})
						conn.Close(websocket.StatusNormalClosure, "session ended")
						return
					}
					ch = next
					if !sendReplay() {
						return
					}
					continue
				}
				wsWriteJSON(ctx, conn, map[string]any{"type": "terminal_closed"})
				conn.Close(websocket.StatusNormalClosure, "session ended")
				return
			}
			if err := wsWrite(ctx, conn, websocket.MessageBinary, data); err != nil {
				return
			}
		}
	}
}
