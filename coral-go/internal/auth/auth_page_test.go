package auth

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// #3948: the /auth page has three states, chosen by the server, and never carries a secret or runs inline script.
func TestAuthPageStates(t *testing.T) {
	set := AuthPage(PageState{PasswordSet: true, Local: true})
	assert.Contains(t, set, `id="signin-form"`)
	assert.Contains(t, set, `autocomplete="current-password"`, "a password manager fills it")
	assert.Contains(t, set, `autocomplete="username"`, "with a username to file it under")
	assert.Contains(t, set, "Keep me signed in on this browser")
	assert.Regexp(t, `<input type="checkbox" id="keep">`, set, "unchecked by default (the operator's rule)")
	assert.Contains(t, set, "Forgot password?")
	assert.Contains(t, set, "coral reset-password", "the local-only reset, named")
	assert.Contains(t, set, `id="panel-key"`, "an API key still works")
	assert.NotContains(t, set, `id="create-form"`)
	assert.NotContains(t, set, `id="setup-code"`)

	local := AuthPage(PageState{Local: true})
	assert.Contains(t, local, `id="create-form"`)
	assert.Equal(t, 2, strings.Count(local, `autocomplete="new-password"`), "password and confirmation")
	assert.NotContains(t, local, `id="setup-code"`, "from this computer nothing else is needed")
	assert.NotContains(t, local, `id="signin-form"`)

	remote := AuthPage(PageState{})
	assert.Contains(t, remote, `id="create-form"`)
	assert.Contains(t, remote, `id="setup-code"`, "from another computer the one-time setup code is asked for")
	assert.Contains(t, remote, "coral setup-code", "and says where to find it")
	assert.Contains(t, remote, "on that computer (or look at the console if you started Coral from a terminal)")
	assert.NotContains(t, remote, "startup log", "the code is never in a log, so the page does not send anyone to one")

	assert.Contains(t, AuthPage(PageState{PasswordSet: true, Strict: true}), "the tray and coral open sign in by themselves")
	assert.NotContains(t, AuthPage(PageState{PasswordSet: true}), "the tray and coral open sign in by themselves")
}

func TestAuthPageCarriesNoSecretAndNoInlineScript(t *testing.T) {
	for _, s := range []PageState{{PasswordSet: true}, {Local: true}, {}, {PasswordSet: true, Strict: true}} {
		p := AuthPage(s)
		assert.Equal(t, 1, strings.Count(p, `value="`), "only the hidden username stub has a value: %+v", s)
		assert.Contains(t, p, `name="username" value="coral"`)
		assert.Contains(t, p, `<script type="module" src="/static/auth_page.js"></script>`)
		assert.Contains(t, p, `<script src="/static/theme_init.js"></script>`)
		assert.Equal(t, 2, strings.Count(p, "<script"), "two external scripts and nothing inline: %+v", s)
		assert.False(t, regexp.MustCompile(`<script[^>]*>[^<]+</script>`).MatchString(p), "no inline script body")
		assert.False(t, regexp.MustCompile(`(?i)\son[a-z]+=`).MatchString(p), "no inline event handler")
		assert.Contains(t, p, `name="viewport"`, "works on phones")
		assert.Contains(t, p, `name="referrer" content="no-referrer"`)
		assert.Contains(t, p, "~/.coral/api_key", "the key panel says where the key is, never its value")
		assert.Contains(t, p, `type="password" autocomplete="off"`, "the key is a password-type field")
	}
}

// The three pages are checked in under testdata, and the browser suite tests/frontend/auth_page_browser.test.js loads them,
// so what the browser exercises is what the server renders. UPDATE_GOLDEN=1 go test ./internal/auth rewrites them.
func TestAuthPageGolden(t *testing.T) {
	for name, s := range map[string]PageState{
		"signin": {PasswordSet: true, Local: true, Strict: true},
		"create": {Local: true},
		"setup":  {},
	} {
		path := filepath.Join("testdata", "auth_page_"+name+".html")
		got := AuthPage(s)
		if os.Getenv("UPDATE_GOLDEN") == "1" {
			assert.NoError(t, os.WriteFile(path, []byte(got), 0o644))
			continue
		}
		want, err := os.ReadFile(path)
		assert.NoError(t, err)
		assert.Equal(t, string(want), got, "%s changed: run UPDATE_GOLDEN=1 go test ./internal/auth", path)
	}
}
