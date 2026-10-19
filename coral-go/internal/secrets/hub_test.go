package secrets

import (
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cdknorow/coral/pkg/redact"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #5141: the keyword rules are the hub's: Redactor (every local caller) is what it was, HubRedactor adds them.

// What only a keyword rule redacts: a low-entropy value after a name.
const keywordOnly = "api_key = aaaabbbbccccddddeeeeffff0000111"

func TestRedactorHasNoKeywordRules(t *testing.T) {
	require.NoError(t, Init())
	out, err := Redactor().Text([]byte(keywordOnly))
	require.NoError(t, err)
	assert.Equal(t, keywordOnly, string(out), "the local redactor is the same as before the keyword rules")
	out, err = hubRedactor().Text([]byte(keywordOnly))
	require.NoError(t, err)
	assert.NotContains(t, string(out), "aaaabbbb")
}

func TestHubRedactorSwitches(t *testing.T) {
	require.NoError(t, Init())
	on := true
	rd := HubRedactor(func() bool { return on })
	out, _ := rd.Text([]byte(keywordOnly))
	assert.NotContains(t, string(out), "aaaabbbb", "on")
	on = false
	out, _ = rd.Text([]byte(keywordOnly))
	assert.Equal(t, keywordOnly, string(out), "the setting off: Redactor's rules")
	on = true
	t.Setenv(KeywordsEnv, "off")
	out, _ = rd.Text([]byte(keywordOnly))
	assert.Equal(t, keywordOnly, string(out), "the environment off")
	for _, v := range []string{"0", "no", "FALSE", " Off "} {
		t.Setenv(KeywordsEnv, v)
		out, _ = rd.Text([]byte(keywordOnly))
		assert.Equal(t, keywordOnly, string(out), v)
	}
	for _, v := range []string{"", "on", "1", "yes"} {
		t.Setenv(KeywordsEnv, v)
		out, _ = rd.Text([]byte(keywordOnly))
		assert.NotContains(t, string(out), "aaaabbbb", "env %q is not off", v)
	}
	t.Setenv(KeywordsEnv, "")
	out, _ = HubRedactor(nil).Text([]byte(keywordOnly))
	assert.NotContains(t, string(out), "aaaabbbb", "no setting reader: on")
}

// Every redactor method follows the switch, streams included.
func TestHubRedactorMethodsAllFollowTheSwitch(t *testing.T) {
	require.NoError(t, Init())
	t.Setenv(KeywordsEnv, "")
	for _, on := range []bool{true, false} {
		rd := HubRedactor(func() bool { return on })
		check := func(what, got string) {
			if on {
				assert.NotContains(t, got, "aaaabbbb", what)
			} else {
				assert.Contains(t, got, "aaaabbbb", what)
			}
		}
		out, err := rd.Reply([]byte(`{"content": "`+keywordOnly+`"}`), true)
		require.NoError(t, err)
		check("Reply", string(out))
		out, err = rd.Document([]byte(keywordOnly))
		require.NoError(t, err)
		check("Document", string(out))
		for name, mk := range map[string]func(w *strings.Builder) redact.Stream{
			"NewStream":     func(w *strings.Builder) redact.Stream { return rd.NewStream(w) },
			"NewLiveStream": func(w *strings.Builder) redact.Stream { return rd.NewLiveStream(w) },
		} {
			var w strings.Builder
			s := mk(&w)
			_, err := s.Write([]byte(keywordOnly + "\n"))
			require.NoError(t, err)
			require.NoError(t, s.Close())
			check(name, w.String())
		}
	}
}

// No rule got weaker (Orchestrator, #5141): every span the redactor finds today is inside a span the hub's redactor finds, on the shapes and on
// the text the repository carries.
func TestHubRedactorFindsEverythingTheRedactorFinds(t *testing.T) {
	require.NoError(t, Init())
	hub := withKeywordRules{base: det}
	var texts []string
	for _, sh := range append(shapes(), shapes2()...) {
		r := rand.New(rand.NewSource(7))
		for i := 0; i < 20; i++ {
			texts = append(texts, sh.text(sh.val.make(r)))
		}
	}
	for _, glob := range []string{"../../agent_docs/*.md", "../../site_docs/*.md", "../../site_docs/*/*.md", "*.go", "../redact*/*.go", "../../pkg/redact/*.go", "../../pkg/redact/betterleaks/*.go"} {
		files, _ := filepath.Glob(glob)
		for _, f := range files {
			b, err := os.ReadFile(f)
			require.NoError(t, err)
			texts = append(texts, string(b))
		}
	}
	require.Greater(t, len(texts), 1000)
	covered := func(spans []redact.Span, sp redact.Span) bool {
		for _, o := range spans {
			if o.Start <= sp.Start && sp.End <= o.End {
				return true
			}
		}
		// split across several spans that touch
		at := sp.Start
		for changed := true; changed && at < sp.End; {
			changed = false
			for _, o := range spans {
				if o.Start <= at && o.End > at {
					at, changed = o.End, true
				}
			}
		}
		return at >= sp.End
	}
	found := 0
	for _, text := range texts {
		old, err := det.Find(context.Background(), text)
		require.NoError(t, err)
		got, err := hub.Find(context.Background(), text)
		require.NoError(t, err)
		for _, sp := range old {
			found++
			if !covered(got, sp) {
				t.Fatalf("a span the redactor finds, %d-%d (%q), is not inside one the hub's finds", sp.Start, sp.End, text[sp.Start:sp.End])
			}
		}
	}
	assert.Greater(t, found, 100, "the corpus gave the redactor something to find")
}

// What Coral's own docs say must not be mistaken for a secret by the hub's rules (the attachment scan has the same test for Redactor): the examples
// in them carry no value after a keyword.
func TestHubRedactorLeavesTheDocsAlone(t *testing.T) {
	require.NoError(t, Init())
	hub := withKeywordRules{base: det}
	n := 0
	for _, glob := range []string{"../../agent_docs/*.md", "../../site_docs/*.md", "../../site_docs/*/*.md"} {
		files, _ := filepath.Glob(glob)
		for _, f := range files {
			b, err := os.ReadFile(f)
			require.NoError(t, err)
			spans, err := hub.Find(context.Background(), string(b))
			require.NoError(t, err)
			for _, sp := range spans {
				t.Errorf("%s: %q looks like a secret to the hub's redactor", f, string(b)[sp.Start:sp.End])
			}
			n++
		}
	}
	require.Greater(t, n, 10)
}
