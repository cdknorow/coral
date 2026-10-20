package secrets

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Round five (#5242): the entity-encoded equals and colon, npm and yarn config set, an .npmrc line, and a "pwd" name: 2000 random values each, none may come back, as text and as a JSON reply.
func TestRoundFiveShapesAreRedacted(t *testing.T) {
	require.NoError(t, Init())
	shapes := []struct {
		name   string
		format string // %s is the value
	}{
		{"password&#61;", "login?user=a&password&#61;%s"},
		{"password&#x3d;", "password&#x3d;%s"},
		{"password&#X3D;", "DB_PASSWORD&#X3D;%s"},
		{"password&equals;", "password&equals;%s"},
		{"password&amp;#61; (escaped again)", "password&amp;#61;%s"},
		{"password&#0061; (zero padded)", "password&#0061;%s"},
		{"api_key&#58; (entity colon)", "api_key&#58; %s"},
		{"token&colon; (entity colon)", "token&colon;%s"},
		{"token&#x3a; (entity colon)", "access_token&#x3a;%s"},
		{"npm config set registry token", "npm config set //registry.npmjs.org/:_authToken %s"},
		{"npm config set, a flag first", "npm config set -g //registry.npmjs.org/:_authToken %s"},
		{"npm config set, a long flag", "npm config --location=user set //registry.npmjs.org/:_authToken %s"},
		{"npm config set _authToken", "npm config set _authToken %s"},
		{"npm config set, quoted value", `npm config set //npm.pkg.github.com/:_authToken "%s"`},
		{"npm config set _auth", "npm config set _auth %s"},
		{"npm config set _password", "npm config set //registry.test/:_password %s"},
		{"yarn config set npmAuthToken", "yarn config set npmAuthToken %s"},
		{"pnpm config set", "pnpm config set //registry.test/:_authToken %s"},
		{".npmrc _authToken", "//registry.npmjs.org/:_authToken=%s"},
		{".npmrc _auth", "_auth=%s"},
		{".npmrc _auth, indented, quoted", "  _auth = \"%s\""},
		{".npmrc _password", "//registry.test/:_password=%s"},
		{"pwd=", "pwd=%s"},
		{"DB_PWD=", "export DB_PWD=%s"},
		{"pwd JSON", `{"pwd": "%s"}`},
		{"pwd after a colon", "pwd: %s"},
	}
	for _, s := range shapes {
		t.Run(s.name, func(t *testing.T) {
			for i := 0; i < 2000; i++ {
				value := fake(32, alnumAZ, uint32(i*7919+len(s.name)))
				if strings.Contains(s.name, "pwd after a colon") {
					value += "9" // after an unquoted ':' a plain word is kept, as for every name
				}
				text := fmt.Sprintf(s.format, value)
				out, err := hubRedactor().Text([]byte(text))
				require.NoError(t, err)
				if strings.Contains(string(out), value) {
					t.Fatalf("%s: sample %d leaked: %.90q", s.name, i, text)
				}
				raw, _ := json.Marshal(map[string]any{"content": text})
				reply, err := hubRedactor().Reply(raw, true)
				require.NoError(t, err)
				if strings.Contains(string(reply), value) {
					t.Fatalf("%s: sample %d leaked in a JSON reply: %.90q", s.name, i, text)
				}
			}
		})
	}
}

// What is not a secret stays as it is: placeholders, paths after pwd, ordinary npm commands, a short value.
func TestRoundFiveLeavesOrdinaryTextAlone(t *testing.T) {
	require.NoError(t, Init())
	for _, text := range []string{
		"pwd=$(pwd)",
		"pwd=${PWD}",
		"pwd=/home/alice/src/project",
		"PWD=/Users/alice/Software/coral",
		`PWD=C:\Users\alice\work`,
		"pwd=~/projects/coral",
		"pwd=./relative/path/here",
		"pwd=../up/one/level",
		`pwd=\\server\share\dir`,
		"pwd=short",
		"pwd=",
		"pwd=<your-password-here>",
		"pwd=xxxxxxxxxxxx",
		"the pwd command prints the directory",
		"npm config set registry https://registry.npmjs.org/",
		"npm config set save-exact true",
		"npm config set //registry.npmjs.org/:_authToken ${NPM_TOKEN}",
		"npm config set _authToken $NPM_TOKEN",
		"npm config set _authToken <token>",
		"npm config get //registry.npmjs.org/:_authToken",
		"npm config set loglevel verbose",
		"yarn config set nodeLinker node-modules",
		"//registry.npmjs.org/:_authToken=${NPM_TOKEN}",
		"_auth=${NPM_AUTH}",
		"registry=https://registry.npmjs.org/",
		"a &#61; b and c &equals; d, &colon; and &#58; alone",
		"password&#61;",
		"password&#61;short",
		"x &#x3d; y",
	} {
		out, err := hubRedactor().Text([]byte(text))
		require.NoError(t, err)
		assert.Equal(t, text, string(out), "left as it is")
	}
}

// The name stays where there is one: only the value goes.
func TestRoundFiveKeepsTheName(t *testing.T) {
	require.NoError(t, Init())
	for text, want := range map[string]string{
		"npm config set //registry.npmjs.org/:_authToken " + strings.Repeat("a1", 16): "npm config set //registry.npmjs.org/:_authToken ",
		"password&#61;" + strings.Repeat("b2", 12):                                    "password&#61;",
		"pwd=" + strings.Repeat("c3", 8):                                              "pwd=",
	} {
		out, err := hubRedactor().Text([]byte(text))
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(string(out), want), "%q -> %q", text, out)
		assert.NotContains(t, string(out), strings.Repeat("a1", 4))
	}
}

func FuzzRoundFiveSpans(f *testing.F) {
	for _, s := range []string{"npm config set _authToken abcdefgh12345678", "password&#61;abcdefgh1234", "pwd=abcdefgh1234", "_auth=abcdefgh1234", "&#58;&colon;&#x3a;&equals;&#x3d;&#61;", "npm config set -g -g -g x y"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, text string) {
		for _, sp := range append(npmConfigSpans(text), pwdSpans(text)...) {
			require.True(t, sp.Start >= 0 && sp.Start < sp.End && sp.End <= len(text), "span %v in %d bytes", sp, len(text))
		}
		for _, sp := range moreKeywordSpans(text) {
			require.True(t, sp.Start >= 0 && sp.Start < sp.End && sp.End <= len(text), "span %v in %d bytes", sp, len(text))
		}
	})
}
