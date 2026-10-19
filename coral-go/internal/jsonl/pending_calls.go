package jsonl

import (
	"sort"
	"strings"
)

// pendingCallsMax bounds what a checkpoint carries of the tool calls before it.
const pendingCallsMax = 64

// pendingCalls follows the tool calls a read has parsed, for the checkpoints (#5023): a result is named
// from its call (toolUseNames), and an old page that starts at a checkpoint has not parsed the call
// when the call is before it. The calls whose result has not come, up to pendingCallsMax, are carried
// by the checkpoint and seed the page's names; a call that has had its result is kept only while
// there is room, newest first, since a second result for it (a Codex output and its event) is named
// the same. Entries starting "__" (the last call of an Antigravity reply) are always carried.
type pendingCalls struct {
	calls map[string]pendingCall
	known map[string]struct{} // the names seen so far, to tell a call new to toolUseNames
	gen   uint32              // counts the parses (the order of the calls within one is not kept)
	last  map[string]string   // the "__" entries
}

type pendingCall struct {
	name     string
	gen      uint32
	resolved bool
}

// observe notes the calls a parse added to names and the results it returned.
func (p *pendingCalls) observe(names map[string]string, parsed []map[string]any) {
	if p.calls == nil {
		p.calls, p.known = map[string]pendingCall{}, map[string]struct{}{}
	}
	p.gen++
	for k, v := range names {
		if strings.HasPrefix(k, "__") {
			if p.last == nil {
				p.last = map[string]string{}
			}
			p.last[k] = v
			continue
		}
		if _, ok := p.known[k]; !ok {
			p.known[k] = struct{}{}
			p.calls[k] = pendingCall{name: v, gen: p.gen}
		}
	}
	for _, m := range parsed {
		if m["type"] != "tool_result" {
			continue
		}
		if id, _ := m["tool_use_id"].(string); id != "" {
			if c, ok := p.calls[id]; ok {
				c.resolved = true
				p.calls[id] = c
			}
		}
	}
	if len(p.calls) > pendingCallsMax {
		p.trim()
	}
	if len(p.known) > 2*(toolNamesMax+pendingCallsMax) { // forget the names the read has dropped
		p.known = make(map[string]struct{}, len(names))
		for k := range names {
			p.known[k] = struct{}{}
		}
		for k := range p.calls {
			p.known[k] = struct{}{}
		}
	}
}

// trim drops the calls past the bound: those with a result first, then the oldest.
func (p *pendingCalls) trim() {
	type entry struct {
		id string
		pendingCall
	}
	list := make([]entry, 0, len(p.calls))
	for id, c := range p.calls {
		list = append(list, entry{id, c})
	}
	sort.Slice(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.resolved != b.resolved {
			return !a.resolved // pending first: kept
		}
		if a.gen != b.gen {
			return a.gen > b.gen // newer first
		}
		return a.id < b.id
	})
	for _, e := range list[pendingCallsMax:] {
		delete(p.calls, e.id)
	}
}

// snapshot is the names a checkpoint carries: nil when there are none.
func (p *pendingCalls) snapshot() map[string]string {
	if len(p.calls) == 0 && len(p.last) == 0 {
		return nil
	}
	out := make(map[string]string, len(p.calls)+len(p.last))
	for id, c := range p.calls {
		out[id] = c.name
	}
	for k, v := range p.last {
		out[k] = v
	}
	return out
}
