package store

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/subgentic/coral/internal/querypage"
)

// The agent events query (#4745): GET /api/sessions/live/{name}/events with type, q, since, until, sort, limit and cursor, answering
// {events, total, next_cursor, prev_cursor} like the other searches. The events inside the type and time bounds are read once, in order, as
// their ids, times, types, tool names and summaries only (a detail can be large), tested in Go for q on what a viewer sees (EventQuery.Shown),
// and the page's full rows are read afterwards. q is a plain, case-insensitive substring of the summary or the tool name (not the detail):
// nothing the caller sends reaches a statement or a pattern. The scan is bounded by rows and by text.

const (
	EventQueryDefaultLimit = 50
	EventQueryMaxLimit     = 200
	EventQueryMaxTextLen   = 200
	eventQueryMaxTypes     = 20
)

var (
	eventScanRows  = 200000
	eventScanBytes = 64 << 20
)

var ErrEventQuery = errors.New("invalid events query")

func eventQueryErr(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrEventQuery, fmt.Sprintf(format, a...))
}

// eventTypeRe is what an event type looks like: a lower-case identifier (tool_use, permission_request, auto_accept ...).
var eventTypeRe = regexp.MustCompile(`^[a-z0-9_]{1,40}$`)

// EventQueryParams are the raw values of a request, one slice per parameter.
type EventQueryParams struct {
	Type, Q, Since, Until, Sort, Limit, Cursor []string
}

// EventQuery is a validated query for one agent's (or one session's) events. Build it with ParseEventQuery; the caller sets Shown for a
// request through Coral Hub (what a viewer sees of a text: the match is made on it; nil is the text as stored).
type EventQuery struct {
	AgentName string  // the events of this agent name; empty: any
	SessionID *string // and of this session
	Types     []string
	Text      string // lower-cased; empty: no search
	Since     string // created_at >=, in the stored layout
	Until     string // created_at <
	Oldest    bool
	Limit     int
	Shown     func(string) string
	cursor    *eventCursor
	finger    string
}

type eventCursor struct {
	Finger string `json:"f"`
	At     string `json:"a"`
	ID     int64  `json:"i"`
	Prev   bool   `json:"p,omitempty"`
}

