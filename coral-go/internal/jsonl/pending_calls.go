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
//
// A parse only notes the results it returned (noteResults: a poll of a transcript that gained a few lines
// does nothing else); the calls are read from toolUseNames when a checkpoint is taken (snapshot), once
// a megabyte of transcript, not on every poll.
type pendingCalls struct {
	calls    map[string]pendingCall
	known    map[string]struct{} // the names already taken into calls, to tell a call new to toolUseNames
	answered map[string]struct{} // the results since the last snapshot
	gen      uint32              // counts the snapshots (the order of the calls within one is not kept)
}

type pendingCall struct {
	name     string
	gen      uint32
	resolved bool
}

// noteResults notes the tool results among the messages a parse returned.
func (p *pendingCalls) noteResults(parsed []map[string]any) {
	for _, m := range parsed {
		if m["type"] != "tool_result" {
			continue
		}
		if id, _ := m["tool_use_id"].(string); id != "" {
			if p.answered == nil {
				p.answered = map[string]struct{}{}
			}
			p.answered[id] = struct{}{}
		}
	}
}

// snapshot takes the calls names holds that are new, marks those answered since the last snapshot, keeps the
// bound, and returns what a checkpoint carries: the calls, and the "__" entries of names as they stand. nil
// when there are none.
func (p *pendingCalls) snapshot(names map[string]string) map[string]string {
	if p.calls == nil {
		p.calls, p.known = map[string]pendingCall{}, map[string]struct{}{}
	}
	p.gen++
	var last map[string]string
	for k, v := range names {
		if strings.HasPrefix(k, "__") {
			if last == nil {
				last = map[string]string{}
			}
			last[k] = v
			continue
		}
		if _, ok := p.known[k]; !ok {
			p.known[k] = struct{}{}
			p.calls[k] = pendingCall{name: v, gen: p.gen}
		}
	}
	for id := range p.answered {
		if c, ok := p.calls[id]; ok {
			c.resolved = true
			p.calls[id] = c
		}
	}
	clear(p.answered)
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
	if len(p.calls) == 0 && len(last) == 0 {
		return nil
	}
	out := make(map[string]string, len(p.calls)+len(last))
	for id, c := range p.calls {
		out[id] = c.name
	}
	for k, v := range last {
		out[k] = v
	}
	return out
}

// noteDropped is the hook boundNames calls with each name it is about to drop: a call the snapshots have not seen yet
// (it came after the last checkpoint of its chunk) is taken now, from names, as new to the next snapshot, so the bound on
// the names cannot lose a call a checkpoint should carry. It costs what is dropped, nothing for a poll that drops none.
func (p *pendingCalls) noteDropped(names map[string]string) func(id string) {
	return func(id string) {
		if _, ok := p.known[id]; ok {
			return
		}
		if p.calls == nil {
			p.calls, p.known = map[string]pendingCall{}, map[string]struct{}{}
		}
		p.known[id] = struct{}{}
		p.calls[id] = pendingCall{name: names[id], gen: p.gen + 1}
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
