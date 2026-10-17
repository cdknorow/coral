package background

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/pkg/redact"
)

// knowledgeToken is a token of a shape Coral's secret rules know (a GitHub token).
const knowledgeToken = "ghp_A1b2C3d4E5A1b2C3d4E5A1b2C3d4E5F6g7H8"

// isolateKnowledgeEnv points every temp and home folder at a test folder (#4381).
func isolateKnowledgeEnv(t *testing.T) {
	iso := t.TempDir()
	for _, k := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(k, iso)
	}
	t.Setenv("HOME", filepath.Join(iso, "home"))
	t.Setenv("USERPROFILE", filepath.Join(iso, "home"))
	t.Setenv("CORAL_DATA_DIR", filepath.Join(iso, "data"))
}

// knowledgeFiles is every file in the team's knowledge folder, by name.
func knowledgeFiles(t *testing.T, coralDir, team string) map[string]string {
	dir := filepath.Join(coralDir, "agent_knowledge", team)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	out := map[string]string{}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		require.NoError(t, err)
		out[e.Name()] = string(b)
	}
	return out
}

func assertRedacted(t *testing.T, s, where string) {
	t.Helper()
	assert.NotContains(t, s, knowledgeToken, where)
	assert.Contains(t, s, redact.Marker, where)
}

// #4414: a token in an agent's transcript is redacted before the transcript is sent for distilling.
func TestDistillTranscriptIsRedacted(t *testing.T) {
	isolateKnowledgeEnv(t)
	f := newKnowledgeTeamFixture(t, "team")
	f.launch("s-ada", "Ada", true)
	f.transcript("s-ada", "export GITHUB_TOKEN="+knowledgeToken+" then make test")
	got := f.distilled()
	require.Contains(t, got, "Ada")
	assertRedacted(t, got["Ada"], "the transcript sent for distilling")
	assert.Contains(t, got["Ada"], "then make test", "the rest of the transcript is kept")
}

