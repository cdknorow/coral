package redact

import "bytes"

// URLPasswordSpans is the span of the password of every "scheme://user:password@host" in text (#4769): what the writer's URL rule (#3756)
// redacts, for a Detector that finds spans in text it holds whole (internal/secrets' rules beside betterleaks', which has none for a
// URL's credentials, so Coral Hub's viewers saw them). The scheme, the user and the host are not in a span. The same measure as the
// writer's: the user ends at the first ':' (empty for redis://:pw@host), the password runs to the LAST '@' before the next space, quote or
// "://" (so "p@ss:w0rd" is one password, and a line of URLs is scanned in linear time), at most maxURLRun bytes, and digits that end at a
// '/', '?', '#' or the end are a port (https://host:8080/path), not a password. A URL with no password (user@host, an empty one) has no span.
func URLPasswordSpans(text string) []Span {
	var out []Span
	b := []byte(text)
	sep := []byte("://")
	for from := 0; from < len(b); {
		i := bytes.Index(b[from:], sep)
		if i < 0 {
			break
		}
		start := from + i + len(sep)
		from = start
		glue, _ := urlUserGlue(b[start:])
		if glue <= 0 {
			continue
		}
		pw := start + glue
		if n, _ := urlPassword(b[pw:], true); n > 0 {
			out = append(out, Span{Start: pw, End: pw + n})
			from = pw + n
		}
	}
	return out
}
