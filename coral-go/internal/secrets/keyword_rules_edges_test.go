package secrets

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// QA on #5141: edges of the round-2 rules that the shape tables did not pin (each was a surviving mutant).

func redactedText(t *testing.T, in string) string {
	t.Helper()
	b, err := hubRedactor().Text([]byte(in))
	assert.NoError(t, err)
	return string(b)
}

// The value inside a JSON string stops before the escaped closing quote: the backslash stays, so the JSON around it still parses.
func TestKeywordJSONInJSONValueStopsBeforeTheEscapedQuote(t *testing.T) {
	const v = "Xk9mQ2vL7pR4tZ8wN3bY"
	out := redactedText(t, `{"log":"{\"api_key\": \"`+v+`\"}"}`)
	assert.NotContains(t, out, v)
	assert.Contains(t, out, `[redacted token]\"}"}`, "the escape of the closing quote is part of the text around the value")
}

// A URL-encoded value may hold an encoded underscore.
func TestKeywordEncodedValueMayHoldAnEncodedUnderscore(t *testing.T) {
	out := redactedText(t, "q=token%3Dabcd%5Fefgh%5Fijkl%5Fmnop%5Fqrst")
	assert.NotContains(t, out, "qrst")
}

// A query parameter that follows a semicolon is a query parameter.
func TestKeywordQueryAfterSemicolon(t *testing.T) {
	out := redactedText(t, "a=1;key=abababababababab") // a low-entropy value: only the keyword rule can see it
	assert.NotContains(t, out, "abababab")
}

// The attributes of Set-Cookie are not values, whatever their length.
func TestKeywordSetCookieAttributesAreNotRedacted(t *testing.T) {
	in := "Set-Cookie: theme=dark; Domain=www.example-long-domain.com; Path=/; Max-Age=31536000"
	assert.Equal(t, in, redactedText(t, in))
	assert.True(t, strings.Contains(redactedText(t, "Set-Cookie: sid=Xk9mQ2vL7pR4tZ8wN3bY; Domain=www.example-long-domain.com"), "Domain=www.example-long-domain.com"))
}
