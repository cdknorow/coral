package teamhealth

import (
	"regexp"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/pkg/redact"
)

// secretShapes are the fixture's fake secrets, whole or in part.
var secretShapes = regexp.MustCompile(`ghp_[A-Za-z0-9]{4,}|AKIA[0-9A-Z]{4,}`)

// assertShownLine checks what #4176 rule 2 asks of every line of agent text Coral shows.
func assertShownLine(t *testing.T, out string) {
	t.Helper()
	assert.NotContains(t, out, "@", "output has no @")
	assert.False(t, secretShapes.MatchString(out), "output matches no secret: %q", out)
	for _, r := range out {
		assert.False(t, unicode.IsControl(r), "output has no control character: %q", out)
		assert.False(t, unicode.Is(unicode.Cf, r), "output has no format (bidi, zero-width) character: %q", out)
	}
	assert.LessOrEqual(t, utf8.RuneCountInString(out), MaxShown, "output len <= 120")
	assert.NotContains(t, out, "\n", "output is one line")
	assert.True(t, strings.HasPrefix(out, quotedLabel), "output is labelled as quoted agent text")
}

// Case C4 of Security's #4180 fixture: the agent's last message quotes an instruction to the
// orchestrator and carries secrets, bidi and control characters and a second line.
func TestShownTextOfTheHostileMessage(t *testing.T) {
	f := loadAdversarial(t)
	var text string
	for _, c := range f.Cases {
		if c.ID == "C4" {
			for _, e := range f.EventsOf(c) {
				if e.Text != "" {
					text = e.Text
				}
			}
		}
	}
	require.NotEmpty(t, text)
	out := ShownText(text, nil)
	assertShownLine(t, out)
	assert.Contains(t, out, "(at)Orchestrator", "the mention is neutralised, not dropped")
}

// Redaction comes before the cut: a secret that the 120-character cut would split is still
// redacted (cutting first would leave a prefix the redactor no longer recognises).
func TestShownTextRedactsBeforeTheCut(t *testing.T) {
	token := "ghp_" + strings.Repeat("A1b2C3d4E5", 4)
	for pad := 90; pad <= 118; pad++ {
		out := ShownText(strings.Repeat("x", pad)+" "+token, nil)
		assertShownLine(t, out)
	}
}

// Text the redactor can't check is never shown.
func TestShownTextFailsClosed(t *testing.T) {
	assert.Equal(t, NotShown, ShownText("anything", failingRedactor{}))
}

type failingRedactor struct{ redact.Redactor }

func (failingRedactor) Text([]byte) ([]byte, error) { return nil, redact.ErrUnchecked }

// Names in Coral's lines (#4176 note 1 on step 1): no @, no invisible characters, one line.
func TestNameForCoralLines(t *testing.T) {
	assert.Equal(t, "QA Engineer", Name("QA Engineer"))
	assert.Equal(t, "(at)Orchestrator evil", Name("@Orchestrator "+string(rune(0x202e))+"evil"+string(rune(0x200b))))
	assert.Equal(t, "first", Name("first"+string(rune(10))+"second"))
	assert.LessOrEqual(t, utf8.RuneCountInString(Name(strings.Repeat("n", 200))), 64)
}
