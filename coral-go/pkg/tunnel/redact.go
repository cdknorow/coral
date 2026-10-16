package tunnel

// Token redaction for Coral Hub viewers (#3560, design c73e2d6a). The connector replaces
// well-known credential formats in the text replies of View routes with RedactMarker before
// they leave this computer; local requests never pass through here. Rules: a literal prefix,
// a character class, a length floor and ceiling, and a word boundary before the token. There
// is no entropy rule, so hashes, UUIDs and base64 are left alone. A PEM private-key block is
// removed from its BEGIN line to its END line, to the end of the JSON string it sits in, or to
// the end of the reply, however long it is: a key is never flushed in part.
//
// The Redactor streams: each Write emits everything that can no longer be part of a token and
// holds back only a tail that could still start or continue one (bounded by the longest rule,
// or the few bytes that could start a key's END line), so memory stays flat whatever the
// reply's size. A body redacted in one Write comes out byte for byte the same as any split of
// it (tested).
//
// A live stream that goes quiet (a terminal, #3562) calls Flush, which settles the held tail
// without ending the stream: see Flush.

import (
	"bytes"
	"io"
	"sort"
	"strings"
)

// RedactMarker replaces a token. For Bearer and Authorization values the scheme is kept.
const RedactMarker = "[redacted token]"

type tokenRule struct {
	prefixes []string                 // literal starts (case variants listed explicitly)
	glue     func([]byte) (int, bool) // after the prefix, the separator before the value and whether it named a scheme (Authorization); nil: none
	class    func(byte) bool          // the value's characters
	min, max int                      // the value's length; a longer run is still redacted (max only bounds what is held back)
	exact    bool                     // the value is exactly min long (AKIA)
	keep     bool                     // keep the prefix and glue, replace only the value (Bearer, Authorization)
	notAfter func(byte) bool          // a character that may not come right before the prefix
	params   bool                     // the value may be an auth-param list (Digest's key="value", ...): redacted to its end
	bare     func([]byte) bool        // when glue reports no scheme: whether the value is a credential (nil: mixedValue)
	toQuote  bool                     // a value after an opening quote runs to its closing quote, spaces and all (#3733)
	// value, when set, measures the value itself (in place of class, min and max): its length, or
	// need when b ends before that can be told (never when final). A URL's password ends at the
	// last '@' before its host.
	value func(b []byte, final bool) (n int, need bool)
}

// prefixLen is the length of the longest of the rule's prefixes b starts with (SECRET_KEY, not
// SECRET; credentials, not credential), 0 when none. The secret names are a set, looked up per
// length (#3733: about a hundred spellings).
func (ru *tokenRule) prefixLen(b []byte) int {
	if ru == secretRule {
		for _, n := range secretNameLens {
			if n <= len(b) && secretNameSet[string(b[:n])] {
				return n
			}
		}
		return 0
	}
	at := 0
	for _, pre := range ru.prefixes {
		if len(pre) > at && bytes.HasPrefix(b, []byte(pre)) {
			at = len(pre)
		}
	}
	return at
}

// bareOK: an unqualified value (no scheme, not quoted) counts as a credential.
func (ru *tokenRule) bareOK(v []byte) bool {
	if ru.bare != nil {
		return ru.bare(v)
	}
	return mixedValue(v)
}

func isAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}
func isWord(c byte) bool       { return isAlnum(c) || c == '_' }
func isUpperDigit(c byte) bool { return c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' }
func isB64URL(c byte) bool     { return isAlnum(c) || c == '_' || c == '-' }
func isAlnumDash(c byte) bool  { return isAlnum(c) || c == '-' }
func isCredValue(c byte) bool {
	return isAlnum(c) || c == '.' || c == '_' || c == '~' || c == '+' || c == '/' || c == '=' || c == '-'
}

// authGlue measures what follows "authorization" up to its value: an optional closing quote,
// spaces, ':' or '=', spaces, an optional opening quote, and an optional scheme word and a
// space. A quote is one however it is written (quoteLen): in a JSON string it is \" (#3731: a
// quoted "authorization": "Basic ..." passed through Coral Hub, whose replies are JSON). Terminal escape codes (colour) may sit anywhere a space may (#3562). -1: not an
// Authorization value; -2: the buffer ended before that could be told. scheme reports whether
// a scheme word came before the value.
func authGlue(b []byte) (n int, scheme bool) {
	i := 0
	if q := quoteLen(b); q < 0 {
		return q, false
	} else {
		i += q
	}
	if i = skipGap(b, i); i < 0 {
		return i, false
	}
	if i >= len(b) {
		return -2, false
	}
	if b[i] != ':' && b[i] != '=' {
		return -1, false
	}
	if i = skipGap(b, i+1); i < 0 {
		return i, false
	}
	if q := quoteLen(b[i:]); q < 0 {
		return q, false
	} else {
		i += q
	}
	j := i
	for j < len(b) && j-i <= 16 && (b[j] >= 'a' && b[j] <= 'z' || b[j] >= 'A' && b[j] <= 'Z') {
		j++
	}
	if j == len(b) {
		return -2, false
	}
	if j > i && j-i <= 16 {
		if k := skipGap(b, j); k == -2 || k == len(b) {
			return -2, false // the gap after the scheme word may go on in the next chunk
		} else if k > j && hasSpace(b[j:k]) {
			i, scheme = k, true
		}
	}
	if i >= len(b) {
		return -2, false
	}
	return i, scheme
}

// maxQuoteEscape bounds the backslashes taken before a quote: \" in a JSON string, \\\" when
// the text itself had an escaped quote, and so on.
const maxQuoteEscape = 8

// quoteLen is the length of the quote at the start of b, however it is written: " or ',
// after an odd run of backslashes (JSON writes " as \", and a \" in the text as \\\"), or a JSON \u0022
// or \u0027. 0: no quote there; -2: b ends before that can be told. When unsure, a quote: an
// Authorization value after it is then redacted (fail closed).
func quoteLen(b []byte) int {
	i := 0
	for i < len(b) && b[i] == '\\' && i < maxQuoteEscape {
		i++
	}
	if i == len(b) {
		if i > 0 {
			return -2
		}
		return 0
	}
	switch c := b[i]; {
	case c == '"' && i%2 == 1, c == '"' && i == 0, c == '\'':
		return i + 1
	case c == '"':
		// An even run of backslashes is that many escaped backslashes: this '"' is unescaped,
		// the end of the JSON string around the text, not a quote in it.
		return 0
	case i > 0 && (c == 'u' || c == 'U'):
		for _, hex := range []string{"u0022", "u0027"} {
			k := 0
			for k < len(hex) && i+k < len(b) && b[i+k]|0x20 == hex[k]|0x20 {
				k++
			}
			if k == len(hex) {
				return i + k
			}
			if i+k == len(b) {
				return -2
			}
		}
	}
	return 0
}

// bearerGlue: after "Bearer", spaces (at least one) and any escape codes, up to the value.
func bearerGlue(b []byte) (n int, scheme bool) {
	i := skipGap(b, 0)
	switch {
	case i < 0:
		return i, false
	case i >= len(b):
		return -2, false
	case !hasSpace(b[:i]):
		return -1, false
	}
	return i, true
}

// maxGap bounds the spaces and escape codes skipped between a scheme and its value.
const maxGap = 64

// skipGap skips spaces and terminal escape codes from b[i]: the index after them, -2 when b
// ends inside an escape code, or -1 when the gap runs past maxGap.
func skipGap(b []byte, i int) int {
	start := i
	for i < len(b) {
		if i-start > maxGap {
			return -1
		}
		if b[i] == ' ' {
			i++
			continue
		}
		n := escLen(b[i:])
		if n == -2 {
			return -2
		}
		if n == 0 {
			break
		}
		i += n
	}
	return i
}

func hasSpace(b []byte) bool {
	for k := 0; k < len(b); k++ {
		if b[k] == ' ' {
			return true
		}
		if n := escLen(b[k:]); n > 0 {
			k += n - 1
		}
	}
	return false
}

// escLen is the length of the terminal escape code (a CSI: ESC '[' parameters intermediates
// final) at the start of b, raw or as JSON writes it ("\u001b[..."); 0 when b doesn't start
// one, -2 when b ends inside one.
func escLen(b []byte) int {
	i := 0
	switch {
	case len(b) > 0 && b[0] == 0x1b:
		i = 1
	case len(b) > 0 && b[0] == '\\':
		const esc = "\\u001b"
		for k := 1; k < len(esc); k++ {
			if k >= len(b) {
				return -2
			}
			if c := b[k] | 0x20; c != esc[k]|0x20 {
				return 0
			}
		}
		i = len(esc)
	default:
		return 0
	}
	if i >= len(b) {
		return -2
	}
	if b[i] != '[' {
		return 0
	}
	for i++; i < len(b) && i < maxGap; i++ {
		c := b[i]
		if c >= 0x40 && c <= 0x7e {
			return i + 1
		}
		if c < 0x20 || c > 0x3f {
			return 0
		}
	}
	if i >= len(b) {
		return -2
	}
	return 0
}

var tokenRules = []tokenRule{
	{prefixes: []string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_"}, class: isAlnum, min: 36, max: 255, notAfter: isWord},
	{prefixes: []string{"github_pat_"}, class: isWord, min: 22, max: 255, notAfter: isWord},
	{prefixes: []string{"sk-"}, class: isB64URL, min: 20, max: 255, notAfter: isB64URL},
	{prefixes: []string{"AKIA", "ASIA"}, class: isUpperDigit, min: 16, max: 16, exact: true, notAfter: isAlnum},
	{prefixes: []string{"xoxa-", "xoxb-", "xoxp-", "xoxr-", "xoxs-"}, class: isAlnumDash, min: 10, max: 255, notAfter: isWord},
	{prefixes: []string{"Bearer", "bearer", "BEARER"}, glue: bearerGlue, class: isCredValue, min: 20, max: 512, keep: true, notAfter: isWord},
	{prefixes: []string{"Authorization", "authorization", "AUTHORIZATION"}, glue: authGlue, class: isCredValue, min: 8, max: 512, keep: true, notAfter: isWord, params: true},
	{prefixes: secretNamePrefixes, glue: secretGlue, class: isSecretValue, min: 1, max: 512, keep: true, notAfter: isAlnum, bare: secretBare, toQuote: true},
	// A URL with a password, scheme://user:password@host (#3756): postgres, mysql, redis, mongodb(+srv),
	// amqp, https, git+https and the rest. The scheme, the user and the host stay; the password goes.
	{prefixes: []string{"://"}, glue: urlUserGlue, class: urlRunByte, min: 1, max: maxURLRun, keep: true, notAfter: never, value: urlPassword},
}

// Secret-named values (#3733, Security on #3731): "password": "...", api_key=..., TOKEN=... in
// text the structural JSON walk doesn't see (streamed replies, text replies, JSON too deep to
// walk). The name is kept and its value redacted. A name may follow '_' or '-' (DB_PASSWORD,
// x-auth-token, access_token), never a letter or digit ("mytoken"), and must be followed by its
// separator, so "tokens", "secretary" and "author" are words. The structural walk redacts by
// key exactly (isSecretKey); this is the text form of the same list.
var secretNames = []string{"password", "passwd", "passphrase", "secret", "secret_key", "secret_access_key",
	"access_key", "token", "auth", "api_key", "apikey", "private_key", "credential", "credentials"}

// Limits, on purpose (Security on #3733): in text, an unquoted letters-only value after ':'
// ("password: correcthorse") is kept, since prose reads the same way; quote it, or use
// KEY=value, and it is redacted. Credentials inside a URL (mysql://user:pw@host) are not a rule
// of their own. In a JSON reply held whole the key decides, so neither limit applies there.
//
// The text rule looks for every name but "auth": that starts "Authorization", so a stream cut
// inside that word would end at "Auth", the rule would fail there and the Authorization rule
// would lose its start; and in text, auth is the Authorization rule's case.

// secretNamePrefixes: each name lower case, upper case and capitalised, the two-word ones also
// with '-' and in camel case, and the camel-case compounds a '_' wouldn't separate
// (accessToken, refreshToken, clientSecret).
var secretNamePrefixes = func() []string {
	var out []string
	add := func(s ...string) { out = append(out, s...) }
	for _, n := range secretNames {
		if n == "auth" {
			continue // see secretNames
		}
		add(n, strings.ToUpper(n), strings.ToUpper(n[:1])+n[1:])
		if parts := strings.Split(n, "_"); len(parts) > 1 {
			title := func(s string) string { return strings.ToUpper(s[:1]) + s[1:] }
			titled := make([]string, len(parts))
			for i, w := range parts {
				titled[i] = title(w)
			}
			dashed := strings.Join(parts, "-")
			camel := parts[0] + strings.Join(titled[1:], "")
			add(dashed, strings.ToUpper(dashed), strings.Join(titled, "-"), strings.Join(titled, "_"), camel, title(camel))
		}
	}
	for _, c := range []string{"accessToken", "refreshToken", "clientSecret"} {
		add(c, strings.ToUpper(c[:1])+c[1:])
	}
	return out
}()

// secretGlue measures what follows a secret name up to its value: an optional closing quote,
// spaces, ':' or '=', spaces and an optional opening quote (quoteLen: raw, or as JSON writes
// it). certain (glue's "scheme") is true when the value is quoted, or follows '=' with no space
// around it (KEY=value): then any value is redacted; otherwise only one that looks like a
// credential (secretBare). -1: not a secret value; -2: more bytes are needed to tell.
func secretGlue(b []byte) (n int, certain bool) {
	i := 0
	if q := quoteLen(b); q < 0 {
		return q, false
	} else {
		i += q
	}
	sep := i
	if sep = skipGap(b, i); sep < 0 {
		return sep, false
	}
	if sep >= len(b) {
		return -2, false
	}
	if b[sep] != ':' && b[sep] != '=' {
		return -1, false
	}
	v := skipGap(b, sep+1)
	if v < 0 {
		return v, false
	}
	q := quoteLen(b[min(v, len(b)):])
	if q < 0 || v+q >= len(b) {
		return -2, false
	}
	tight := b[sep] == '=' && sep == i && v == sep+1
	return v + q, q > 0 || tight
}

// seekSecretName finds the first secret name at or after from: one pass for the separators
// (':' and '='), and at each a look back over a closing quote and spaces to the word before it,
// which must end in a name (checked first by its last letter).
// A word at the very end may still get its separator: it is returned too (match then holds it).
func seekSecretName(data []byte, from int) int {
	next := func(c byte, at int) int {
		if k := bytes.IndexByte(data[at:], c); k >= 0 {
			return at + k
		}
		return len(data)
	}
	colon, eq := next(':', from), next('=', from)
	for {
		j := min(colon, eq)
		if j >= len(data) {
			break
		}
		if st := secretNameBefore(data, j, from); st >= 0 {
			return st
		}
		if j == colon {
			colon = next(':', j+1)
		} else {
			eq = next('=', j+1)
		}
	}
	// At the end, a closing quote may be half written (a \ of \", or \u002 of \u0022): the word
	// before it is held until the rest arrives.
	k := len(data)
	for k > from && len(data)-k < maxGap+maxQuoteEscape+6 && strings.IndexByte(" \"'\\u027", data[k-1]) >= 0 {
		k--
	}
	for ; k <= len(data); k++ {
		if st := secretNameBefore(data, k, from); st >= 0 {
			return st
		}
	}
	return -1
}

// secretNameBefore is where a secret name starts that ends right before data[j] (past a closing
// quote, however written, and spaces), at or after from; -1 when there is none.
func secretNameBefore(data []byte, j, from int) int {
	k := j
	for k > from && j-k <= maxGap && data[k-1] == ' ' {
		k--
	}
	if k-6 >= from && data[k-6] == '\\' && (data[k-5]|0x20) == 'u' && string(data[k-4:k-2]) == "00" && (data[k-2] == '2') && (data[k-1] == '2' || data[k-1] == '7') {
		k -= 6 // \u0022, \u0027
	} else if k > from && (data[k-1] == '"' || data[k-1] == '\'') {
		k--
		for k > from && j-k <= maxQuoteEscape+maxGap && data[k-1] == '\\' {
			k--
		}
	}
	end := k
	if end <= from {
		return -1
	}
	if !secretNameLast[data[end-1]] {
		return -1
	}
	// The longest name first: its start is the earliest. The byte before the name is checked by
	// starts.next (boundaryOK), which also sees the bytes before data and a JSON escape there.
	for _, n := range secretNameLens {
		if st := end - n; st >= from && secretNameFirst[n][data[st]] && secretNameSet[string(data[st:end])] {
			return st
		}
	}
	return -1
}

// secretRule is the secret names' rule; secretNameStarts every proper beginning of a name (a
// stream ending in one may be cut inside the name).
var secretRule = &tokenRules[7]

// urlCredRule is the URL credentials rule (#3756), named so a rule added later can't move it.
var urlCredRule = &tokenRules[8]

var secretNameStarts = func() map[string]bool {
	out := map[string]bool{}
	for _, p := range secretNamePrefixes {
		for k := 1; k < len(p); k++ {
			out[p[:k]] = true
		}
	}
	return out
}()

// secretNameLast marks the bytes a name ends with; secretNameFirst[n] those a name n long starts
// with: two table reads rule out most words before the set is hashed.
var secretNameLast, secretNameFirst = func() (last [256]bool, first [][256]bool) {
	for _, p := range secretNamePrefixes {
		last[p[len(p)-1]] = true
		for len(first) <= len(p) {
			first = append(first, [256]bool{})
		}
		first[len(p)][p[0]] = true
	}
	return last, first
}()

// secretNameSet holds secretNamePrefixes; secretNameLens their lengths, longest first.
var secretNameSet, secretNameLens = func() (map[string]bool, []int) {
	set, seen := map[string]bool{}, map[int]bool{}
	var lens []int
	for _, p := range secretNamePrefixes {
		set[p] = true
		if !seen[len(p)] {
			seen[len(p)] = true
			lens = append(lens, len(p))
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(lens)))
	return set, lens
}()

// isSecretValue: a secret value's characters, up to a space, a quote, a backslash (JSON's
// escape: the string's end, or a quote written \") or a delimiter of the text around it.
func isSecretValue(c byte) bool {
	return c > ' ' && c < 0x7f && strings.IndexByte("\"'\\&,;<>(){}[]", c) < 0
}

// secretBare: an unquoted value after ':' is a credential only if it mixes letters with digits
// or symbols: a JSON number, true, false or null, and prose ("token: expired") are left.
func secretBare(v []byte) bool {
	letter := false
	for _, c := range v {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' {
			letter = true
			break
		}
	}
	return letter && mixedValue(v)
}

func never(byte) bool { return false }

// maxURLUser bounds a URL's user name, and maxURLRun its password and host together: past them
// the text is not taken for a URL's credentials.
const (
	maxURLUser = 256
	maxURLRun  = 1024
)

// urlUserByte: a character of a URL's user name (not ':', which ends it).
func urlUserByte(c byte) bool {
	return c > ' ' && c < 0x7f && strings.IndexByte("@/?#\\\"'<>[]{}|`:", c) < 0
}

// urlRunByte: a character of what follows the user name's ':' up to the next space or quote: the
// password ('@', ':' and even a raw '/', '?' or '#' included, Security on #3756) and the host.
// Printable ASCII only: a URL percent-encodes anything else, and taking any other byte would make
// a run of non-ASCII prose one token.
func urlRunByte(c byte) bool {
	return c > ' ' && c < 0x7f && strings.IndexByte("\"'<>`", c) < 0
}

// urlUserGlue measures, after "://", the user name and the ':' after it (redis://:pw@host has
// none). -1: not a password here (an '@', a '/', a space or a quote comes first); -2: b ends first.
func urlUserGlue(b []byte) (int, bool) {
	for i := 0; i < len(b) && i <= maxURLUser; i++ {
		switch c := b[i]; {
		case c == ':':
			return i + 1, false
		case !urlUserByte(c):
			return -1, false
		}
	}
	if len(b) <= maxURLUser {
		return -2, false
	}
	return -1, false
}

// urlPassword measures a URL's password: up to the last '@' before the next space, quote or
// "://" (so "p@ss@host" takes "p@ss", and "ab/cd@host" takes "ab/cd"); none without an '@'. The
// run ends at the next "://", where another URL starts: each URL's scan then covers only the text
// up to the next, so a line of them costs linear time (Security on #3756 v2: "://" repeated over
// 4 MiB took 4 s). Digits that end at a '/', '?', '#' or the run's end are a port
// (https://host:8080/path, https://h:80/x/@user).
func urlPassword(b []byte, final bool) (n int, need bool) {
	last := -1
	i := 0
	for ; i < len(b) && i < maxURLRun && urlRunByte(b[i]); i++ {
		if b[i] == ':' && i+2 < len(b) && b[i+1] == '/' && b[i+2] == '/' {
			break
		}
		if b[i] == '@' {
			last = i
		}
	}
	if i == len(b) && i < maxURLRun && !final {
		return 0, true // the run may go on in the next chunk
	}
	d := 0
	for d < i && b[d] >= '0' && b[d] <= '9' {
		d++
	}
	if d > 0 && (d == i || b[d] == '/' || b[d] == '?' || b[d] == '#') {
		return 0, false // a port
	}
	return max(last, 0), false
}

// PEM private-key blocks: "-----BEGIN <LABEL>PRIVATE KEY-----" ... "-----END <LABEL>-----".
var (
	pemBegin  = []byte("-----BEGIN ")
	pemKeyEnd = []byte("PRIVATE KEY")
	pemTails  = [][]byte{[]byte("-----"), []byte(" BLOCK-----")} // "...PRIVATE KEY-----", PGP's "...PRIVATE KEY BLOCK-----"
	pemEnd    = []byte("-----END ")
	pemDashes = []byte("-----")
)

const maxPEMLabel = 64

// Redactor redacts what is written to it and passes the result on to its destination.
type Redactor struct {
	dst     io.Writer
	pending []byte          // held back: it could still start or continue a token
	prev    []byte          // the last few bytes of the original stream before pending (for the boundary)
	inPEM   bool            // inside a private key: dropped until its END line or the closing quote
	pemEsc  bool            // inside a private key: the last byte dropped was an unpaired backslash
	inToken func(byte) bool // inside a token longer than its rule's ceiling: its class is dropped
	shown   int             // the leading bytes of pending that Flush already emitted as they are
	err     error
	buf     []byte // pending + the new bytes (reused)
	out     []byte // the redacted output of one Write (reused; dst must not keep it)
	st      starts
}

// NewRedactor returns a Redactor writing the redacted stream to dst. Close flushes it.
func NewRedactor(dst io.Writer) *Redactor { return &Redactor{dst: dst} }

// Write redacts p (after what was held back) and emits everything that is settled.
func (r *Redactor) Write(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	data := p
	if len(r.pending) > 0 {
		r.buf = append(append(r.buf[:0], r.pending...), p...)
		data = r.buf
	}
	out, consumed := r.process(data, false)
	if consumed > 0 {
		r.prev = append(r.prev, data[max(0, consumed-escapeContext):consumed]...)
		r.prev = r.prev[:copy(r.prev, r.prev[max(0, len(r.prev)-escapeContext):])]
	}
	r.pending = append(r.pending[:0], data[consumed:]...)
	out = r.skipShown(out, data, consumed)
	if len(out) > 0 {
		if _, err := r.dst.Write(out); err != nil {
			r.err = err
			return 0, err
		}
	}
	return len(p), nil
}

// skipShown drops from out what Flush already emitted: the leading bytes of data, shown as
// they were. Where a token turned out to start inside them, out differs from data from there
// on, and the viewer, having seen its first characters, gets the marker after them.
func (r *Redactor) skipShown(out, data []byte, consumed int) []byte {
	if r.shown == 0 {
		return out
	}
	k := 0
	for k < r.shown && k < len(out) && out[k] == data[k] {
		k++
	}
	r.shown = max(0, r.shown-consumed)
	return out[k:]
}

// Flush settles the held tail of a stream that has gone quiet, without ending it (#3562: a
// terminal must not sit on its last characters). No character of a token's value is ever
// shown:
//   - A tail with no value character in it yet (the first letters of a prefix, a whole prefix,
//     "Authorization: Bearer" before its value, a key's BEGIN line still arriving) is shown as
//     it is but kept as context, so a token it turns out to start is still caught: the viewer
//     sees those words, then the marker.
//   - A tail holding a prefix and the start of its value is held while force is false. With
//     force, its value is replaced by the marker now, whatever its length (the prefix and
//     scheme stay when the rule keeps them), and the rest of the run is dropped if more
//     arrives.
//   - Inside a private key nothing is shown, so Flush waits for its END line.
func (r *Redactor) Flush(force bool) error {
	if r.err != nil {
		return r.err
	}
	if r.inPEM || len(r.pending) <= r.shown {
		return nil
	}
	var out []byte
	if rule, valueAt := heldValue(r.pending); rule != nil {
		if !force {
			return nil
		}
		keep := 0
		if rule.keep {
			keep = valueAt
		}
		out = append(r.out[:0], r.pending[min(r.shown, keep):keep]...)
		if r.shown > keep {
			out = out[:0] // a token began inside what was shown: the marker follows it
		}
		out = append(out, RedactMarker...)
		r.inToken = rule.class
		if rule.toQuote && openingQuote(r.pending, valueAt) != nil {
			r.inToken = notQuoteEnd // a quoted value's rest runs to its quote, spaces and all
		}
		r.settleAll()
	} else {
		// No value in it: it redacts to itself (checked, so a tail this misjudged is settled
		// redacted instead of shown).
		out, _ = r.process(r.pending, true)
		if r.inToken == nil && !r.inPEM && bytes.Equal(out, r.pending) {
			out = out[r.shown:]
			r.shown = len(r.pending)
		} else {
			out = r.skipShown(out, r.pending, len(r.pending))
			r.settleAll()
		}
	}
	if len(out) > 0 {
		if _, err := r.dst.Write(out); err != nil {
			r.err = err
			return err
		}
	}
	return nil
}

// settleAll counts everything held back as consumed (Flush).
func (r *Redactor) settleAll() {
	r.prev = append(r.prev, r.pending[max(0, len(r.pending)-escapeContext):]...)
	r.prev = r.prev[:copy(r.prev, r.prev[max(0, len(r.prev)-escapeContext):])]
	r.pending, r.shown = r.pending[:0], 0
}

// heldValue: when a held tail starts with a rule's whole prefix (and glue) and at least one
// character of its value, the rule and where the value starts; else nil (no value held yet).
func heldValue(b []byte) (*tokenRule, int) {
	for k := range tokenRules {
		ru := &tokenRules[k]
		at := ru.prefixLen(b)
		if at == 0 {
			continue
		}
		if ru.glue != nil {
			g, _ := ru.glue(b[at:])
			if g < 0 {
				return nil, 0 // the glue is still arriving (or isn't one): no value yet
			}
			at += g
		}
		if at < len(b) && (ru.class(b[at]) || ru.toQuote && openingQuote(b, at) != nil) {
			return ru, at // a quoted value's first character may be a space
		}
		return nil, 0
	}
	return nil, 0
}

// Close redacts and emits what was held back; a private key still open is dropped.
func (r *Redactor) Close() error {
	if r.err != nil {
		return r.err
	}
	out, _ := r.process(r.pending, true)
	out = r.skipShown(out, r.pending, len(r.pending))
	r.pending = nil
	if len(out) > 0 {
		if _, err := r.dst.Write(out); err != nil {
			r.err = err
			return err
		}
	}
	return nil
}

// RedactTokens redacts a whole body.
func RedactTokens(b []byte) []byte {
	var out bytes.Buffer
	r := NewRedactor(&out)
	_, _ = r.Write(b)
	_ = r.Close()
	return out.Bytes()
}

// process redacts data and returns the output and how many bytes of data it settled; the rest
// is held back. With final set, all of data is settled.
func (r *Redactor) process(data []byte, final bool) (out []byte, consumed int) {
	out = r.out[:0]
	defer func() { r.out = out[:0] }()
	st := &r.st
	st.reset(data, r.prev)
	i := 0
	for i < len(data) {
		if r.inToken != nil {
			// The rest of an over-long token: dropped up to the first byte outside its class.
			j := i
			for j < len(data) && r.inToken(data[j]) {
				j++
			}
			if j == len(data) {
				if final {
					r.inToken = nil
				}
				return out, len(data)
			}
			r.inToken = nil
			i = j
			continue
		}
		if r.inPEM {
			end, found, hold, esc := pemClose(data[i:], final, r.pemEsc)
			if !found {
				if final {
					return out, len(data)
				}
				r.pemEsc = esc
				return out, len(data) - hold // the key so far is dropped; a possible END start is held
			}
			r.inPEM, r.pemEsc = false, false
			i += end
			continue
		}
		start, rule := st.next(i)
		if start < 0 {
			if final {
				return append(out, data[i:]...), len(data)
			}
			cut := len(data) - st.partialTail(i)
			return append(out, data[i:cut]...), cut
		}
		out = append(out, data[i:start]...)
		if rule == nil { // a PEM BEGIN
			n, ok, need := pemLabel(data[start:])
			switch {
			case need && !final:
				return out, start
			case !ok:
				out = append(out, data[start])
				i = start + 1
			default:
				out = append(out, RedactMarker...)
				r.inPEM, r.pemEsc = true, false
				i = start + n
			}
			continue
		}
		n, valueAt, ok, need, long := rule.match(data[start:], final)
		switch {
		case need && !final:
			return out, start
		case !ok:
			out = append(out, data[start])
			i = start + 1
		default:
			// A value that holds another rule's start ends before it, so that rule redacts what
			// follows: in "token:  Authorization: Basic ..." or "token: DB_PASSWORD=\"...\"" the
			// keyword was taken as token's value and redacted, and the credential after it went
			// out (#3761, found by FuzzRedactStream). A value that would be shorter than the
			// rule's least (or empty) is not this rule's value at all.
			// Only a start whose own rule matches there counts (near the end of the data a start
			// is only possible); when that can't be told yet, the whole match waits for more.
			cut, wait := -1, false
			for k, kr := st.next(start + valueAt); k >= 0 && k < start+n; k, kr = st.next(k + 1) {
				if kr == nil { // a PEM BEGIN
					cut = k
					break
				}
				_, _, kok, kneed, _ := kr.match(data[k:], final)
				if kneed && !final {
					wait = true
					break
				}
				if kok {
					cut = k
					break
				}
			}
			if wait {
				return out, start
			}
			if cut >= 0 {
				if cut-(start+valueAt) < max(rule.min, 1) {
					out = append(out, data[start])
					i = start + 1
					continue
				}
				n, long = cut-start, false
			}
			if rule.keep {
				out = append(out, data[start:start+valueAt]...)
			}
			out = append(out, RedactMarker...)
			i = start + n
			if long {
				r.inToken = rule.class // the rest of the run is dropped as it arrives
			}
		}
	}
	return out, len(data)
}

// starts finds, in order, where in data a rule's prefix or a PEM BEGIN starts with a valid
// boundary before it. Each search is for a short anchor that several prefixes share (or a
// distinctive part of one), checked against the full prefixes where it's found, and the next
// occurrence of each anchor is remembered, so a reply costs about ten passes in all however
// many near misses it holds.
type starts struct {
	data []byte
	prev []byte // the bytes just before data[0] (empty: the start of the stream)
	lits []startLit
}

// escapeContext is how many bytes before a token the boundary rule looks at: a \uXXXX
// escape, or a terminal escape code ending right before it (a 24-bit colour is ~20 bytes).
const escapeContext = 40

type startLit struct {
	anchor []byte
	back   int        // the anchor's offset inside the prefix
	full   [][]byte   // the prefixes it stands for, checked at the start
	rule   *tokenRule // nil: a PEM BEGIN
	at     int        // the next occurrence's start, -1 none
	looked bool
	// seek, when set, finds the next start itself (anchor and full unused): the secret names,
	// found from their separator in one pass rather than one search per spelling (#3733).
	seek func(data []byte, from int) int
}

// startAnchors pairs each anchor with the rule whose prefixes it finds.
var startAnchors = func() []startLit {
	var out []startLit
	add := func(anchor string, back int, rule *tokenRule) {
		l := startLit{anchor: []byte(anchor), back: back, rule: rule}
		if rule == nil {
			l.full = [][]byte{pemBegin}
		} else {
			for _, pre := range rule.prefixes {
				if back+len(anchor) <= len(pre) && pre[back:back+len(anchor)] == anchor {
					l.full = append(l.full, []byte(pre))
				}
			}
		}
		out = append(out, l)
	}
	add("gh", 0, &tokenRules[0])
	add("_pat_", 6, &tokenRules[1])
	add("sk-", 0, &tokenRules[2])
	add("AKIA", 0, &tokenRules[3])
	add("ASIA", 0, &tokenRules[3])
	add("xox", 0, &tokenRules[4])
	add("earer", 1, &tokenRules[5])
	add("EARER", 1, &tokenRules[5])
	add("uthorization", 1, &tokenRules[6])
	add("UTHORIZATION", 1, &tokenRules[6])
	// Secret names (#3733), after the rules above so Authorization wins a start they share.
	out = append(out, startLit{rule: secretRule, seek: seekSecretName})
	add("://", 0, urlCredRule)
	add("-----BEGIN ", 0, nil)
	return out
}()

func (s *starts) reset(data, prev []byte) {
	s.data, s.prev = data, prev
	if s.lits == nil {
		s.lits = append([]startLit(nil), startAnchors...)
	}
	for k := range s.lits {
		s.lits[k].looked, s.lits[k].at = false, -1
	}
}

// find sets l.at to the first start at or after from whose full prefix matches there.
func (s *starts) find(l *startLit, from int) {
	l.looked = true
	if l.seek != nil {
		l.at = l.seek(s.data, from)
		return
	}
	for search := from + l.back; search <= len(s.data); {
		j := bytes.Index(s.data[search:], l.anchor)
		if j < 0 {
			break
		}
		at := search + j - l.back
		for _, pre := range l.full {
			if bytes.HasPrefix(s.data[at:], pre) {
				l.at = at
				return
			}
		}
		search += j + 1
	}
	l.at = -1
}

// next returns the first valid start at or after i (-1: none) and its rule (nil: a PEM BEGIN).
func (s *starts) next(i int) (int, *tokenRule) {
	best := -1
	var bestRule *tokenRule
	for k := range s.lits {
		l := &s.lits[k]
		if !l.looked || (l.at >= 0 && l.at < i) {
			s.find(l, i)
		}
		for l.at >= 0 && l.rule != nil && !s.boundaryOK(l.at, l.rule) {
			s.find(l, l.at+1)
		}
		if l.at >= 0 && (best < 0 || l.at < best) {
			best, bestRule = l.at, l.rule
		}
	}
	return best, bestRule
}

// byteBefore is the byte k places before data[at], reaching into the bytes before data;
// ok is false before the start of the stream.
func (s *starts) byteBefore(at, k int) (byte, bool) {
	if i := at - k; i >= 0 {
		return s.data[i], true
	} else if j := len(s.prev) + i; j >= 0 {
		return s.prev[j], true
	}
	return 0, false
}

// boundaryOK: the prefix at data[at] doesn't continue a word. A JSON escape right before it
// ("\n", "\t", "\"", "\u003e" and the like) counts as a boundary, since in the text it stands
// for a separator.
func (s *starts) boundaryOK(at int, ru *tokenRule) bool {
	before, ok := s.byteBefore(at, 1)
	if !ok || !ru.notAfter(before) {
		return true
	}
	if bs, ok := s.byteBefore(at, 2); ok && bs == '\\' && strings.IndexByte("nrtbf", before) >= 0 {
		return true
	}
	if bs, ok := s.byteBefore(at, 6); ok && bs == '\\' {
		if u, _ := s.byteBefore(at, 5); u == 'u' {
			hex := true
			for k := 4; k >= 1; k-- {
				c, _ := s.byteBefore(at, k)
				hex = hex && (c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F')
			}
			return hex
		}
	}
	return s.escapeEndsBefore(at)
}

// escapeEndsBefore: the bytes right before data[at] end a terminal escape code (a CSI: ESC '['
// parameters intermediates final, raw or as JSON writes it, "\u001b[1;32m"), so in the text
// a token starting there starts a word (#3562: coloured output is the normal case).
func (s *starts) escapeEndsBefore(at int) bool {
	c, ok := s.byteBefore(at, 1)
	if !ok || c < 0x40 || c > 0x7e {
		return false
	}
	k := 2
	for ; k <= escapeContext; k++ {
		if c, ok = s.byteBefore(at, k); !ok || c < 0x20 || c > 0x3f {
			break
		}
	}
	if !ok || c != '[' {
		return false
	}
	if e, _ := s.byteBefore(at, k+1); e == 0x1b {
		return true
	}
	const esc = "\\u001b"
	for j := 0; j < len(esc); j++ {
		e, ok := s.byteBefore(at, k+len(esc)-j)
		if !ok || e|0x20 != esc[j]|0x20 {
			return false
		}
	}
	return true
}

// match reports the token at b, which starts with one of the rule's prefixes: its length so far,
// where its value starts, whether it is a token, whether more bytes are needed to tell (never
// when final: the body ends there), and whether it runs past the rule's ceiling (it is then a
// token whose rest is dropped as it arrives; the ceiling only bounds what is held back).
func (ru *tokenRule) match(b []byte, final bool) (n, valueAt int, ok, need, long bool) {
	at := ru.prefixLen(b)
	scheme := true
	if ru.glue != nil {
		g, sch := ru.glue(b[at:])
		switch {
		case g == -2:
			return 0, 0, false, !final, false
		case g < 0:
			return 0, 0, false, false, false
		}
		at, scheme = at+g, sch
	}
	if ru.toQuote {
		if form := openingQuote(b, at); form != nil {
			return quotedValue(b, at, form, ru.max, final)
		}
	}
	if ru.params {
		// Digest (and any scheme with parameters): username="bob", nonce="...", response="..."
		// is one credential, redacted to the list's end (#3731).
		switch n, k := paramList(b[at:], final); {
		case n == -2:
			return 0, 0, false, true, false
		case n > 0 && k >= 2 || n > 0 && bytes.IndexByte(b[at:at+n], '"') >= 0:
			return at + n, at, true, false, false
		}
	}
	if ru.value != nil {
		n, need := ru.value(b[at:], final)
		switch {
		case need:
			return 0, 0, false, true, false
		case n < ru.min:
			return 0, 0, false, false, false
		}
		return at + n, at, true, false, false
	}
	run := 0
	for at+run < len(b) && ru.class(b[at+run]) {
		run++
		if run > ru.max {
			if ru.exact {
				return 0, 0, false, false, false // longer than the fixed length: not this kind
			}
			if !scheme && !ru.bareOK(b[at:at+run]) {
				return 0, 0, false, false, false
			}
			return at + run, at, true, false, true
		}
	}
	if at+run == len(b) && !final {
		return 0, 0, false, true, false // the value may go on in the next chunk
	}
	if run < ru.min || (ru.exact && run != ru.min) {
		return 0, 0, false, false, false
	}
	if !scheme && !ru.bareOK(b[at:at+run]) {
		// "Authorization: unavailable": a bare value of letters only is a word, not a credential.
		return 0, 0, false, false, false
	}
	return at + run, at, true, false, false
}

// notQuoteEnd: inside a quoted value whose start was flushed as the marker, every byte is
// dropped up to a quote (or the backslash before an escaped one).
func notQuoteEnd(c byte) bool { return c != '"' && c != '\'' && c != '\\' }

// openingQuote is the quote (however written: quoteLen) that ends right at b[at], or nil. The
// longest spelling wins: \" is one quote, not a backslash and a raw ".
func openingQuote(b []byte, at int) []byte {
	for s := max(0, at-maxQuoteEscape-6); s < at; s++ {
		if q := quoteLen(b[s:]); q > 0 && s+q == at {
			return b[s:at]
		}
	}
	return nil
}

// quotedValue measures a value that starts after its opening quote (form) at b[at]: it runs to
// its closing quote (the same spelling), so PASSWORD="my pass phrase" is redacted whole, or to the
// end of the JSON string around the text if that comes first (quotedEnd, the #3731 rules), never
// past it. A value with no end within max is redacted to there and the rest of its run dropped
// (long); one that may still end in the next bytes is held (need).
func quotedValue(b []byte, at int, form []byte, max int, final bool) (n, valueAt int, ok, need, long bool) {
	if len(form) == 1 && form[0] == '"' {
		// A raw '"' followed by '}', ',', ']', ':' or the end closes the JSON string around the
		// text ({"content":"token ="}): it opens no value.
		if at == len(b) {
			return 0, 0, false, !final, false
		}
		if strings.IndexByte("},]:", b[at]) >= 0 {
			return 0, 0, false, false, false
		}
	}
	rest := b[at:]
	if len(rest) > max {
		rest = rest[:max]
	}
	end, stop := quotedEnd(form, rest, final && len(b)-at <= max)
	switch {
	case end >= 0:
		v := end - len(form) // the value, up to its closing quote
		if v == 0 {
			return 0, 0, false, false, false
		}
		return at + v, at, true, false, false
	case stop >= 0:
		if stop == 0 {
			return 0, 0, false, false, false
		}
		return at + stop, at, true, false, false
	case stop == -2 || len(b)-at <= max && !final:
		return 0, 0, false, true, false
	case len(b)-at > max:
		return at + max, at, true, false, true
	}
	if len(b) == at {
		return 0, 0, false, false, false
	}
	return len(b), at, true, false, false // the body ends inside the value: all of it (fail closed)
}

// maxParamList bounds an auth-param list (a Digest header is about 300 bytes); one that runs on
// past it is redacted to there, and the rest of its run dropped.
const maxParamList = 1024

// paramList measures the auth-param list at b: key=value pairs separated by commas (and
// spaces), a value a run of credential characters or a quoted string, its quote written however
// the text writes it (quoteLen: raw, \" in JSON, \\\" in JSON inside JSON), and closed by the same
// form. It returns the list's length and how many pairs it holds (0, 0: not a list), or -2 when b
// ends before the list does (or before maxParamList, when it has no end in sight). With final
// set, b is the whole body: one that ends inside a pair has the list run to its end (fail
// closed), and a word with no '=' after it is not a pair.
func paramList(b []byte, final bool) (n, pairs int) {
	i := 0
	// short: b ended. inPair: inside a pair (after its '='); otherwise before a key.
	short := func(inPair bool) (int, int) {
		switch {
		case !final:
			return -2, 0
		case inPair:
			return len(b), pairs + 1
		}
		return i, pairs
	}
	for {
		k := i
		for k < len(b) && (isWord(b[k]) || b[k] == '-') {
			k++
		}
		if k == len(b) {
			if k >= maxParamList {
				return maxParamList, pairs + 1
			}
			return short(false)
		}
		if k == i || b[k] != '=' {
			return i, pairs
		}
		k++
		q := quoteLen(b[k:])
		switch {
		case q == -2:
			return short(true)
		case q == 1 && b[k] == '"' && k+1 == len(b) && !final:
			return -2, 0 // the string's own end, or a value's opening quote: the next byte tells
		case q == 1 && b[k] == '"' && (k+1 == len(b) || strings.IndexByte("},]:", b[k+1]) >= 0):
			// An empty value: this quote closes the JSON string around the text (#3731,
			// Security: the list must never take it, or the reply stops being JSON).
			return k, pairs + 1
		case q > 0:
			end, stop := quotedEnd(b[k:k+q], b[k+q:], final)
			switch {
			case stop == -2:
				return -2, 0
			case stop >= 0:
				// The JSON string around the text ends inside the value: the list stops there and
				// never takes the closing quote (#3731, Security; as pemClose, #3694).
				return k + q + stop, pairs + 1
			case end < 0 && len(b) >= maxParamList:
				return maxParamList, pairs + 1
			case end < 0:
				return short(true)
			}
			k += q + end
		default:
			for k < len(b) && isCredValue(b[k]) {
				k++
			}
		}
		pairs++
		end := k
		if k > maxParamList {
			return maxParamList, pairs
		}
		for k < len(b) && b[k] == ' ' {
			k++
		}
		if k == len(b) {
			if final {
				return end, pairs
			}
			return -2, 0 // another pair may follow in the next chunk
		}
		if b[k] != ',' {
			return end, pairs
		}
		for k++; k < len(b) && b[k] == ' '; k++ {
		}
		if k == len(b) {
			if final {
				return end, pairs
			}
			return -2, 0
		}
		i = k
	}
}

// quotedEnd finds, in b (what follows a value's opening quote, written as form), the end of the
// value: the index just after its closing quote, which is the same quote after the same number
// of backslashes (end), or where the JSON string around the text ends first (stop): an
// unescaped '"' when the value's quote was escaped (the text is inside a JSON string), or, for a
// raw quote, a '"' of another kind that is followed by '}', ',', ']', ':' or the end. -1, -1:
// neither in b; stop -2: b ends right after such a '"' and only the next byte tells.
func quotedEnd(form, b []byte, final bool) (end, stop int) {
	r := len(form) - 1 // backslashes before the quote
	qc := form[r]
	if qc != '"' && qc != '\'' {
		// \u0022 or \u0027: the same spelling closes it; the string still ends at a '"'.
		e := bytes.Index(b, form)
		if u := unescapedQuote(b, false); u >= 0 && (e < 0 || u < e) {
			return -1, u
		}
		if e < 0 {
			return -1, -1
		}
		return e + len(form), -1
	}
	run := 0
	for j, c := range b {
		if c == '\\' {
			run++
			continue
		}
		switch {
		case c == qc && run == r:
			return j + 1, -1
		case c == '"' && run%2 == 0 && r > 0:
			return -1, j // escaped quotes inside, so this unescaped one ends the JSON string
		case c == '"' && run%2 == 0:
			if j+1 == len(b) {
				if !final {
					return -1, -2
				}
				return -1, j
			}
			if strings.IndexByte("},]:", b[j+1]) >= 0 {
				return -1, j
			}
		}
		run = 0
	}
	return -1, -1
}

// mixedValue: the value holds a digit or a symbol, as credentials do.
func mixedValue(v []byte) bool {
	for _, c := range v {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return true
		}
	}
	return false
}

// pemLabel checks for "-----BEGIN <LABEL>PRIVATE KEY-----" (or PGP's "...PRIVATE KEY BLOCK-----")
// at b and returns its length.
func pemLabel(b []byte) (n int, ok, need bool) {
	rest := b[len(pemBegin):]
	limit := min(len(rest), maxPEMLabel)
	if k := bytes.Index(rest[:limit], pemKeyEnd); k >= 0 {
		for _, c := range rest[:k] {
			if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == ' ') {
				return 0, false, false
			}
		}
		after := rest[k+len(pemKeyEnd):]
		for _, tail := range pemTails {
			if bytes.HasPrefix(after, tail) {
				return len(pemBegin) + k + len(pemKeyEnd) + len(tail), true, false
			}
		}
		for _, tail := range pemTails {
			if len(after) < len(tail) && bytes.HasPrefix(tail, after) {
				return 0, false, true // the label's end is still arriving
			}
		}
		return 0, false, false
	}
	if len(rest) >= maxPEMLabel {
		return 0, false, false
	}
	for _, c := range rest { // still arriving, unless it holds a character no label has
		if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == ' ' || c == '-') {
			return 0, false, false
		}
	}
	return 0, false, true
}