// Security's #4414 SHOULD: the transcript's head and tail cut can't leave a fragment of a token.
// Each message is redacted whole before the join and the cut; a token placed across the head cut
// and one across the tail cut leave no 8-character piece of themselves in what is distilled.
func TestTranscriptCutLeavesNoTokenFragment(t *testing.T) {
	isolateKnowledgeEnv(t)
	f := newKnowledgeTeamFixture(t, "team")
	f.launch("s-ada", "Ada", true)
	const prefix = "### Assistant\n" // how condenseMessages heads each message
	half := maxTranscriptChars / 2
	head := strings.Repeat("a", half-len(prefix)-20) + knowledgeToken + " end of the first message"
	tail := knowledgeToken + " " + strings.Repeat("b", half+20-len(knowledgeToken)-1)
	lines := []string{head, "a message in the middle", tail}
	var jsonl strings.Builder
	for _, l := range lines {
		jsonl.WriteString(`{"type":"assistant","message":{"content":"` + l + `"}}` + "\n")
	}
	path := filepath.Join(f.dir, "s-ada.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(jsonl.String()), 0o600))

	// Without the per-message pass the cut falls 20 characters into each token.
	full := prefix + head + "\n\n" + prefix + lines[1] + "\n\n" + prefix + tail
	require.Greater(t, len(full), maxTranscriptChars)
	require.Equal(t, half-20, strings.Index(full, knowledgeToken), "the first token straddles the head cut")
	require.Equal(t, len(full)-half-20, strings.LastIndex(full, knowledgeToken), "the second straddles the tail cut")

	got := f.distilled()["Ada"]
	require.NotEmpty(t, got)
	for i := 0; i+8 <= len(knowledgeToken); i++ {
		assert.NotContains(t, got, knowledgeToken[i:i+8], "a fragment of the token")
	}
	assert.Contains(t, got, redact.Marker)
}

// #4414: what the model returns is redacted before it is written: each agent's file and index.md.
func TestDistilledKnowledgeIsRedactedOnDisk(t *testing.T) {
	isolateKnowledgeEnv(t)
	dir := t.TempDir()
	call := func(prompt, content string) (string, error) {
		if strings.HasPrefix(prompt, "You are analyzing collaboration patterns") {
			return "Ada hands Bob the token " + knowledgeToken, nil
		}
		return "## Tools & Commands\ngh auth login --with-token " + knowledgeToken, nil
	}
	b, err := distillAgents(newTestBundle("team"), distillTestAgents(), call, dir, nil)
	require.NoError(t, err)
	files := knowledgeFiles(t, dir, "team")
	require.Contains(t, files, "ada.md")
	require.Contains(t, files, "index.md")
	for name, md := range files {
		assert.NotContains(t, md, knowledgeToken, name)
	}
	assertRedacted(t, files["ada.md"], "ada.md")
	for name, c := range b.Agents {
		assert.NotContains(t, c.FinalMD, knowledgeToken, "the returned bundle: %s", name)
	}
}

// #4414: a bundle is written with its index and every agent's document redacted.
func TestKnowledgeBundleIsRedactedOnDisk(t *testing.T) {
	isolateKnowledgeEnv(t)
	dir := t.TempDir()
	require.NoError(t, writeKnowledgeBundle(dir, &KnowledgeBundle{TeamName: "team", IndexMD: "# Team " + knowledgeToken,
		Agents: map[string]*AgentConcept{"Ada": {AgentName: "Ada", FinalMD: "use " + knowledgeToken}}}))
	files := knowledgeFiles(t, dir, "team")
	assertRedacted(t, files["index.md"], "index.md")
	assertRedacted(t, files["ada.md"], "ada.md")
}

// #4414: knowledge saved from the dashboard or imported with a team is redacted before it is written.
func TestSavedAndImportedKnowledgeIsRedacted(t *testing.T) {
	isolateKnowledgeEnv(t)
	dir := t.TempDir()
	redacted, err := WriteKnowledgeFile(dir, "team", "Ada", "token: "+knowledgeToken)
	require.NoError(t, err)
	assert.True(t, redacted, "the caller is told a secret was replaced")
	redacted, err = WriteKnowledgeFile(dir, "team", "Bob", "no secret here")
	require.NoError(t, err)
	assert.False(t, redacted)
	require.NoError(t, WriteKnowledgeIndex(dir, "team", "# Team\n"+knowledgeToken))
	files := knowledgeFiles(t, dir, "team")
	assertRedacted(t, files["ada.md"], "ada.md")
	assertRedacted(t, files["index.md"], "index.md")
}

// #4414: a file written before knowledge was redacted is redacted when it is read: shown on the
// dashboard (ReadKnowledgeBundle, LoadAgentKnowledge) or put in a launch prompt (LoadAgentKnowledge).
func TestLegacyKnowledgeIsRedactedWhenRead(t *testing.T) {
	isolateKnowledgeEnv(t)
	dir := t.TempDir()
	kd := filepath.Join(dir, "agent_knowledge", "team")
	require.NoError(t, os.MkdirAll(kd, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(kd, "index.md"), []byte("# Team "+knowledgeToken), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(kd, "ada.md"), []byte("use "+knowledgeToken), 0o600))

	assertRedacted(t, LoadAgentKnowledge(dir, "team", "Ada"), "LoadAgentKnowledge")
	b, err := ReadKnowledgeBundle(dir, "team")
	require.NoError(t, err)
	assertRedacted(t, b.IndexMD, "the bundle's index")
	require.Contains(t, b.Agents, "ada")
	assertRedacted(t, b.Agents["ada"].FinalMD, "the bundle's ada")
}

type failingRedactor struct{ redact.Redactor }

func (failingRedactor) Text([]byte) ([]byte, error) { return nil, redact.ErrUnchecked }

// #4414: knowledge that can't be checked for secrets is neither written, nor shown, nor sent.
func TestUncheckedKnowledgeFailsClosed(t *testing.T) {
	isolateKnowledgeEnv(t)
	dir := t.TempDir()
	kd := filepath.Join(dir, "agent_knowledge", "team")
	require.NoError(t, os.MkdirAll(kd, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(kd, "index.md"), []byte("# Team"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(kd, "bob.md"), []byte("old bob"), 0o600))

	old := knowledgeRedactor
	knowledgeRedactor = func() redact.Redactor { return failingRedactor{redact.Rules} }
	t.Cleanup(func() { knowledgeRedactor = old })

	_, err := WriteKnowledgeFile(dir, "team", "Ada", "x")
	assert.Error(t, err)
	assert.Error(t, WriteKnowledgeIndex(dir, "team", "new index"))
	assert.Error(t, writeKnowledgeBundle(dir, &KnowledgeBundle{TeamName: "team", IndexMD: "new index",
		Agents: map[string]*AgentConcept{"Bob": {AgentName: "Bob", FinalMD: "new bob"}}}))
	files := knowledgeFiles(t, dir, "team")
	assert.Equal(t, map[string]string{"index.md": "# Team", "bob.md": "old bob"}, files, "nothing written")

	assert.Empty(t, LoadAgentKnowledge(dir, "team", "Bob"))
	_, err = ReadKnowledgeBundle(dir, "team")
	assert.Error(t, err)
	assert.Empty(t, redactedTranscript("a transcript"), "an unchecked transcript is not sent")
}
