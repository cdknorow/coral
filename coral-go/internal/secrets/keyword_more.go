package secrets

import (
	"regexp"
	"strings"

	"github.com/cdknorow/coral/pkg/redact"
)

// Round four of the hub's keyword rules (#5212): the neighbours of what keyword_rules.go takes, found by Security's review of #5172, and four misses coral-hub measured. They sit in their own file
// and are found by their own pass (moreKeywordSpans, run beside keywordSpans by withKeywordRules), each behind a cheap test of the text, so ordinary text pays for a handful of Contains calls and
// the keyword gate and rules are as they were.
//
//   - a copy of the text with the spellings a name or a quote can take written plainly: the HTML quote entities in their odd forms (&amp;quot;, &quot with no semicolon) as a quote, and an encoded
//     underscore or hyphen (api%5Fkey) as itself; the keyword rules run over the copy, and what they find is mapped back (the same way the invisible characters are, extra.go);
//   - a value in an XML element (<api_key>VALUE</api_key>) or an HTML form field (<input name="api_key" value="VALUE">, either order of the attributes);
//   - a URL-encoded JSON pair (%22api_key%22%3A%22VALUE%22), encoded once or twice;
//   - the cookies of curl -b NAME=VALUE (glued on or after a space, quoted or not) and the password of docker login -p VALUE.
func moreKeywordSpans(text string) []redact.Span {
	var out []redact.Span
	lower := ""
	low := func() string {
		if lower == "" {
			lower = lowerASCII(text)
		}
		return lower
	}
	if strings.Contains(text, "&") || strings.Contains(text, "%5") || strings.Contains(text, "%2") {
		if norm, at, to := plainSpellings(text); at != nil {
			for _, sp := range keywordSpans(norm) {
				if sp.Start >= 0 && sp.End <= len(at) && sp.Start < sp.End {
					out = append(out, redact.Span{Start: at[sp.Start], End: to[sp.End-1]})
				}
			}
		}
	}
	if strings.Contains(text, "</") || strings.Contains(text, "<input") || strings.Contains(low(), "<input") {
		out = append(out, xmlElementSpans(text)...)
		out = append(out, formFieldSpans(text)...)
	}
	if strings.Contains(text, "%22") || strings.Contains(text, "%2522") {
		out = append(out, encodedQuotedPairSpans(text)...)
	}
	if strings.Contains(text, "curl") {
		out = append(out, curlCookieSpans(text)...)
	}
	if strings.Contains(text, "docker") {
		out = append(out, dockerLoginSpans(text)...)
	}
	if strings.Contains(text, "npm") || strings.Contains(text, "yarn") || strings.Contains(text, "_auth") {
		out = append(out, npmConfigSpans(text)...)
	}
	if strings.Contains(low(), "pwd") {
		out = append(out, pwdSpans(text)...)
	}
	return out
}

var (
	// the quote entities in the forms people and logs write: &quot;, &quot (no semicolon, as browsers read it), &amp;quot; (escaped again, once or more), &apos;, &#34; &#39; &#x22; &#x27; (any zero padding)
	oddEntityQuoteRe = regexp.MustCompile(`(?i)&(?:amp;)*(?:quot;?|apos;|#0*34;|#0*39;|#x0*2[27];)`)
	// an encoded underscore or hyphen, in a name: api%5Fkey, x%2Dapi%2Dkey (and encoded twice)
	encodedNameBytesRe = regexp.MustCompile(`(?i)%(?:25)?(?:5F|2D)`)
	// an HTML entity for '=' or ':' between a name and its value (password&#61;VALUE, &#x3d;, &equals;, &#58;, &#x3a;, &colon;), escaped again or not (#5242)
	entitySepRe = regexp.MustCompile(`(?i)&(?:amp;)*(?:#0*61;|#x0*3d;|equals;|#0*58;|#x0*3a;|colon;)`)
)

// plainSpellings is text with each odd quote entity written as one '"' and each encoded '_' or '-' as itself, and for each byte of the result where its spelling starts and ends in text (a span that
// ends on a replaced byte ends after the whole spelling, so no tail of an encoded value is left showing); at is nil when there is nothing to change.
func plainSpellings(text string) (norm string, at, to []int) {
	type rep struct {
		start, end int
		to         byte
	}
	var reps []rep
	for _, m := range oddEntityQuoteRe.FindAllStringIndex(text, -1) {
		reps = append(reps, rep{m[0], m[1], '"'})
	}
	for _, m := range entitySepRe.FindAllStringIndex(text, -1) {
		to := byte('=')
		if e := strings.ToLower(text[m[0]:m[1]]); strings.HasSuffix(e, "58;") || strings.HasSuffix(e, "3a;") || strings.HasSuffix(e, "colon;") {
			to = ':'
		}
		reps = append(reps, rep{m[0], m[1], to})
	}
	for _, m := range encodedNameBytesRe.FindAllStringIndex(text, -1) {
		to := byte('_')
		if strings.HasSuffix(strings.ToLower(text[m[0]:m[1]]), "2d") {
			to = '-'
		}
		reps = append(reps, rep{m[0], m[1], to})
	}
	if len(reps) == 0 {
		return "", nil, nil
	}
	// the two lists are each in order; merge by start and drop an overlap (an entity quote and a name byte never overlap, but be safe)
	for i := 1; i < len(reps); i++ {
		for j := i; j > 0 && reps[j].start < reps[j-1].start; j-- {
			reps[j], reps[j-1] = reps[j-1], reps[j]
		}
	}
	var b strings.Builder
	at = make([]int, 0, len(text))
	to = make([]int, 0, len(text))
	pos := 0
	for _, r := range reps {
		if r.start < pos {
			continue
		}
		for ; pos < r.start; pos++ {
			b.WriteByte(text[pos])
			at, to = append(at, pos), append(to, pos+1)
		}
		b.WriteByte(r.to)
		at, to = append(at, r.start), append(to, r.end) // the one byte stands for the whole spelling
		pos = r.end
	}
	for ; pos < len(text); pos++ {
		b.WriteByte(text[pos])
		at, to = append(at, pos), append(to, pos+1)
	}
	return b.String(), at, to
}

