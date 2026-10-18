package routes

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// #4569: where a CLI is missing, the prerequisite check says how to install it and links the vendor's own page; agy also says what
// to do about ~/.local/bin. A CLI that is found carries none of it.
func TestCLICheckMissingCarriesTheOfficialInstallPage(t *testing.T) {
	cli := useFakeCLIs(t)
	h := newSystemHandlerForTest(t)
	for typ, want := range map[string]string{
		"claude": "https://code.claude.com/docs/en/overview", "codex": "https://github.com/openai/codex",
		"agy": "https://antigravity.google", "antigravity": "https://antigravity.google", "gemini": "https://antigravity.google", "pi": "https://pi.dev",
	} {
		r := cliCheck(t, h, "type="+typ)
		assert.Equal(t, false, r["found"], typ)
		assert.Equal(t, want, r["install_url"], typ)
		assert.NotEmpty(t, r["install_command"], typ)
		if typ == "agy" || typ == "antigravity" || typ == "gemini" {
			assert.Equal(t, "curl -fsSL https://antigravity.google/cli/install.sh | bash", r["install_command"], typ)
			assert.Contains(t, r["install_note"], "~/.local/bin", typ)
		} else {
			assert.NotContains(t, r, "install_note", typ)
		}
	}
	cli.install(t, "claude", `echo 2.1.0`)
	r := cliCheck(t, h, "type=claude")
	assert.Equal(t, true, r["found"])
	assert.NotContains(t, r, "install_url")
}