// pemClose finds where an open private key ends in b: after its "-----END <LABEL>-----"
// marker, or at (not after) the first unescaped '"', the end of the JSON string holding it.
// Inside a JSON string a quote is written \" (#3694): that '"' is part of the key's text, and
// stopping at it dropped the backslash and closed the string early, which left the rest of the
// string as bare JSON. esc says whether the byte dropped just before b was an unpaired
// backslash (a chunk boundary can fall between a backslash and what it escapes). When the end
// isn't found, hold is how many trailing bytes might begin the END marker, and escAfter is the
// escape state after the bytes consumed (all but hold).
func pemClose(b []byte, final, esc bool) (end int, found bool, hold int, escAfter bool) {
	q := unescapedQuote(b, esc)
	for e := bytes.Index(b, pemEnd); e >= 0 && (q < 0 || e < q); {
		rest := b[e+len(pemEnd):]
		if k := bytes.Index(rest[:min(len(rest), maxPEMLabel)], pemDashes); k >= 0 {
			return e + len(pemEnd) + k + len(pemDashes), true, 0, false
		}
		if len(rest) < maxPEMLabel && q < 0 && !final {
			return 0, false, len(b) - e, escapeAfter(b[:e], esc) // the END line may finish in the next chunk
		}
		next := bytes.Index(b[e+1:], pemEnd)
		if next < 0 {
			break
		}
		e += 1 + next
	}
	if q >= 0 {
		return q, true, 0, false
	}
	hold = partialSuffix(b, pemEnd)
	return 0, false, hold, escapeAfter(b[:len(b)-hold], esc)
}

