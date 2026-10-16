package agentmodels

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	require.NoError(t, err)
	return b
}

func values(models []Model) []string {
	var v []string
	for _, m := range models {
		v = append(v, m.Value)
	}
	return v
}

// Recorded from Claude Code 2.1.301's initialize reply (models trimmed to six, the account
// block replaced by a fake one): only the models are kept, never the account.
func TestParseClaudeInitialize(t *testing.T) {
	raw := fixture(t, "claude-2.1.301-initialize.jsonl")
	models, err := ParseClaudeInitialize(bytes.NewReader(raw))
	require.NoError(t, err)
	assert.Equal(t, []string{"default", "opus", "fable", "sonnet", "haiku", "claude-haiku-4-5-20251001"}, values(models))
	assert.Equal(t, "Opus 5.5", models[1].DisplayName)
	assert.Contains(t, models[1].Efforts, "max")
	out, _ := json.Marshal(models)
	for _, secret := range []string{"someone@example.test", "Example Org", "subscriptionType", "4242"} {
		assert.NotContains(t, string(out), secret, "the reply's account details are never kept")
	}

	_, err = ParseClaudeInitialize(strings.NewReader(`{"type":"system"}` + "\n"))
	assert.Error(t, err, "no reply")
	_, err = ParseClaudeInitialize(strings.NewReader(`{"type":"control_response","response":{"subtype":"error","error":"x"}}` + "\n"))
	assert.Error(t, err, "a refused initialize")
}

// Recorded from codex-cli 0.155.1's codex debug models (fields trimmed): hidden models are
// left out, reasoning levels become efforts.
func TestParseCodexCatalog(t *testing.T) {
	models, err := ParseCodexCatalog(fixture(t, "codex-0.155.1-debug-models.json"))
	require.NoError(t, err)
	assert.Equal(t, []string{"gpt-6-astra", "gpt-6-sol", "gpt-6-luna", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"}, values(models))
	assert.NotContains(t, values(models), "gpt-reserve", "visibility hide")
	assert.Equal(t, "GPT-6-Astra", models[0].DisplayName)
	assert.Contains(t, models[0].Efforts, "medium")
	_, err = ParseCodexCatalog([]byte(`not json`))
	assert.Error(t, err)
}

// Recorded from agy 1.3.5's agy models.
func TestParseAgyModels(t *testing.T) {
	models, err := ParseAgyModels(fixture(t, "agy-1.3.5-models.txt"))
	require.NoError(t, err)
	assert.Equal(t, "gemini-3.8-flash-high", models[0].Value)
	assert.Equal(t, "Gemini 3.8 Flash (High)", models[0].DisplayName)
	assert.Contains(t, values(models), "claude-sonnet-4-6")
}

// Recorded from pi 0.73.1's pi --list-models (a dummy provider key, no network): the value is
// provider/model; pi's sentence for "no provider" is no models, not a model.
func TestParsePiModels(t *testing.T) {
	models, err := ParsePiModels(fixture(t, "pi-0.73.1-list-models.txt"))
	require.NoError(t, err)
	assert.Equal(t, "openai/gpt-4", models[0].Value)
	assert.Equal(t, "gpt-4 (openai)", models[0].DisplayName)
	_, err = ParsePiModels([]byte("No models available. Use /login to log into a provider via OAuth or API key. See:\n  /x/docs/providers.md\n"))
	assert.ErrorIs(t, err, ErrNoModels)
}

// What a CLI prints can't become anything but a plain model id (#4065, Security's condition 3):
// no leading dash, no spaces or control characters, bounded; text is one plain line.
func TestModelValuesAreOnlyModelIDs(t *testing.T) {
	for _, v := range []string{"opus", "claude-opus-5-5", "opus[1m]", "gpt-6-astra", "openai/gpt-4o", "sonnet:high", "claude-haiku-4-5-20251001"} {
		assert.True(t, ValidModelValue(v), v)
	}
	for _, v := range []string{"", "-c", "--dangerously-skip-permissions", "a b", "a\nb", "a;rm", "$(x)", "`x`", "a\x00b", strings.Repeat("a", 129), "<synthetic>"} {
		assert.False(t, ValidModelValue(v), "%q", v)
	}
	models, err := ParseAgyModels([]byte("--yolo\tbad\nok-1\tName\x1b[31m red\u202e\nok-1\tdup\n" + strings.Repeat("x", 300) + "\tlong\n"))
	require.NoError(t, err)
	assert.Equal(t, []string{"ok-1"}, values(models))
	assert.Equal(t, "Name [31m red", models[0].DisplayName, "control and format characters become spaces")
}
