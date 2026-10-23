package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #6268: a message that opens with the editor extension's context (<ide_opened_file>, <ide_selection>) keeps the text the person typed after it; a message that is only context is skipped, as before.
const ideOpened = "<ide_opened_file>The user opened the file /work/proj/main.go in the IDE. This may or may not be related to the current task.</ide_opened_file>"

const ideSelected = "<ide_selection>The user selected the lines 1 to 3 from /work/proj/main.go:\nfunc main() {}</ide_selection>"

func TestTypedTextKeepsWhatFollowsTheIDEContext(t *testing.T) {
	for _, c := range []struct {
		name, in, want string
		ok             bool
	}{
		{"typed text after one block", ideOpened + "\nwhich folder are you in?", "\nwhich folder are you in?", true},
		{"after two blocks", ideOpened + "<ide_selection>The user selected the lines 1 to 3 from /work/proj/main.go:\nfunc main() {}</ide_selection> fix this", " fix this", true},
		{"after a block and a newline", ideOpened + "\n\nrefactor it", "\n\nrefactor it", true},
		{"only context", ideOpened, "", false},
		{"only context, two blocks", ideOpened + "\n" + ideOpened, "", false},
		{"an unclosed block is not context", "<ide_opened_file>never closed which folder?", "", false},
		{"mismatched tags are not one block", "<ide_opened_file>x</ide_selection> typed", "", false},
		{"no context", "which folder are you in?", "which folder are you in?", true},
		{"a block after the typed text", "fix this\n" + ideSelected, "fix this\n", true},
		{"blocks before and after", ideOpened + " fix this " + ideSelected + "\n" + ideOpened, " fix this ", true},
		{"only trailing-looking context", ideSelected, "", false},
		{"a close tag with no open is left as typed", "fix this</ide_selection>", "fix this</ide_selection>", true},
		{"a typed tag name that is not IDE context stays", "use <b>bold</b>", "use <b>bold</b>", true},
		{"the CLI's own text still skipped", "<system-reminder>x</system-reminder> typed", "", false},
		{"context then the CLI's own text", ideOpened + "<command-name>/model</command-name>", "", false},
	} {
		got, ok := typedText(c.in)
		assert.Equal(t, c.ok, ok, c.name)
		if ok {
			assert.Equal(t, c.want, got, c.name)
		}
	}
}

func TestEachIndexerKeepsTheTypedTextAfterIDEContext(t *testing.T) {
	const typed = "which folder are you in?"
	dir := t.TempDir()

	claude := filepath.Join(dir, "proj", "00000000-0000-4000-8000-000000006268.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(claude), 0o755))
	require.NoError(t, os.WriteFile(claude, []byte(`{"type":"user","timestamp":"2026-10-23T00:00:00Z","message":{"role":"user","content":"`+ideOpened+`"}}`+"\n"+
		`{"type":"user","timestamp":"2026-10-23T00:00:01Z","message":{"role":"user","content":"`+ideOpened+`\n`+typed+`"}}`+"\n"+
		`{"type":"assistant","timestamp":"2026-10-23T00:00:02Z","message":{"content":[{"type":"text","text":"The project folder."}]}}`+"\n"), 0o644))
	got, err := (&ClaudeAgent{}).ExtractSessions(dir, nil)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, typed, got[0].FirstUserMessage, "claude: a context-only entry is skipped, the next keeps its typed text")

	codex := filepath.Join(dir, "rollout-2026-10-23T00-00-00-019e90eb-a08a-7511-a410-000000006268.jsonl")
	require.NoError(t, os.WriteFile(codex, []byte(`{"timestamp":"2026-10-23T00:00:01.000Z","type":"event_msg","payload":{"type":"user_message","message":"`+ideOpened+`\n`+typed+`","images":[]}}`+"\n"+
		`{"timestamp":"2026-10-23T00:00:02.000Z","type":"event_msg","payload":{"type":"agent_message","message":"The project folder.","phase":"final_answer"}}`+"\n"), 0o644))
	cs, err := parseCodexSession(codex, 1)
	require.NoError(t, err)
	require.NotNil(t, cs)
	assert.Equal(t, typed, cs.FirstUserMessage, "codex")

	agy := filepath.Join(dir, "conv.jsonl")
	require.NoError(t, os.WriteFile(agy, []byte(`{"type":"USER_INPUT","content":"`+ideOpened+`\n`+typed+`","created_at":"2026-10-23T00:00:00Z"}`+"\n"), 0o644))
	as, err := parseAgyTranscriptSession("conv", agy, 1)
	require.NoError(t, err)
	require.NotNil(t, as)
	assert.Equal(t, typed, as.FirstUserMessage, "antigravity")

	gem := filepath.Join(dir, "session-abc.json")
	require.NoError(t, os.WriteFile(gem, []byte(`[{"role":"user","timestamp":"2026-10-23T00:00:00Z","parts":[{"text":"`+ideOpened+`"},{"text":"`+typed+`"}]},{"role":"model","timestamp":"2026-10-23T00:00:01Z","parts":[{"text":"The project folder."}]}]`), 0o644))
	gs, err := parseGeminiSession(gem, 1)
	require.NoError(t, err)
	require.NotNil(t, gs)
	assert.Equal(t, typed, gs.FirstUserMessage, "gemini")

	pi := filepath.Join(dir, "pi.jsonl")
	require.NoError(t, os.WriteFile(pi, []byte(`{"type":"session","id":"pi-6268"}`+"\n"+`{"type":"message","role":"user","content":"`+ideOpened+` `+typed+`","timestamp":"2026-10-23T00:00:00Z"}`+"\n"), 0o644))
	ps, err := parsePiSession(pi, 1)
	require.NoError(t, err)
	require.NotNil(t, ps)
	assert.Equal(t, typed, ps.FirstUserMessage, "pi")
}

