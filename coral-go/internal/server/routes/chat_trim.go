package routes

import "unicode/utf8"

// A chat page asked for with ?trim=N (#3723) carries at most N bytes of each long tool field:
// a tool's output, a written file, an edit's old and new text, a patch. These are folded away in
// the chat, and on the operator's transcripts they were a third of a page's bytes. A shortened
// field is marked: a tool_result message gets "trimmed": true, a tool in tool_uses gets
// "trimmed": true, and the message too. The dashboard loads that message in full (the same page
// request for that one message, without trim) when its card is opened.

// trimMin and trimMax bound a requested trim (bytes).
const (
	trimMin = 256
	trimMax = 64 << 10
)

// trimmedToolFields are the tool_uses fields that are shortened.
var trimmedToolFields = []string{"write_content", "old_string", "new_string", "patch"}

// trimChatPage returns page with long tool fields cut to n bytes. The messages are shared with
// the transcript reader's cache, so a message that changes is copied, never edited.
func trimChatPage(page []map[string]any, n int) []map[string]any {
	n = min(max(n, trimMin), trimMax)
	out := page
	copied := false
	for i, m := range page {
		t, changed := trimChatMessage(m, n)
		if !changed {
			continue
		}
		if !copied {
			out = append([]map[string]any(nil), page...)
			copied = true
		}
		out[i] = t
	}
	return out
}

func trimChatMessage(m map[string]any, n int) (map[string]any, bool) {
	var c map[string]any
	clone := func() {
		if c == nil {
			c = make(map[string]any, len(m)+1)
			for k, v := range m {
				c[k] = v
			}
		}
	}
	if m["type"] == "tool_result" {
		if s, ok := m["content"].(string); ok && len(s) > n {
			clone()
			c["content"] = cutUTF8(s, n)
			c["trimmed"] = true
		}
	}
	var tools []map[string]any
	switch v := m["tool_uses"].(type) {
	case []map[string]any:
		tools = v
	case []any:
		for _, x := range v {
			if t, ok := x.(map[string]any); ok {
				tools = append(tools, t)
			} else {
				tools = nil // an unexpected shape: left as it is
				break
			}
		}
	}
	var newTools []map[string]any
	for i, t := range tools {
		var tc map[string]any
		for _, f := range trimmedToolFields {
			if s, ok := t[f].(string); ok && len(s) > n {
				if tc == nil {
					tc = make(map[string]any, len(t)+1)
					for k, v := range t {
						tc[k] = v
					}
				}
				tc[f] = cutUTF8(s, n)
				tc["trimmed"] = true
			}
		}
		if tc == nil {
			continue
		}
		if newTools == nil {
			newTools = append([]map[string]any(nil), tools...)
		}
		newTools[i] = tc
	}
	if newTools != nil {
		clone()
		c["tool_uses"] = newTools
		c["trimmed"] = true
	}
	if c == nil {
		return m, false
	}
	return c, true
}

// cutUTF8 is s cut to at most n bytes, at a character boundary.
func cutUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
