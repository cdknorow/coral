package redact

import (
	"bytes"
	"encoding/json"
	"io"
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
//
// And the Authorization keyword reaches what it governs however it is nested (Security's SHOULD on
// #3732): every string inside a member whose key contains "authorization" is redacted as an
// Authorization value ({"Authorization": {"scheme": "Basic", "credentials": "..."}}), and in an
// object where one string is, or ends with, "authorization", so are the others
// ({"k": "Authorization", "v": "Basic ..."}, whatever the members are called).

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

// authKeyword is the keyword a string under an Authorization member is redacted with.
const authKeyword = "Authorization"

// namesAuthorization reports whether s is, or ends with, "authorization" (any case, an optional
// colon, spaces around): a member saying that the others hold an Authorization value.
func namesAuthorization(s string) bool {
	t := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(strings.ToLower(s)), ":"))
	return strings.HasSuffix(t, "authorization")
}

// redact redacts n's strings in place; key (when has) is the key its strings are redacted with,
// and auth means they are under an Authorization member (redacted as its value).
//
// Under a member whose key names a secret (isSecretKey, #3733), secret is set: every value in
// its subtree, string or number or true/false, is the marker (a string, so the reply stays
// JSON); an empty string and null stay, since they hold nothing.
func (n *jnode) redact(key string, has, auth, secret bool) {
	if secret {
		switch {
		case n.kind == 's' && n.str != "", n.kind == 'l' && n.lit != "null":
			n.kind, n.str, n.lit = 's', Marker, ""
			return
		case n.kind == 's' || n.kind == 'l':
			return
		}
	}
	switch n.kind {
	case 's':
		switch {
		case auth:
			n.str = redactMember(authKeyword, n.str)
		case has:
			n.str = redactMember(key, n.str)
		default:
			n.str = string(Tokens([]byte(n.str)))
		}
	case '[':
		for _, c := range n.vals {
			c.redact(key, has, auth, secret)
		}
	case '{':
		name, hasName := "", false
		for i, k := range n.keys {
			if lk := strings.ToLower(k); (lk == "name" || lk == "key") && n.vals[i].kind == 's' {
				name, hasName = n.vals[i].str, true // read before any redaction of it
				break
			}
		}
		mentions := 0 // string members that name authorization (read before any is redacted)
		names := make([]bool, len(n.vals))
		for i, v := range n.vals {
			if v.kind == 's' && namesAuthorization(v.str) {
				names[i] = true
				mentions++
			}
		}
		for i, k := range n.keys {
			ctx := k
			if hasName && strings.EqualFold(k, "value") {
				ctx = name
			}
			other := mentions
			if names[i] {
				other--
			}
			childAuth := auth || strings.Contains(strings.ToLower(k), "authorization") ||
				other > 0 && n.vals[i].kind == 's'
			n.vals[i].redact(ctx, true, childAuth, secret || isSecretKey(ctx))
			n.keys[i] = string(Tokens([]byte(k)))
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

// JSON is raw (valid JSON) with every string redacted. ok is false if raw isn't
// one JSON value that can be walked.
func JSON(raw []byte) (out []byte, ok bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	root, ok := readJSONNode(dec, 0)
	if !ok {
		return nil, false
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, false // one JSON value only
	}
	root.redact("", false, false, false)
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
	r := string(Tokens([]byte(pre + v)))
	if !strings.HasPrefix(r, pre) {
		return Marker
	}
	return string(Tokens([]byte(r[len(pre):])))
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

// Document redacts a JSON document kept at rest (an agent event's detail, #3763): by its
// structure when it is valid JSON that can be walked, as text otherwise. It never fails: what
// can't be walked is redacted as text.
func Document(raw []byte) []byte {
	out := Tokens(raw)
	if json.Valid(raw) {
		if s, ok := JSON(raw); ok {
			out = s
		}
	}
	// Redaction only ever puts the marker in: with no new one, nothing was found, and the
	// document is kept as it was written (the walk re-encodes it, \u0026 and all).
	if bytes.Count(out, []byte(Marker)) == bytes.Count(raw, []byte(Marker)) {
		return raw
	}
	return out
}

// Reply redacts a reply held whole: by its structure when it is JSON (isJSON: declared so, and
// valid), as text otherwise. A panic in either is a failure, so a reply is never sent unscanned.
func Reply(raw []byte, isJSON bool) (out []byte, ok bool) {
	defer func() {
		if recover() != nil {
			out, ok = nil, false
		}
	}()
	if isJSON && json.Valid(raw) {
		if out, ok := JSON(raw); ok {
			return out, true
		}
		// Too deep to walk: redacted as text, as before #3732 (and still checked to parse).
	}
	return Tokens(raw), true
}

// isSecretKey reports whether a JSON key names a secret (#3733): one of secretNames or its
// plural, or a key ending in one after '_' (db_password, access_token, client_secret, x_api_key,
// aws_secret_access_key, secrets). Case, camel case, '-', '.' and spaces are read as '_' (apiKey,
// refreshToken, X-Api-Key). "tokens" is not a plural of a secret: Coral's replies count model
// tokens under it (input_tokens). A word that only starts like one is not ("secretary",
// "author", "total_token_usage").
func isSecretKey(key string) bool {
	var b strings.Builder
	for i := 0; i < len(key); i++ {
		c := key[i]
		switch {
		case c >= 'A' && c <= 'Z':
			if i > 0 && (key[i-1] >= 'a' && key[i-1] <= 'z' || key[i-1] >= '0' && key[i-1] <= '9') {
				b.WriteByte('_')
			}
			b.WriteByte(c + 'a' - 'A')
		case c == '-' || c == ' ' || c == '.':
			b.WriteByte('_')
		default:
			b.WriteByte(c)
		}
	}
	k := b.String()
	for _, n := range secretNames {
		for _, form := range [2]string{n, n + "s"} {
			if form == "tokens" || form == "auths" {
				continue
			}
			if k == form || strings.HasSuffix(k, "_"+form) {
				return true
			}
		}
	}
	return false
}
