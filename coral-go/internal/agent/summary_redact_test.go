package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Security's finding on #6207 (the same class as #3733): a History summary was cut to 200 bytes BEFORE it was redacted, so a secret that straddled the cut was stored as part of a token the detector no longer
// recognises (a GitHub token short of its last character passed unchanged). The summary is redacted whole and then cut where no token can be split, at every offset of the cut, for each source.
func TestASecretAtTheSummaryCutIsNotStoredCut(t *testing.T) {
	secrets := map[string]string{
		"a GitHub token":    "ghp_" + strings.Repeat("aB3dE5gH7j", 3) + "kLmNoP",
		"an AWS access key": "AKIAJ7QX2LNB4MWT9RZD", // not the documented EXAMPLE key, which the redactor allow-lists
		"an Anthropic key":  "sk-ant-api03-" + strings.Repeat("Zx9Qw8Er7T", 4),
	}
	longestRun := func(secret, stored string) int {
		longest := 0
		for i := 0; i < len(secret); i++ {
			for j := i + longest + 1; j <= len(secret); j++ {
				if !strings.Contains(stored, secret[i:j]) {
					break
				}
				longest = j - i
			}
		}
		return longest
	}
	for name, secret := range secrets {
		for pad := 200 - len(secret); pad <= 200; pad++ { // the cut at byte 200 lands at every offset inside the secret, and at its ends
			text := strings.Repeat("x", pad-1) + " " + secret + " and then some more words after it"
			if n := longestRun(secret, redactedCut(text, 200)); n > 8 {
				t.Fatalf("%s at pad %d: %d characters of the secret are stored", name, pad, n)
			}
			if n := longestRun(secret, cursorSummary(text)); n > 8 {
				t.Fatalf("%s at pad %d (Claude's cursor): %d characters of the secret are stored", name, pad, n)
			}
		}
	}
	t.Run("a PEM key at the start is seen whole", func(t *testing.T) {
		pem := "-----BEGIN RSA PRIVATE KEY-----\n" + strings.Repeat("MIIEowIBAAKCAQEA7x9aB3dE5gH7jkLmNoPqRsTuVwXyZ0123456789\n", 6) + "-----END RSA PRIVATE KEY-----"
		got := redactedCut(pem+" why does it fail?", 200)
		assert.NotContains(t, got, "MIIEowIBAAKCAQEA7x9a")
	})
	t.Run("ordinary text is cut as before", func(t *testing.T) {
		got := redactedCut(strings.Repeat("word ", 100), 200)
		assert.LessOrEqual(t, len(got), 200)
		assert.True(t, strings.HasPrefix(got, "word word"))
		assert.Equal(t, "short", redactedCut("short", 200))
	})
}

// Each indexer keeps the safe summary and stamps the parser version, so rows of the older parser are read again.
func TestEveryIndexerRedactsItsSummaryAndStampsTheVersion(t *testing.T) {
	secret := "ghp_" + strings.Repeat("aB3dE5gH7j", 3) + "kLmNoP"
	pad := strings.Repeat("x", 200-len(secret)+20) + " " + secret + " tail"
	dir := t.TempDir()
	check := func(name string, s *IndexedSession) {
		t.Helper()
		require.NotNil(t, s, name)
		assert.Equal(t, HistoryIndexVersion, s.IndexVersion, name)
		assert.LessOrEqual(t, len(s.DisplaySummary), 200, name)
		assert.NotContains(t, s.DisplaySummary, secret[:12], name+": no long run of the token is kept")
	}
	claude, err := (&ClaudeAgent{}).ExtractSessions(dir, nil)
	require.NoError(t, err)
	require.Empty(t, claude)
	p := filepath.Join(dir, "proj", "00000000-0000-4000-8000-000000006207.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(`{"type":"user","timestamp":"2026-10-23T00:00:00Z","message":{"role":"user","content":"hi"}}`+"\n"+
		`{"type":"assistant","timestamp":"2026-10-23T00:00:01Z","message":{"content":[{"type":"text","text":"`+pad+`"}]}}`+"\n"), 0o644))
	got, err := (&ClaudeAgent{}).ExtractSessions(dir, nil)
	require.NoError(t, err)
	require.Len(t, got, 1)
	check("claude", &got[0])

	codex := filepath.Join(dir, "rollout-2026-10-23T00-00-00-019e90eb-a08a-7511-a410-000000006207.jsonl")
	require.NoError(t, os.WriteFile(codex, []byte(`{"timestamp":"2026-10-23T00:00:01.000Z","type":"event_msg","payload":{"type":"user_message","message":"hello","images":[]}}`+"\n"+
		`{"timestamp":"2026-10-23T00:00:02.000Z","type":"event_msg","payload":{"type":"agent_message","message":"`+pad+`","phase":"final_answer"}}`+"\n"), 0o644))
	cs, err := parseCodexSession(codex, 1)
	require.NoError(t, err)
	check("codex", cs)

	agy := filepath.Join(dir, "conv.jsonl")
	require.NoError(t, os.WriteFile(agy, []byte(`{"type":"PLANNER_RESPONSE","content":"`+pad+`","created_at":"2026-10-23T00:00:00Z"}`+"\n"), 0o644))
	as, err := parseAgyTranscriptSession("conv", agy, 1)
	require.NoError(t, err)
	check("agy", as)

	gem := filepath.Join(dir, "session-abc.json")
	require.NoError(t, os.WriteFile(gem, []byte(`[{"role":"model","timestamp":"2026-10-23T00:00:00Z","parts":[{"text":"`+pad+`"}]}]`), 0o644))
	gs, err := parseGeminiSession(gem, 1)
	require.NoError(t, err)
	check("gemini", gs)

	pi := filepath.Join(dir, "pi.jsonl")
	require.NoError(t, os.WriteFile(pi, []byte(`{"type":"session","id":"pi-6207"}`+"\n"+`{"type":"message","role":"user","content":"`+pad+`","timestamp":"2026-10-23T00:00:00Z"}`+"\n"), 0o644))
	ps, err := parsePiSession(pi, 1)
	require.NoError(t, err)
	check("pi", ps)
}