const (
	moreNames = `(?:[a-z0-9]+[_-])*(?:` + keyNames + `|` + passwordNames + `|` + exactNames + `)`
)

var (
	// <api_key>VALUE</api_key>, with the name's own prefix (<x_api_key>, <db-password>) and nothing but the value between the tags.
	xmlElementRe = regexp.MustCompile(`(?i)<(` + moreNames + `)\s*>\s*([^<>\s"'&]{8,})\s*</`)
	// <input ... name="api_key" ... value="VALUE">, and the attributes the other way round; the value is in quotes.
	formNameFirstRe  = regexp.MustCompile(`(?i)<input\b[^>]*?\bname\s*=\s*["']?(` + moreNames + `)["']?[^>]*?\bvalue\s*=\s*["']([^"'<>]{8,})["']`)
	formValueFirstRe = regexp.MustCompile(`(?i)<input\b[^>]*?\bvalue\s*=\s*["']([^"'<>]{8,})["'][^>]*?\bname\s*=\s*["']?(` + moreNames + `)["']?`)
	// a pair URL-encoded as JSON: %22name%22%3A%22VALUE%22 (the quotes around the name and the value are optional), encoded once or twice.
	encodedPairRe = regexp.MustCompile(`(?i)(` + moreNames + `)(?:%(?:25)?22)?%(?:25)?3A(?:%(?:25)?20|\+)?(?:%(?:25)?22)?(` + encValue + `{8,})`)
	// curl -b / --cookie: a cookie string NAME=VALUE[; NAME=VALUE]: glued on (-bNAME=VALUE), after a space or '=', quoted with " or ' or not. A file name (-b cookies.txt) has no '='.
	curlCookieRe = regexp.MustCompile(`\bcurl\b[^\n]*?\s(?:-b|--cookie)(?:\s+|=)?(?:"([^"\n]*)"|'([^'\n]*)'|([^\s"']+))`)
	// docker login -u USER -p VALUE (or --password VALUE, --password=VALUE; --password-stdin has no value).
	dockerLoginRe = regexp.MustCompile(`\bdocker\b[^\n]*?\blogin\b[^\n]*?\s(?:-p|--password)(?:\s+|=)["']?([^\s"']{4,})`)
)

func xmlElementSpans(text string) []redact.Span {
	var out []redact.Span
	for _, m := range xmlElementRe.FindAllStringSubmatchIndex(text, -1) {
		if moreValueOK(text[m[2]:m[3]], text[m[4]:m[5]]) {
			out = append(out, redact.Span{Start: m[4], End: m[5]})
		}
	}
	return out
}

func formFieldSpans(text string) []redact.Span {
	var out []redact.Span
	for _, m := range formNameFirstRe.FindAllStringSubmatchIndex(text, -1) {
		if moreValueOK(text[m[2]:m[3]], text[m[4]:m[5]]) {
			out = append(out, redact.Span{Start: m[4], End: m[5]})
		}
	}
	for _, m := range formValueFirstRe.FindAllStringSubmatchIndex(text, -1) {
		if moreValueOK(text[m[4]:m[5]], text[m[2]:m[3]]) {
			out = append(out, redact.Span{Start: m[2], End: m[3]})
		}
	}
	return out
}

func encodedQuotedPairSpans(text string) []redact.Span {
	var out []redact.Span
	for _, m := range encodedPairRe.FindAllStringSubmatchIndex(text, -1) {
		if moreValueOK(text[m[2]:m[3]], text[m[4]:m[5]]) {
			out = append(out, redact.Span{Start: m[4], End: m[5]})
		}
	}
	return out
}

// moreValueOK is keywordValueOK for these shapes: the value is delimited (by tags, quotes or an encoded quote), so a plain word counts as it does after '=', but not a placeholder, and not one too short for the name.
func moreValueOK(name, value string) bool {
	if value == "" || placeholderValue(value) {
		return false
	}
	return keywordValueOK(keywordRule{}, name, "=", value)
}

func curlCookieSpans(text string) []redact.Span {
	var out []redact.Span
	for _, m := range curlCookieRe.FindAllStringSubmatchIndex(text, -1) {
		g := 2
		for _, k := range []int{2, 4, 6} {
			if m[k] >= 0 {
				g = k
				break
			}
		}
		base, arg := m[g], text[m[g]:m[g+1]]
		for _, p := range cookiePairRe.FindAllStringSubmatchIndex(arg, -1) {
			name, value := arg[p[2]:p[3]], arg[p[4]:p[5]]
			if cookieAttrs[strings.ToLower(name)] || placeholderValue(value) {
				continue
			}
			if len(value) >= minKeyLen || len(value) >= minPasswordLen && cookieNames.MatchString(name) {
				out = append(out, redact.Span{Start: base + p[4], End: base + p[5]})
			}
		}
	}
	return out
}

func dockerLoginSpans(text string) []redact.Span {
	var out []redact.Span
	for _, m := range dockerLoginRe.FindAllStringSubmatchIndex(text, -1) {
		if v := text[m[2]:m[3]]; !placeholderValue(v) {
			out = append(out, redact.Span{Start: m[2], End: m[3]})
		}
	}
	return out
}
