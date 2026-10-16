package tunnel

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

// A JSON reply held whole is redacted by its structure (#3732, Security's option on #3731): it
// is decoded, each string in it (keys and values) goes through the redactor as the text it
// stands for, and it is encoded again. Escaping can then never hide a token from the redactor
// (a JSON string's \" is a plain " to it), and redaction can never break the reply's JSON (the
// encoder writes it). Key order and numbers are kept as they were; the reply is written compact.
// The text redactor's quoting rules then only matter for replies that are streamed, or aren't
// JSON.

// redactJSONStructure is raw (valid JSON) with every string redacted. ok is false if raw can't
// be walked as one JSON value.
func redactJSONStructure(raw []byte) (out []byte, ok bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var b bytes.Buffer
	b.Grow(len(raw))
	type frame struct {
		obj bool // an object (else an array)
		n   int  // values written in it
		key bool // in an object: a key comes next
	}
	var stack []frame
	// done records that a key or a value was written in the innermost container.
	done := func() {
		if len(stack) == 0 {
			return
		}
		f := &stack[len(stack)-1]
		switch {
		case f.obj && f.key:
			f.key = false
		case f.obj:
			f.key, f.n = true, f.n+1
		default:
			f.n++
		}
	}
	values := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, false
		}
		if d, isDelim := tok.(json.Delim); isDelim && (d == '}' || d == ']') {
			if len(stack) == 0 {
				return nil, false
			}
			stack = stack[:len(stack)-1]
			b.WriteByte(byte(d))
			done()
			continue
		}
		if len(stack) == 0 {
			if values++; values > 1 {
				return nil, false // one JSON value only
			}
		} else if f := stack[len(stack)-1]; f.obj && !f.key {
			b.WriteByte(':')
		} else if f.n > 0 {
			b.WriteByte(',')
		}
		switch v := tok.(type) {
		case json.Delim:
			b.WriteByte(byte(v))
			stack = append(stack, frame{obj: v == '{', key: v == '{'})
			continue // done when it closes
		case string:
			if !writeJSONString(&b, string(RedactTokens([]byte(v)))) {
				return nil, false
			}
		case json.Number:
			b.WriteString(string(v))
		case bool:
			if v {
				b.WriteString("true")
			} else {
				b.WriteString("false")
			}
		case nil:
			b.WriteString("null")
		default:
			return nil, false
		}
		done()
	}
	if len(stack) != 0 || values != 1 {
		return nil, false
	}
	return b.Bytes(), true
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
		return redactJSONStructure(raw)
	}
	return RedactTokens(raw), true
}
