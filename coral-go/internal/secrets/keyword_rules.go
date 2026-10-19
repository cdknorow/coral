package secrets

import (
	"regexp"
	"strings"

	"github.com/cdknorow/coral/pkg/redact"
)

// Keyword rules (#5132, #5141): the value after a name that says it is a secret is redacted whatever its entropy. betterleaks' generic rules
// gate on entropy, and a random value is sometimes below the threshold (coral-hub measured an api_key at the end of a query string
// missing 18 times in 100, a few others 1 to 2 percent): the keyword is itself the evidence, so these rules do not ask the value to look random.
// Each is a Go regexp (linear time, no backtracking) over the text a chunk at a time, like the rules in extra.go.
//
// The tradeoff, on purpose, is the value's length and where a bare word is not enough:
//   - a value in a query string must be at least 12 characters. Otherwise a value must be at least minKeyLen (16) characters after an api key, token or access key name, and at least minPasswordLen (8) after a
//     password, passphrase or secret: "token=abc" or "password=" in prose, and a value that is empty or a placeholder, are left alone.
//     A password of 7 characters or fewer after "password=" is NOT redacted by these rules (betterleaks' own rules still may).
//   - after an unquoted ':' ("password: correcthorse") a value of letters only is kept unless it is 16 or more characters: prose reads the
//     same way ("password: required"), and a key of 16 letters with no digit is no word. After '=' or a quote any value is redacted, as the
//     streaming rules do (Security on #3733).
//   - a value that is a variable or a template ($TOKEN, ${TOKEN}, {{ token }}, <token>, %(token)s) is not a value, and nor is one made of a single
//     repeated character (****, xxxx).
//   - a name follows '_' or '-' or starts the word (DB_PASSWORD, x-api-key, api_key), never a letter or digit ("mytoken", "secretary", "tokens"); the
//     exceptions are exact names, an allow-list of the ones people write as one word (PGPASSWORD, MYSQL_PWD, AWS_SECRET, ...: exactNames).
//   - the same rules read a name and value that are JSON inside a JSON string (\"api_key\": \"...\", also escaped twice), URL-encoded (api_key%3D...,
//     also inside a redirect parameter, also encoded twice), after a full-width '=' or ':', as a command-line argument (--password VALUE, --token=VALUE),
//     as mysql -pVALUE or sshpass -p VALUE, and as a Cookie or Set-Cookie value (cookieSpans).
const (
	minKeyLen      = 16
	minPasswordLen = 8
)

type ruleKind int

const (
	kindName    ruleKind = iota // a name, a separator and a value: the length depends on the name
	kindCurl                    // curl -u user:password: group 3 is the password
	kindAuth                    // Authorization: Bearer ...
	kindCmdPass                 // mysql -pPASSWORD, sshpass -p PASSWORD: group 3 is the password
	kindArg                     // --password VALUE: a flag names the value, so a plain word counts
)

// keywordRule: re has the name in group 1, the separator (an optional closing quote, ':' or '=', an optional opening quote) in group 2 and the
// value in group 3. fixed: the pattern holds the minimum length (a query string, an encoded value), so the name does not.
type keywordRule struct {
	re    *regexp.Regexp
	kind  ruleKind
	fixed bool
}

// The value ends at white space, a quote or an angle bracket; except after a password the characters that end a query parameter or a list (& ; , ) ] })
// end it too (a password may hold them: its tail must not be left showing, so there the value runs on to a space). A backslash ends it only before a
// quote (the end of a value written inside a JSON string, \") or another backslash; elsewhere it is part of it.
const (
	valueEnd         = `(?:[^\s"'<>\\&;,)\]}]|\\[^"'\\])`
	valueEndPassword = `(?:[^\s"'<>\\]|\\[^"'\\])`
	nameBoundary     = `(?:^|[^A-Za-z0-9])`
	encBoundary      = `(?:^|[^A-Za-z0-9]|%[0-9A-Fa-f]{2})` // a name after %3F, %26 (a URL inside a parameter)
	nameChain        = `((?:[a-z0-9]+[_-])*`
	// The separator: an optional closing quote (raw, or backslash-escaped, once or twice, as JSON inside a JSON string writes it), ':' or '=' (or the
	// full-width forms), an optional opening quote.
	// A quote as a log or a web page writes it: raw, backslash-escaped, or an HTML entity (&quot; &#34; &#x22; and the single-quote forms, #5172).
	quoteLike   = `(?:\\*["']|&(?:quot|apos|#0*34|#0*39|#[xX]0*2[27]);)`
	sepAndQuote = `((?:` + quoteLike + `)?\s*[:=\x{FF1D}\x{FF1A}]\s*(?:` + quoteLike + `)?)`
	// '=' URL-encoded, or encoded twice; or ':' with its space (password%3A%20value, #5172).
	encSep = `(%(?:25)?3[Dd](?:%(?:25)?20|\+)?|%(?:25)?3[Aa](?:%(?:25)?20|\+)?)`
	// What a URL-encoded value may hold: the characters of a key or a token, encoded or not; it ends at the first encoded & ; space or quote.
	encValue = `(?:[A-Za-z0-9._~+/=-]|%(?:2[BbFfDdEe]|3[Dd]|5[Ff]))`

	keyNames      = `api[_-]?key|apikey|x-api-key|api[_-]?secret|secret[_-]?access[_-]?key|access[_-]?key|secret[_-]?key|client[_-]?secret|auth[_-]?key|secret|token`
	passwordNames = `password|passwd|passphrase`
	queryNames    = `api[_-]?key|apikey|access[_-]?token|auth[_-]?token|token|key|secret|sig|signature|auth|password|passwd`
	// Names people write as one word, or that carry a prefix a name rule would not take: matched whole (the name rules never look inside a word).
	exactNames = `pgpassword|pgpass|mysql_pwd|mysqlpwd|sshpass|aws_secret|rediscli_auth|secret_key_base|apitoken|authtoken|accesstoken|refreshtoken|bearertoken`
	argNames   = `password|passwd|passphrase|pass|token|api-?key|apikey|secret|access-?key|auth-?token|client-?secret`
)

