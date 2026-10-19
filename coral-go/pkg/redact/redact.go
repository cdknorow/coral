package redact

// Token redaction for Coral Hub viewers (#3560, design c73e2d6a). The connector replaces
// well-known credential formats in the text replies of View routes with Marker before
// they leave this computer; local requests never pass through here. Rules: a literal prefix,
// a character class, a length floor and ceiling, and a word boundary before the token. There
// is no entropy rule, so hashes, UUIDs and base64 are left alone. A PEM private-key block is
// removed from its BEGIN line to its END line, to the end of the JSON string it sits in, or to
// the end of the reply, however long it is: a key is never flushed in part.
//
// The Writer streams: each Write emits everything that can no longer be part of a token and
// holds back only a tail that could still start or continue one (bounded by the longest rule,
// or the few bytes that could start a key's END line), so memory stays flat whatever the
// reply's size. A body redacted in one Write comes out byte for byte the same as any split of
// it (tested).
//
// The rules' live stream (NewLiveStream, #3958) calls Flush with force after every write, so it
// holds nothing: see Flush.

import (
	"bytes"
)

// Marker replaces a token. For Bearer and Authorization values the scheme is kept.
const Marker = "[redacted token]"

// Tokens redacts a whole body.
func Tokens(b []byte) []byte {
	var out bytes.Buffer
	r := NewWriter(&out)
	_, _ = r.Write(b)
	_ = r.Close()
	return out.Bytes()
}
