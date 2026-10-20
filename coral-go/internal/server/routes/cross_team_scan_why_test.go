package routes

import (
	"encoding/base64"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/cdknorow/coral/internal/secrets"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #5212: a refusal says where (the line) and by which rule, and never repeats any of the secret; and a code reference in a value position is not taken for one.
func TestRefusalNamesTheLineAndTheRule(t *testing.T) {
	require.NoError(t, secrets.Init())
	t.Setenv(secrets.KeywordsEnv, "")
	SetKeywordSwitch(nil)

	text := "first line\nsecond line\nthird: api_key = " + keywordValue + "\nfourth line\n"
	got, why := scanSecretsWhy([]byte(text))
	require.Equal(t, scanFound, got)
	assert.Equal(t, 3, why.line)
	assert.Contains(t, why.rule, `"api_key"`, "the keyword rule and the name before the value")
	assert.Equal(t, ` (line 3: a value after the name "api_key" (the keyword rules))`, why.where())
	assert.NotContains(t, why.where(), keywordValue, "none of the secret is repeated")

	// a library rule is named by its id
	token := "ghp_" + strings.Repeat("aB3dE5gH7j", 3) + "K9mN1p"
	got, why = scanMessageTextWhy([]byte("note\nthe token is " + token + " ok"))
	require.Equal(t, scanFound, got)
	assert.Equal(t, 2, why.line)
	assert.Contains(t, why.rule, "github", "a betterleaks rule by its id: "+why.rule)
	assert.NotContains(t, why.where(), token)

	// a secret only in a re-encoded copy: the copy's lines, and it says it is a copy
	enc := base64.StdEncoding.EncodeToString([]byte("some prose\napi_key = " + keywordValue + "\n"))
	got, why = scanSecretsWhy([]byte("a file\nwith a blob:\n" + enc + "\n"))
	require.Equal(t, scanFound, got)
	assert.Equal(t, "a re-encoded copy of it", why.view)
	assert.Contains(t, why.where(), "of a re-encoded copy of it")

	// nothing found: no clause
	assert.Equal(t, "", scanWhy{}.where())
}

func TestCodeReferencesAreNotTakenForSecrets(t *testing.T) {
	require.NoError(t, secrets.Init())
	t.Setenv(secrets.KeywordsEnv, "")
	SetKeywordSwitch(nil)
	for _, code := range []string{
		"prevKey := config.PostHogKey",
		"config.PostHogKey = prevKey",
		"\tkey := config.PostHogKey\n",
		"token: container._modelToken",
		"const apiKey = settings.PostHog_Api_Key;",
		"secret = this.options.clientSecret,",
		`authToken: "session.currentUser.token"`,
	} {
		got, why := scanMessageTextWhy([]byte(code))
		assert.Equal(t, scanClean, got, "a code reference is left alone: %q (%s)", code, why.where())
	}
	// a real value that holds a digit, or is not dotted identifiers, is still found, 200 times with random bodies
	r := rand.New(rand.NewSource(5212))
	const set = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	for i := 0; i < 200; i++ {
		body := make([]byte, 24)
		for k := range body {
			body[k] = set[r.Intn(len(set))]
		}
		value := string(body[:10]) + "." + string(body[10:]) + "7" // dotted, but with a digit
		got, _ := scanMessageTextWhy([]byte(fmt.Sprintf("api_key = %s", value)))
		assert.Equal(t, scanFound, got, "a dotted value with a digit is a secret: %s", "api_key = <value>")
	}
	// offsets are the file's own after the mask
	masked := maskCodeReferences([]byte("a := config.PostHogKey\nb = 1"))
	assert.Equal(t, "a := aaaaaaaaaaaaaaaaa\nb = 1", string(masked))
	assert.Equal(t, "x = y", string(maskCodeReferences([]byte("x = y"))), "no reference: the same bytes")
}

// #5212 round 2: a dotted-words value after a password or secret name is a passphrase, not a code reference, and is still refused; the code-reference shapes still pass.
func TestPassphraseAfterPasswordNameIsStillRefused(t *testing.T) {
	require.NoError(t, secrets.Init())
	t.Setenv(secrets.KeywordsEnv, "")
	SetKeywordSwitch(nil)
	for _, text := range []string{
		"password = correct.horse.battery.staple",
		"db_password: staple.horse.battery",
		"client_secret=lemon.drizzle.cake",
		"passwd = lemon.drizzle.cake\n",
		"passphrase: lemon.drizzle.cake",
		`"secret": "lemon.drizzle.cake"`,
		"secret = lemon.drizzle.cake",
	} {
		got, why := scanMessageTextWhy([]byte(text))
		assert.Equal(t, scanFound, got, "a passphrase is refused: %q", text)
		assert.Equal(t, 1, why.line)
	}
	for _, code := range []string{
		"token: container._modelToken",
		"prevKey := config.PostHogKey",
		"secret = this.options.clientSecret,",
		"client_secret = os.environ_map",
	} {
		got, why := scanMessageTextWhy([]byte(code))
		assert.Equal(t, scanClean, got, "a code reference stays clean: %q (%s)", code, why.where())
	}
}

// the refusal text carries the name before the value, never a token-looking prefix of a name chain
func TestRefusalNameChainCarriesNoToken(t *testing.T) {
	require.NoError(t, secrets.Init())
	t.Setenv(secrets.KeywordsEnv, "")
	SetKeywordSwitch(nil)
	prefix := "ghp_" + strings.Repeat("aB3dE5gH7j", 3) + "K9mN1p"
	got, why := scanMessageTextWhy([]byte("x " + prefix + "_token=" + keywordValue + "\n"))
	require.Equal(t, scanFound, got)
	assert.NotContains(t, why.where(), prefix)
	assert.NotContains(t, why.where(), "ghp_")
	assert.NotContains(t, why.where(), keywordValue)
}
