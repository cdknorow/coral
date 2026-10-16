package tunnel

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// A JSON reply held whole is redacted by its structure (#3732, Security's option on #3731): it
// is decoded, each string in it (keys and values) goes through the redactor as the text it
// stands for, and it is encoded again. Escaping can then never hide a token from the redactor
// (a JSON string's \" is a plain " to it), and redaction can never break the reply's JSON (the
// encoder writes it). Key order and numbers are kept as they were; the reply is written compact.
// The text redactor's quoting rules then only matter for replies that are streamed, or aren't
// JSON.
//
// A value is redacted together with its key, as "key: value" (Security on #3732): in
// {"Authorization": "Basic ..."} the keyword is the key and the credential the value, and the
// Authorization rule needs both. Strings in an array take the key of the member holding it
// ({"Authorization": ["Basic ..."]}, as Go writes an http.Header), and an object's "value" takes
// its "name" (or "key") member's text, for headers written as {"name": ..., "value": ...} (HAR
// files, browser automation logs). The reply is read whole into a tree first, since "name" may
// come after "value".

// jnode is one JSON value: an object (keys and vals), an array (vals), a string (str), or a
// number, true, false or null (lit, as written).
type jnode struct {
	kind byte // '{', '[', 's' or 'l'
	str  string
	lit  string
	keys []string
	vals []*jnode
}

// maxJSONDepth bounds the nesting read (the reply is at most jsonWholeMax bytes; this keeps the
// recursion bounded too). A deeper reply is not walked: the text redactor takes it.
const maxJSONDepth = 512

// readJSONNode reads one value from dec.
func readJSONNode(dec *json.Decoder, depth int) (*jnode, bool) {
	if depth > maxJSONDepth {
		return nil, false
	}
	tok, err := dec.Token()
	if err != nil {
		return nil, false
	}
	switch v := tok.(type) {
	case json.Delim:
		n := &jnode{kind: byte(v)}
		if v != '{' && v != '[' {
			return nil, false
		}
		for dec.More() {
			if v == '{' {
				kt, err := dec.Token()
				k, isStr := kt.(string)
				if err != nil || !isStr {
					return nil, false
				}
				n.keys = append(n.keys, k)
			}
			c, ok := readJSONNode(dec, depth+1)
			if !ok {
				return nil, false
			}
			n.vals = append(n.vals, c)
		}
		if _, err := dec.Token(); err != nil { // the closing delimiter
			return nil, false
		}
		return n, true
	case string:
		return &jnode{kind: 's', str: v}, true
	case json.Number:
		return &jnode{kind: 'l', lit: string(v)}, true
	case bool:
		if v {
			return &jnode{kind: 'l', lit: "true"}, true
		}
		return &jnode{kind: 'l', lit: "false"}, true
	case nil:
		return &jnode{kind: 'l', lit: "null"}, true
	}
	return nil, false
}

// redact redacts n's strings in place; key (when has) is the key its strings are redacted with.
func (n *jnode) redact(key string, has bool) {
	switch n.kind {
	case 's':
		if has {
			n.str = redactMember(key, n.str)
		} else {
			n.str = string(RedactTokens([]byte(n.str)))
		}
	case '[':
		for _, c := range n.vals {
			c.redact(key, has)
		}
	case '{':
		name, hasName := "", false
		for i, k := range n.keys {
			if lk := strings.ToLower(k); (lk == "name" || lk == "key") && n.vals[i].kind == 's' {
				name, hasName = n.vals[i].str, true // read before any redaction of it
				break
			}
		}
		for i, k := range n.keys {
			ctx := k
			if hasName && strings.EqualFold(k, "value") {
				ctx = name
			}
			n.vals[i].redact(ctx, true)
			n.keys[i] = string(RedactTokens([]byte(k)))
		}
	}
}

// write writes n as compact JSON.
func (n *jnode) write(b *bytes.Buffer) bool {
	switch n.kind {
	case 's':
		return writeJSONString(b, n.str)
	case 'l':
		b.WriteString(n.lit)
		return true
	}
	b.WriteByte(n.kind)
	for i, c := range n.vals {
		if i > 0 {
			b.WriteByte(',')
		}
		if n.kind == '{' {
			if !writeJSONString(b, n.keys[i]) {
				return false
			}
			b.WriteByte(':')
		}
		if !c.write(b) {
			return false
		}
	}
	if n.kind == '{' {
		b.WriteByte('}')
	} else {
		b.WriteByte(']')
	}
	return true
}

// redactJSONStructure is raw (valid JSON) with every string redacted. ok is false if raw isn't
// one JSON value that can be walked.
func redactJSONStructure(raw []byte) (out []byte, ok bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	root, ok := readJSONNode(dec, 0)
	if !ok {
		return nil, false
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, false // one JSON value only
	}
	root.redact("", false)
	var b bytes.Buffer
	b.Grow(len(raw))
	if !root.write(&b) {
		return nil, false
	}
	return b.Bytes(), true
}

// redactMember redacts value v as the text "key: v", so a rule that needs its keyword
// (Authorization) sees it, and returns the value's part, redacted once more on its own. A key
// that redaction changed (it held a token itself) leaves the value's place unknown: the value is
// then the marker (fail closed).
func redactMember(key, v string) string {
	pre := key + ": "
	r := string(RedactTokens([]byte(pre + v)))
	if !strings.HasPrefix(r, pre) {
		return RedactMarker
	}
	return string(RedactTokens([]byte(r[len(pre):])))
}

// writeJSONString writes s as a JSON string, without the HTML escaping json.Marshal adds.
func writeJSONString(b *bytes.Buffer, s string) bool {
	var one bytes.Buffer
	enc := json.NewEncoder(&one)
	enc.SetEscapeHTML(false)
	if enc.Encode(s) != nil {
		return false
	}
	b.Write(bytes.TrimSuffix(one.Bytes(), []byte("\n")))
	return true
}

// redactReply redacts a reply held whole: by its structure when it is JSON (declared and
// valid), as text otherwise. A panic in either is a failure, so a reply is never sent unscanned.
func redactReply(hdr http.Header, raw []byte) (out []byte, ok bool) {
	defer func() {
		if recover() != nil {
			out, ok = nil, false
		}
	}()
	if jsonReply(hdr) && json.Valid(raw) {
		if out, ok := redactJSONStructure(raw); ok {
			return out, true
		}
		// Too deep to walk: redacted as text, as before #3732 (and still checked to parse).
	}
	return RedactTokens(raw), true
}