// Redacted before it is cut, as every first message is: a secret typed after the context is not stored cut.
func TestASecretAfterIDEContextIsNotStoredCut(t *testing.T) {
	secret := "ghp_aB3dE5gH7jaB3dE5gH7jaB3dE5gH7jkLmNoP"
	text := ideOpened + " " + string(make([]byte, 0)) + "token is " + secret + " thanks"
	got, ok := typedText(text)
	require.True(t, ok)
	out := clipFirstMessage(got)
	assert.NotContains(t, out, secret[:12])
}

// The same with the context AFTER the typed text: no indexer keeps the block or the selected code in the title.
func TestEachIndexerDropsIDEContextAfterTheTypedText(t *testing.T) {
	const typed = "which folder are you in?"
	dir := t.TempDir()

	claude := filepath.Join(dir, "proj", "00000000-0000-4000-8000-000000006278.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(claude), 0o755))
	require.NoError(t, os.WriteFile(claude, []byte(`{"type":"user","timestamp":"2026-10-23T00:00:01Z","message":{"role":"user","content":"`+typed+` <ide_selection>x</ide_selection>"}}`+"\n"+
		`{"type":"assistant","timestamp":"2026-10-23T00:00:02Z","message":{"content":[{"type":"text","text":"The project folder."}]}}`+"\n"), 0o644))
	got, err := (&ClaudeAgent{}).ExtractSessions(dir, nil)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, typed, got[0].FirstUserMessage, "claude")

	codex := filepath.Join(dir, "rollout-2026-10-23T00-00-00-019e90eb-a08a-7511-a410-000000006278.jsonl")
	require.NoError(t, os.WriteFile(codex, []byte(`{"timestamp":"2026-10-23T00:00:01.000Z","type":"event_msg","payload":{"type":"user_message","message":"`+typed+` <ide_selection>x</ide_selection>","images":[]}}`+"\n"+
		`{"timestamp":"2026-10-23T00:00:02.000Z","type":"event_msg","payload":{"type":"agent_message","message":"The project folder.","phase":"final_answer"}}`+"\n"), 0o644))
	cs, err := parseCodexSession(codex, 1)
	require.NoError(t, err)
	require.NotNil(t, cs)
	assert.Equal(t, typed, cs.FirstUserMessage, "codex")

	agy := filepath.Join(dir, "conv.jsonl")
	require.NoError(t, os.WriteFile(agy, []byte(`{"type":"USER_INPUT","content":"`+typed+` <ide_selection>x</ide_selection>","created_at":"2026-10-23T00:00:00Z"}`+"\n"), 0o644))
	as, err := parseAgyTranscriptSession("conv", agy, 1)
	require.NoError(t, err)
	require.NotNil(t, as)
	assert.Equal(t, typed, as.FirstUserMessage, "antigravity")

	gem := filepath.Join(dir, "session-abc.json")
	require.NoError(t, os.WriteFile(gem, []byte(`[{"role":"user","timestamp":"2026-10-23T00:00:00Z","parts":[{"text":"`+typed+`"},{"text":"<ide_selection>x</ide_selection>"}]},{"role":"model","timestamp":"2026-10-23T00:00:01Z","parts":[{"text":"The project folder."}]}]`), 0o644))
	gs, err := parseGeminiSession(gem, 1)
	require.NoError(t, err)
	require.NotNil(t, gs)
	assert.Equal(t, typed, gs.FirstUserMessage, "gemini")

	pi := filepath.Join(dir, "pi.jsonl")
	require.NoError(t, os.WriteFile(pi, []byte(`{"type":"session","id":"pi-6278"}`+"\n"+`{"type":"message","role":"user","content":"`+typed+` <ide_selection>x</ide_selection>","timestamp":"2026-10-23T00:00:00Z"}`+"\n"), 0o644))
	ps, err := parsePiSession(pi, 1)
	require.NoError(t, err)
	require.NotNil(t, ps)
	assert.Equal(t, typed, ps.FirstUserMessage, "pi")
}
