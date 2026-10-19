package redact

import (
	"bytes"
	"sort"
	"strings"
)

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