// unescapedQuote is the index of the first '"' in b that no backslash escapes (a run of
// backslashes escapes the next byte when it is odd), counting esc as a backslash just before b;
// -1 if there is none.
func unescapedQuote(b []byte, esc bool) int {
	for k, c := range b {
		switch {
		case esc:
			esc = false
		case c == '\\':
			esc = true
		case c == '"':
			return k
		}
	}
	return -1
}

// escapeAfter is whether b, read after a backslash state esc, ends with an unpaired backslash.
func escapeAfter(b []byte, esc bool) bool {
	for _, c := range b {
		if esc {
			esc = false
		} else if c == '\\' {
			esc = true
		}
	}
	return esc
}

// partialTail is how many trailing bytes of data (from i on) might begin a prefix or a PEM
// BEGIN, and so are held back. A prefix's first letters are held only where its boundary
// holds (Security's #3562 SHOULD 1): letters in the middle of a word can't start a token, so
// they go out at once, which keeps a terminal's echo prompt.
func (s *starts) partialTail(i int) int {
	b := s.data[i:]
	n := partialSuffix(b, pemBegin)
	for k := range tokenRules {
		ru := &tokenRules[k]
		if ru == secretRule {
			// The secret names' beginnings are a set: a lookup per tail length, not a search
			// per spelling (#3733).
			for m := min(secretNameLens[0]-1, len(b)); m > n; m-- {
				if secretNameStarts[string(b[len(b)-m:])] && s.boundaryOK(len(s.data)-m, ru) {
					n = m
					break
				}
			}
			continue
		}
		for _, pre := range ru.prefixes {
			for m := min(len(pre)-1, len(b)); m > n; m-- {
				if bytes.HasSuffix(b, []byte(pre[:m])) && s.boundaryOK(len(s.data)-m, ru) {
					n = m
					break
				}
			}
		}
	}
	return n
}

// partialSuffix is the length of the longest proper prefix of pre that b ends with.
func partialSuffix(b, pre []byte) int {
	for k := min(len(pre)-1, len(b)); k > 0; k-- {
		if bytes.HasSuffix(b, pre[:k]) {
			return k
		}
	}
	return 0
}
