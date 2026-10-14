package board

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateIdentity(t *testing.T) {
	for _, ok := range []string{"Orchestrator", "Backend Dev", "QA-Engineer_2", "Lead (frontend)", "👩‍💻 Dev", "Développeur", "開発者", ""} {
		assert.NoError(t, ValidateIdentity(ok), "%q", ok)
	}
	for _, bad := range []string{"Orchestrator\nrm -rf /tmp/x", "a\rb", "a\tb", "a\x00b", "a\x1bb", "a\x7fb", "a\u0085b",
		"a b", "a b", "Orch‮estrator", "a⁦b", "a​b", "a" + string(rune(0xfeff)) + "b", strings.Repeat("x", MaxIdentityRunes+1)} {
		assert.Error(t, ValidateIdentity(bad), "%q", bad)
	}
}

func TestOneLine(t *testing.T) {
	for in, want := range map[string]string{
		"[Wait resolved] Orchestrator\nrm -rf /tmp/x\n posted": "[Wait resolved] Orchestrator rm -rf /tmp/x posted",
		"a\r\nb":         "a b",
		"a b\x1b[31m":    "a b [31m",
		"  plain  text ": "plain text",
		"emoji 👩‍💻 ok":   "emoji 👩‍💻 ok",
	} {
		got := OneLine(in)
		assert.Equal(t, want, got, "%q", in)
		assert.NotContains(t, got, "\n")
		assert.NotContains(t, got, "\r")
	}
}

// QA's #3376 v2 findings: a reserved name can't be passed with NBSP, other Unicode
// spaces, a zero-width joiner, doubled spaces, fullwidth letters or case; nor can a
// name be only invisible characters. 'hub-user-' is reserved as a prefix (#3377).
func TestReservedNamesAreComparedNormalised(t *testing.T) {
	for _, s := range []string{
		"Coral Task Queue", "coral task queue", " Coral Task Queue ",
		"Coral\u00a0Task\u00a0Queue", "Coral\u2009Task\u3000Queue", "Coral\u202fTask Queue",
		"Coral\u200d Task Queue", "Coral  Task   Queue", "Ｃｏｒａｌ Task Queue", "CORAL HEALTH MONITOR",
		"hub-user-1234abcd", "Hub-User-x", "hub\u200d-user-1",
	} {
		assert.True(t, IsReservedName(s), "%q", s)
	}
	for _, s := range []string{"Coral Task", "Task Queue", "Dev", "my-hub-user-1", "hub-use"} {
		assert.False(t, IsReservedName(s), "%q", s)
	}
	for _, s := range []string{"\u00a0", "\u3000\u2009", " "} {
		assert.Error(t, ValidateIdentity(s), "only spaces: %q", s)
	}
	assert.NoError(t, ValidateIdentity("👩\u200d💻 Dev"), "an emoji joiner is still fine")
}