func (c eventCursor) encode() string {
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeEventCursor(s string) (*eventCursor, error) {
	if len(s) > 512 {
		return nil, errors.New("long")
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	var c eventCursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	if c.ID <= 0 || c.Finger == "" || c.At == "" || len(c.At) > 64 {
		return nil, errors.New("bad")
	}
	return &c, nil
}

func eventSingle(name string, vals []string) (string, bool, error) {
	switch len(vals) {
	case 0:
		return "", false, nil
	case 1:
		return vals[0], true, nil
	}
	return "", false, eventQueryErr("%s is given more than once", name)
}

func eventTime(name string, vals []string) (string, error) {
	v, ok, err := eventSingle(name, vals)
	if err != nil || !ok {
		return "", err
	}
	t, perr := time.Parse(time.RFC3339, v)
	if perr != nil {
		return "", eventQueryErr("%s must be an RFC 3339 time, for example 2026-10-18T00:00:00Z", name)
	}
	return t.UTC().Format(ISOFormat), nil // the layout the events are stored in, so the text compares as a time
}

// QueriesEvents reports whether any parameter makes the events list a query (the plain list is unchanged without them).
func QueriesEvents(vals map[string][]string) bool {
	for _, k := range []string{"type", "q", "since", "until", "sort", "cursor"} {
		if _, ok := vals[k]; ok {
			return true
		}
	}
	return false
}

// ParseEventQuery validates the raw parameters for the events of agentName (and sessionID, when not nil); the error names what was wrong.
func ParseEventQuery(agentName string, sessionID *string, p EventQueryParams) (*EventQuery, error) {
	q := &EventQuery{AgentName: agentName, SessionID: sessionID, Limit: EventQueryDefaultLimit}
	seen := map[string]bool{}
	for _, v := range p.Type {
		for _, part := range strings.Split(v, ",") {
			part = strings.TrimSpace(part)
			if !eventTypeRe.MatchString(part) {
				return nil, eventQueryErr("type must be a lower-case word such as tool_use (comma separated or repeated, at most %d)", eventQueryMaxTypes)
			}
			if !seen[part] {
				seen[part] = true
				q.Types = append(q.Types, part)
			}
		}
	}
	if len(q.Types) > eventQueryMaxTypes {
		return nil, eventQueryErr("type has more than %d values", eventQueryMaxTypes)
	}
	if v, ok, err := eventSingle("q", p.Q); err != nil {
		return nil, err
	} else if ok {
		if querypage.HasControl(v) {
			return nil, eventQueryErr("q is at most %d characters, without control characters", EventQueryMaxTextLen)
		}
		v = strings.TrimSpace(v)
		if len(v) > EventQueryMaxTextLen {
			return nil, eventQueryErr("q is at most %d characters, without control characters", EventQueryMaxTextLen)
		}
		q.Text = strings.ToLower(v)
	}
	var err error
	if q.Since, err = eventTime("since", p.Since); err != nil {
		return nil, err
	}
	if q.Until, err = eventTime("until", p.Until); err != nil {
		return nil, err
	}
	if v, ok, serr := eventSingle("sort", p.Sort); serr != nil {
		return nil, serr
	} else if ok {
		switch v {
		case "newest":
		case "oldest":
			q.Oldest = true
		default:
			return nil, eventQueryErr("unknown sort %s (newest or oldest)", v)
		}
	}
	if v, ok, serr := eventSingle("limit", p.Limit); serr != nil {
		return nil, serr
	} else if ok {
		n := 0
		if _, scanErr := fmt.Sscanf(v, "%d", &n); scanErr != nil || fmt.Sprint(n) != v || n < 1 || n > EventQueryMaxLimit {
			return nil, eventQueryErr("limit must be a whole number from 1 to %d", EventQueryMaxLimit)
		}
		q.Limit = n
	}
	sid := ""
	if sessionID != nil {
		sid = "s:" + *sessionID
	}
	fp := sha256.New()
	fmt.Fprintf(fp, "%s|%s|%v|%s|%s|%s|%v", agentName, sid, q.Types, q.Text, q.Since, q.Until, q.Oldest)
	q.finger = hex.EncodeToString(fp.Sum(nil))[:16]
	if v, ok, serr := eventSingle("cursor", p.Cursor); serr != nil {
		return nil, serr
	} else if ok {
		c, cerr := decodeEventCursor(v)
		if cerr != nil {
			return nil, eventQueryErr("cursor is not one this server gave")
		}
		if c.Finger != q.finger {
			return nil, eventQueryErr("cursor was made for other filters or another sort")
		}
		q.cursor = c
	}
	return q, nil
}

// EventQueryResult is one page in the query's order; Total is every match, Truncated says the scan stopped at its bound.
type EventQueryResult struct {
	Events     []AgentEvent
	Total      int
	NextCursor string
	PrevCursor string
	Truncated  bool
}

type eventKey struct {
	ID        int64
	At        string
	EventType string
	Tool      string
	Summary   string
}

func (q *EventQuery) shown(s string) string {
	if q.Shown == nil || s == "" {
		return s
	}
	return q.Shown(s)
}

// QueryAgentEvents runs q.
func (s *TaskStore) QueryAgentEvents(ctx context.Context, q *EventQuery) (*EventQueryResult, error) {
	var where []string
	var args []any
	if q.AgentName != "" {
		where = append(where, "agent_name = ?")
		args = append(args, q.AgentName)
	}
	if q.SessionID != nil {
		where = append(where, "session_id = ?")
		args = append(args, *q.SessionID)
	}
	if len(q.Types) > 0 {
		where = append(where, "event_type IN ("+strings.TrimSuffix(strings.Repeat("?,", len(q.Types)), ",")+")")
		for _, t := range q.Types {
			args = append(args, t)
		}
	}
	if q.Since != "" {
		where = append(where, "created_at >= ?")
		args = append(args, q.Since)
	}
	if q.Until != "" {
		where = append(where, "created_at < ?")
		args = append(args, q.Until)
	}
	dir := "DESC"
	if q.Oldest {
		dir = "ASC"
	}
	stmt := "SELECT id, created_at, event_type, COALESCE(tool_name, ''), summary FROM agent_events"
	if len(where) > 0 {
		stmt += " WHERE " + strings.Join(where, " AND ")
	}
	stmt += " ORDER BY created_at " + dir + ", id " + dir
	rows, err := s.reader().QueryContext(ctx, stmt, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	pg := &querypage.Pager[eventKey]{Limit: q.Limit}
	if q.cursor != nil {
		c := q.cursor
		pg.Prev = c.Prev
		pg.After = func(k eventKey) bool { // the event comes after the cursor in the query's order
			cmp := strings.Compare(k.At, c.At)
			if cmp == 0 {
				if q.Oldest {
					return k.ID > c.ID
				}
				return k.ID < c.ID
			}
			if q.Oldest {
				return cmp > 0
			}
			return cmp < 0
		}
		pg.AtCursor = func(k eventKey) bool { return k.At == c.At && k.ID == c.ID }
	}
	res := &EventQueryResult{}
	scannedRows, scannedBytes := 0, 0
	for rows.Next() {
		var k eventKey
		if err := rows.Scan(&k.ID, &k.At, &k.EventType, &k.Tool, &k.Summary); err != nil {
			return nil, err
		}
		if scannedRows++; scannedRows > eventScanRows || scannedBytes+len(k.Summary)+len(k.Tool) > eventScanBytes {
			res.Truncated = true
			break
		}
		scannedBytes += len(k.Summary) + len(k.Tool)
		if q.Text != "" && !strings.Contains(strings.ToLower(q.shown(k.Summary)), q.Text) && !strings.Contains(strings.ToLower(q.shown(k.Tool)), q.Text) {
			continue
		}
		pg.Add(k)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	res.Total = pg.Total
	res.Events = []AgentEvent{}
	if len(pg.Page) > 0 {
		ids := make([]any, len(pg.Page))
		for i, k := range pg.Page {
			ids[i] = k.ID
		}
		var full []AgentEvent
		if err := s.reader().SelectContext(ctx, &full,
			`SELECT id, agent_name, session_id, event_type, tool_name, summary, detail_json, created_at, duration_ms, ref_id
			 FROM agent_events WHERE id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`)`, ids...); err != nil {
			return nil, err
		}
		byID := make(map[int64]AgentEvent, len(full))
		for _, e := range full {
			byID[e.ID] = e
		}
		for _, k := range pg.Page {
			if e, ok := byID[k.ID]; ok { // an event cleared between the two reads is left out
				res.Events = append(res.Events, e)
			}
		}
		first, last := pg.Page[0], pg.Page[len(pg.Page)-1]
		if pg.HasNext() {
			res.NextCursor = eventCursor{Finger: q.finger, At: last.At, ID: last.ID}.encode()
		}
		if pg.HasPrev() {
			res.PrevCursor = eventCursor{Finger: q.finger, At: first.At, ID: first.ID, Prev: true}.encode()
		}
	}
	return res, nil
}
