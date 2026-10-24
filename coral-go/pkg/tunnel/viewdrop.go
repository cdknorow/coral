package tunnel

import (
	"bytes"
	"encoding/json"
	"net/http"
)

// What an agent waits for is the agent's own text (#6581, #6592): the live row's waiting_summary is cleaned and redacted on the computer, but a request can still be a command, and the
// operator's rule is that a view-only viewer does not see commands. So a viewer who can only look (the lower of its tier and this computer's mode is View) never receives the field: it
// is null in every reply and frame that carries it, whatever the owner's redaction switch says (that switch is about secrets; this is about who may see the text).

// viewDroppedFields are the keys set to null for a view-only viewer.
// waiting_summary is what an agent waits for (its command, #6592); held_messages are a person's own chat messages queued behind the agent's question (#6627): neither is for a viewer who
// cannot send.
//
// A blocked goal's blocked_on, decision, recommendation and if_silent (#7087) are what a person is asked to decide and what is recommended: the same rule, so a view-only viewer sees the goal
// (title, outcome, done_when, state, driver, rollup) and not what it waits for.
var viewDroppedFields = map[string]bool{"waiting_summary": true, "held_messages": true, "blocked_on": true, "decision": true, "recommendation": true, "if_silent": true}

// viewDropRoutes are the View routes whose JSON replies carry a live row (or part of one).
var viewDropRoutes = map[string]bool{
	"GET /api/sessions/live":         true,
	"GET /api/sessions/{id}/resolve": true,
	"GET /api/sessions/{id}/status":  true,
	// A board's goals (#7087): the list answers {"goals": [...]}, the other one goal.
	"GET /api/board/{project}/goals":      true,
	"GET /api/board/{project}/goals/{id}": true,
}

// viewDropMax is the largest reply read whole to drop a field (a live list is a few hundred KiB at most).
const viewDropMax = 16 << 20

// viewOnly reports, each time it is called, whether a viewer of this tier can only look: the lower of its tier and the computer's current mode is View. A stream calls it for every frame.
func (l *LocalHandler) viewOnly(viewer Tier) func() bool {
	return func() bool { return Lower(viewer, currentMode(l.MaxMode)) <= TierView }
}

// dropsFor reports whether a reply to this request must have viewDroppedFields dropped.
func dropsFor(method, decodedPath string) bool {
	if method == http.MethodHead {
		method = http.MethodGet
	}
	pattern, tier, ok := RuleFor(method, decodedPath)
	return ok && tier == TierView && viewDropRoutes[method+" "+pattern]
}

// maxDropDepth bounds how deep a document is walked.
const maxDropDepth = 64

// stripViewDropped is data (one JSON document) with every viewDroppedFields key set to null, wherever it is. A document that does not hold such a key is returned as it is; one that does
// and cannot be parsed is refused (ok false), never sent on.
func stripViewDropped(data []byte) (out []byte, ok bool) {
	hit := false
	for k := range viewDroppedFields {
		if bytes.Contains(data, []byte(`"`+k+`"`)) {
			hit = true
		}
	}
	if !hit {
		return data, true
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber() // numbers go back as they came
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	if !dropKeys(v, 0) {
		return nil, false
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, false
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), true
}

func dropKeys(v any, depth int) bool {
	if depth > maxDropDepth {
		return false
	}
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			if viewDroppedFields[k] {
				t[k] = nil
				continue
			}
			if !dropKeys(child, depth+1) {
				return false
			}
		}
	case []any:
		for _, child := range t {
			if !dropKeys(child, depth+1) {
				return false
			}
		}
	}
	return true
}