var keywordRules = []keywordRule{
	// An API key, a token, an access key or a secret key by name: password-less shapes, any prefix (GITHUB_TOKEN, x-api-key, aws_secret_access_key).
	{re: regexp.MustCompile(`(?i)` + nameBoundary + nameChain + `(?:` + keyNames + `))` + sepAndQuote + `(` + valueEnd + `{8,})`)},
	// A password: the value may hold &, ; and others up to a space or a quote.
	{re: regexp.MustCompile(`(?i)` + nameBoundary + nameChain + `(?:` + passwordNames + `))` + sepAndQuote + `(` + valueEndPassword + `{8,})`)},
	// The names written as one word: PGPASSWORD, MYSQL_PWD, AWS_SECRET, SECRET_KEY_BASE...
	{re: regexp.MustCompile(`(?i)` + nameBoundary + nameChain + `(?:` + exactNames + `))` + sepAndQuote + `(` + valueEndPassword + `{8,})`)},
	// A key, token, signature or secret in a URL's query string (the name alone is "key", "sig" or "auth": ordinary words elsewhere).
	{re: regexp.MustCompile(`(?i)[?&;]((?:[a-z0-9]+[_-])*(?:` + queryNames + `))()=(` + valueEnd + `{12,})`), fixed: true},
	// The same, URL-encoded: api_key%3D..., and nested in a redirect (next=https%3A%2F%2Fh%2Fcb%3Ftoken%3D...%26a%3D1), or encoded twice.
	{re: regexp.MustCompile(`(?i)` + encBoundary + nameChain + `(?:` + keyNames + `|` + passwordNames + `|` + exactNames + `))` + encSep + `(` + encValue + `{8,})`)},
	{re: regexp.MustCompile(`(?i)(?:%(?:25)?3F|%(?:25)?26|%(?:25)?3B)((?:[a-z0-9]+[_-])*(?:` + queryNames + `))` + encSep + `(` + encValue + `{12,})`), fixed: true},
	// An environment variable named ..._KEY in upper case, exported or on a line of its own (an env dump): STRIPE_KEY=..., SSH_KEY=...
	{re: regexp.MustCompile(`(?m)(?:^|[^A-Za-z0-9_])(?:export\s+)?([A-Z][A-Z0-9]*(?:_[A-Z0-9]+)*_(?:KEY|CREDENTIALS?))(=["']?)(` + valueEnd + `{16,})`)},
	// A command-line argument: --password VALUE, --token=VALUE, --api-key VALUE (the value does not start with '-': it is not the next flag).
	{re: regexp.MustCompile(`(?i)(?:^|[\s"'])(--(?:[a-z0-9]+-)*(?:` + argNames + `))(\s+|=)["']?([^\s"'<>&;,)\]}\-]` + valueEnd + `*)`), kind: kindArg},
	// curl -u user:password and --user user:password: the password.
	{re: regexp.MustCompile(`\bcurl\b[^\n]*?\s(-u|--user)(\s+|=)["']?[^:\s"']+:([^\s"']{4,})`), kind: kindCurl},
	// mysql -pPASSWORD (no space: with one it prompts) and sshpass -p PASSWORD.
	{re: regexp.MustCompile(`\b((?i:mysql(?:dump|admin|import|check|show|pump)?))\b[^\n]*?\s(-p)([^\s"'\-][^\s"']{3,})`), kind: kindCmdPass},
	{re: regexp.MustCompile(`\b(sshpass)\b[^\n]*?\s(-p)\s*["']?([^\s"']{4,})`), kind: kindCmdPass},
	// An Authorization header with a bearer or token value, whatever the value looks like (extra.go's Bearer rule wants letters and digits).
	{re: regexp.MustCompile(`(?i)(authorization)(["']?\s*[:=]\s*["']?)(?:bearer|token)\s+([A-Za-z0-9._~+/=-]{12,})`), kind: kindAuth},
}

