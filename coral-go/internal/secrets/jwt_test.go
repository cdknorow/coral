package secrets

import (
	"strings"
	"testing"
)

// #6253: a JSON Web Token pasted or echoed is masked by the redactor Coral uses (the library's rules), wherever it appears, so a summary, a first message or an excerpt never stores one whole.
const jwtFixture = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiaWF0IjoxNTE2MjM5MDIyfQ.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"

func TestAJWTIsMaskedByTheRedactorCoralUses(t *testing.T) {
	for name, in := range map[string]string{
		"alone":          jwtFixture,
		"in a sentence":  "my token is " + jwtFixture + " please check it",
		"before a stop":  "the token " + jwtFixture + ".",
		"in JSON":        `{"id_token":"` + jwtFixture + `","n":1}`,
		"in a URL query": "https://example.com/cb?access_token=" + jwtFixture + "&x=1",
		"after Bearer":   "Authorization: Bearer " + jwtFixture,
	} {
		out, err := Redactor().Text([]byte(in))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got := string(out)
		if strings.Contains(got, "eyJzdWIi") || strings.Contains(got, "SflKxwRJ") {
			t.Errorf("%s: a JWT is left: %q", name, got)
		}
	}
	for _, in := range []string{"eyJ", "the word eyJ appears here", "eyJhbGciOiJIUzI1NiJ9 header only"} {
		out, _ := Redactor().Text([]byte(in))
		if string(out) != in {
			t.Errorf("%q was changed to %q", in, out)
		}
	}
}
