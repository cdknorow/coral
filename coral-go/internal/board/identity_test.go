package board

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateIdentity(t *testing.T) {
	for _, ok := range []string{"Orchestrator", "Backend Dev", "QA-Engineer_2", "Lead (frontend)", "👩\u200d💻 Dev", "Développeur", "開発者", ""} {
		assert.NoError(t, ValidateIdentity(ok), "%q", ok)
	}
	for _, bad := range []string{"Orchestrator\nrm -rf /tmp/x", "a\rb", "a\tb", "a\x00b", "a\x1bb", "a\x7fb", "a\u0085b",
		"a\u2028b", "a\u2029b", "Orch\u202eestrator", "a\u2066b", "a\u200bb", "a" + string(rune(0xfeff)) + "b", strings.Repeat("x", MaxIdentityRunes+1)} {
		assert.Error(t, ValidateIdentity(bad), "%q", bad)
	}
}

func TestOneLine(t *testing.T) {
	for in, want := range map[string]string{
		"[Wait resolved] Orchestrator\nrm -rf /tmp/x\n posted": "[Wait resolved] Orchestrator rm -rf /tmp/x posted",
		"a\r\nb":            "a b",
		"a\u2028b\x1b[31m":  "a b [31m",
		"  plain  text ":    "plain text",
		"emoji 👩\u200d💻 ok": "emoji 👩\u200d💻 ok",
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

// Look-alike letters from other scripts (#4207, Security's #4175 note 2): a name spelled with a
// Cyrillic or Greek letter, a roman numeral or a mathematical form that reads like a Latin one is
// the same name for every identity and reserved-name check. The letters are written as runes, not
// typed, so the source shows what is being tested.
func TestLookAlikeSpellingsAreTheSameName(t *testing.T) {
	cyrA, cyrO, cyrE := string(rune(0x430)), string(rune(0x43e)), string(rune(0x435))
	cyrCapO, cyrCapC, cyrCapS, cyrCapH := string(rune(0x41e)), string(rune(0x421)), string(rune(0x405)), string(rune(0x41d))
	grEta, grOmicron := string(rune(0x397)), string(rune(0x3bf))
	romanI := string(rune(0x2160))
	for _, c := range []struct{ want, spelled string }{
		{"Coral", "Cor" + cyrA + "l"},
		{"Coral", cyrCapC + cyrO + "r" + cyrA + "l"},
		{"Coral", "C" + grOmicron + "ral"},
		{"Coral", "Cora" + romanI},
		{"System", "Syst" + cyrE + "m"},
		{"System", cyrCapS + "yst" + cyrE + "m"},
		{"Health", cyrCapH + cyrE + "alth"},
		{"Health", grEta + "ealth"},
		{"Health", "H" + cyrE + "alth"},
		{"Orchestrator", cyrCapO + "rchestrator"},
		{"Orchestrator", "Orchestrat" + cyrO + "r"},
		{"Operator", cyrCapO + "perator"},
		{"Operator", "Op" + cyrE + "rator"},
		{"Operator", "Operat" + grOmicron + "r"},
		{"Security Reviewer", "S" + cyrE + "curity R" + cyrE + "vi" + cyrE + "w" + cyrE + "r"},
		{"Coral Task Queue", cyrCapC + "oral Task Queue"},
	} {
		assert.True(t, SameIdentity(c.want, c.spelled), "%q is %q", c.spelled, c.want)
	}
	// Names that are not look-alikes stay different, and a plain ASCII name is untouched.
	for _, c := range [][2]string{{"Coral", "Corall"}, {"Dev", "Dave"}, {"QA1", "QAl"}, {"Dev 1", "Dev 2"}, {"Coral", "Cora"}} {
		assert.False(t, SameIdentity(c[0], c[1]), "%q and %q", c[0], c[1])
	}
	assert.Equal(t, "plain name", foldConfusables("plain name"))
	assert.Equal(t, "日本語", foldConfusables("日本語"), "a script without a look-alike is kept")
}

func TestReservedNamesCatchLookAlikes(t *testing.T) {
	cyrA, cyrE, cyrCapC, cyrO := string(rune(0x430)), string(rune(0x435)), string(rune(0x421)), string(rune(0x43e))
	grEta := string(rune(0x397))
	for _, s := range []string{
		"Cor" + cyrA + "l", cyrCapC + "oral", "Syst" + cyrE + "m", "H" + cyrE + "alth", grEta + "ealth",
		"CoraI", "Cora1", "Cora|", "C0ral", "Syst" + cyrE + "m ", "hub-user-1234abcd",
		cyrCapC + "oral Task Queue", "C" + cyrO + "ral Hub", "Coral Task Queue",
	} {
		assert.True(t, IsReservedName(s), "%q", s)
	}
	for _, s := range []string{"Coral Task", "Dev", "Heaith", "Systems", "Coralie", "QA", "Security Reviewer"} {
		assert.False(t, IsReservedName(s), "%q", s)
	}
}