// keywordSpansIn runs every keyword rule over text, all of it: keywordSpans (keyword_gate.go) runs them only where a name could be, and
// the test holds the two equal.
func keywordSpansIn(text string) []redact.Span {
	var out []redact.Span
	for _, r := range keywordRules {
		for _, m := range r.re.FindAllStringSubmatchIndex(text, -1) {
			end := trimEntityQuotes(text, m[6], m[7]) // a password may hold &, so a closing &quot; is part of what was matched: it is not the value's
			name, sep, value := text[m[2]:m[3]], text[m[4]:m[5]], text[m[6]:end]
			if value != "" && keywordValueOK(r, name, sep, value) {
				out = append(out, redact.Span{Start: m[6], End: end})
			}
		}
	}
	return append(out, cookieSpans(text)...)
}

// trimEntityQuotes is end moved back over the HTML quote entities that end text[start:end] (&quot; &#34; ...).
func trimEntityQuotes(text string, start, end int) int {
	for end-start >= 4 && text[end-1] == ';' {
		tail := strings.ToLower(text[max(start, end-12):end])
		n := entityQuoteEndingAt(tail, len(tail))
		if n == 0 {
			break
		}
		end -= n
	}
	return end
}

func keywordValueOK(r keywordRule, name, sep, value string) bool {
	switch r.kind {
	case kindCurl:
		return value[0] != '$' && value[0] != '<' && value[0] != '{' // a password after user: on a curl line is whatever the person typed, but not a variable or a placeholder
	case kindCmdPass:
		return !placeholderValue(value)
	case kindAuth:
		return !placeholderValue(value) // the value has its own shape
	}
	if placeholderValue(value) {
		return false
	}
	if r.fixed {
		return true // the pattern holds the minimum
	}
	lname := strings.ToLower(name)
	if r.kind == kindArg {
		return len(value) >= argMinLen(lname)
	}
	switch {
	case strings.Contains(lname, "pass") || strings.Contains(lname, "pwd") || strings.Contains(lname, "rediscli") || strings.Contains(lname, "secret") && !strings.Contains(lname, "key"):
		if len(value) < minPasswordLen {
			return false
		}
	default:
		if len(value) < minKeyLen {
			return false
		}
	}
	// After an unquoted ':' or a space only a value that is not a plain word counts (see the tradeoff).
	if !strings.ContainsAny(sep, "=\uFF1D\"'%&") && lettersOnly(value) && len(value) < minKeyLen {
		return false
	}
	return true
}

// argMinLen: the minimum value after a --flag, by the flag's name (a password is shorter than a key).
func argMinLen(lname string) int {
	if strings.Contains(lname, "pass") || strings.Contains(lname, "secret") && !strings.Contains(lname, "key") {
		return minPasswordLen
	}
	return minKeyLen
}

func lettersOnly(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}

// placeholderValue: a variable or a template, not a value, or one character repeated.
func placeholderValue(v string) bool {
	switch v[0] {
	case '$', '{', '<', '(', '[', '`':
		return true
	case '%':
		if len(v) > 1 && v[1] == '(' {
			return true
		}
	}
	for i := 1; i < len(v); i++ {
		if v[i] != v[0] {
			return false
		}
	}
	return true
}

// A Cookie or Set-Cookie header (#5141): the values of its pairs, which are session ids and tokens. A value of 16 or more characters is redacted
// whatever its name; a shorter one of 8 or more only under a name that says session, token or auth. The attributes of Set-Cookie (Path, Domain,
// Expires, Max-Age, SameSite, Secure, HttpOnly) are not values.
var (
	cookieHeaderRe = regexp.MustCompile(`(?i)\b(?:set-)?cookie["']?\s*[:=]\s*["']?([^\r\n"']*)`)
	cookiePairRe   = regexp.MustCompile(`(?:^|[;\s])([A-Za-z0-9_.-]+)=([^;\s"']+)`)
	cookieAttrs    = map[string]bool{"path": true, "domain": true, "expires": true, "max-age": true, "samesite": true, "secure": true, "httponly": true, "version": true, "comment": true, "priority": true}
	cookieNames    = regexp.MustCompile(`(?i)sess|sid|auth|token|jwt|remember|login|csrf|xsrf|key|secret|pass`)
)

func cookieSpans(text string) []redact.Span {
	var out []redact.Span
	for _, h := range cookieHeaderRe.FindAllStringSubmatchIndex(text, -1) {
		pairs := text[h[2]:h[3]]
		for _, m := range cookiePairRe.FindAllStringSubmatchIndex(pairs, -1) {
			name, value := pairs[m[2]:m[3]], pairs[m[4]:m[5]]
			if cookieAttrs[strings.ToLower(name)] || placeholderValue(value) {
				continue
			}
			if len(value) >= minKeyLen || len(value) >= minPasswordLen && cookieNames.MatchString(name) {
				out = append(out, redact.Span{Start: h[2] + m[4], End: h[2] + m[5]})
			}
		}
	}
	return out
}
