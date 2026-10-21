package routes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/secrets"
	"github.com/subgentic/coral/pkg/redact"
	"github.com/subgentic/coral/pkg/tunnel"
)

// #5172: the paths a Coral Hub viewer can reach that cut or keep a text before the reply is redacted use the hub's redactor (the keyword rules) when
// the request came through the tunnel: a cut that leaves part of a value the keyword rules would take (under their minimum length) shows it.

const keywordValue = "aaaabbbbccccddddeeeeffff" // low entropy: only a keyword rule takes it, whole or cut

func keywordPage(n int) (page []map[string]any, content string) {
	content = strings.Repeat("log line\n", 28) + "api_key=" + keywordValue + "\nafter\n" + strings.Repeat("more output\n", 40)
	return []map[string]any{{"type": "tool_result", "tool_use_id": "t1", "content": content}}, content
}

func shownAfter(t *testing.T, rd redact.Redactor, n int) string {
	t.Helper()
	page, _ := keywordPage(n)
	body, err := json.Marshal(map[string]any{"messages": trimChatPage(page, n, rd)})
	require.NoError(t, err)
	out, err := secrets.HubRedactor(nil).Reply(body, true) // the tunnel's own redaction of the reply
	require.NoError(t, err)
	return trimSecretShown(string(out), keywordValue)
}

func TestTrimNeverShowsAPieceOfAKeywordValueThroughTheHub(t *testing.T) {
	require.NoError(t, secrets.Init())
	t.Setenv(secrets.KeywordsEnv, "")
	_, content := keywordPage(0)
	// At every trim length, with the hub's redactor before the cut and again on the reply, no piece of the value shows. (With the local redactor
	// before the cut the result is the same today: redact.Cut never splits a run that could be a token, so a value is kept whole, after its name, and
	// the reply's redaction takes it, or is dropped whole. The hub's redactor goes first all the same, so the cut never depends on that.)
	for n := trimMin; n <= len(content); n++ {
		if shown := shownAfter(t, secrets.HubRedactor(nil), n); shown != "" {
			t.Fatalf("trim=%d shows %q of the value through the hub", n, shown)
		}
		if shown := shownAfter(t, secrets.Redactor(), n); shown != "" {
			t.Fatalf("trim=%d with the local redactor first shows %q: a leak the hub's redactor did not have", n, shown)
		}
	}
}

func TestRequestRedactorIsTheHubsThroughTheTunnel(t *testing.T) {
	require.NoError(t, secrets.Init())
	t.Setenv(secrets.KeywordsEnv, "")
	text := "api_key = " + keywordValue
	local := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	viaHub := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	viaHub.Header.Set(tunnel.ViaHeader, "x")
	out, _ := requestRedactor(local).Text([]byte(text))
	assert.Contains(t, string(out), keywordValue, "a local request: the local rules, as before")
	out, _ = requestRedactor(viaHub).Text([]byte(text))
	assert.NotContains(t, string(out), keywordValue, "through the tunnel: the keyword rules too")
	// The switch off: the local rules for the hub too.
	SetKeywordSwitch(func() bool { return false })
	t.Cleanup(func() { SetKeywordSwitch(nil) })
	out, _ = requestRedactor(viaHub).Text([]byte(text))
	assert.Contains(t, string(out), keywordValue, "the operator's switch off")
}

// A saved snippet and the shown text of a team artifact use the same rules.
func TestSavedSnippetAndShownTextFollowTheRequestAndTheHub(t *testing.T) {
	require.NoError(t, secrets.Init())
	t.Setenv(secrets.KeywordsEnv, "")
	text := "note: api_key = " + keywordValue
	assert.Contains(t, savedSnippet(text, secrets.Redactor()), keywordValue)
	assert.NotContains(t, savedSnippet(text, secrets.HubRedactor(nil)), keywordValue)
	assert.NotContains(t, shownText(text), keywordValue, "what a hub viewer matches on is what the reply shows")
}

// What is sent to another Coral through Coral Hub is not redacted by the hub: it is scanned with the hub's rules, the keyword rules too.
func TestScanOfWhatIsSentUsesTheKeywordRules(t *testing.T) {
	require.NoError(t, secrets.Init())
	t.Setenv(secrets.KeywordsEnv, "")
	SetKeywordSwitch(nil)
	assert.Equal(t, scanFound, scanMessageText([]byte("api_key = "+keywordValue)), "a keyword value in a message")
	assert.Equal(t, scanFound, scanSecrets([]byte("config:\napi_key = "+keywordValue+"\n")), "and in a file")
	assert.Equal(t, scanClean, scanSecrets([]byte("a file with nothing in it\n")))
	SetKeywordSwitch(func() bool { return false })
	t.Cleanup(func() { SetKeywordSwitch(nil) })
	assert.Equal(t, scanClean, scanMessageText([]byte("api_key = "+keywordValue)), "the operator's switch off")
}
