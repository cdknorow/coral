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
		assert.Contains(t, p, `<img src="/static/icon-192.png" alt="" width="44" height="44">Coral`, "Coral's logo at the top, from /static (served before sign-in, and covered by img-src 'self')")
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
		"signin":              {PasswordSet: true, Local: true, Strict: true},
		"create":              {Local: true},
		"setup":               {},
		"hub-ready":           {PasswordSet: true, Local: true, HubSignin: "ready", HubURL: "https://app.coralhub.dev"},
		"hub-unpaired":        {PasswordSet: true, Local: true, HubSignin: "unpaired"},
		"hub-unpaired-remote": {PasswordSet: true, HubSignin: "unpaired"},
		"hub-disabled":        {PasswordSet: true, Local: true, HubSignin: "disabled_in_hub", HubURL: "https://app.coralhub.dev"},
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

// #3966: what the page offers beside the password is the server's word (HubSignin); nothing else turns a Hub option on.
func TestAuthPageHubStates(t *testing.T) {
	none := AuthPage(PageState{PasswordSet: true, Local: true})
	for _, id := range []string{"hub-start-form", "hub-link-open", "panel-hublink", "panel-hubwait", "hub-disabled", "Sign in with Coral Hub"} {
		assert.NotContains(t, none, id, "no Hub UI when the server offers none: "+id)
	}
	for _, st := range []PageState{{HubSignin: "ready"}, {HubSignin: "unpaired"}} {
		assert.Contains(t, AuthPage(PageState{PasswordSet: true, Local: true, HubSignin: st.HubSignin}), "or</div>", "an or divider")
		// The first page (create a password) has one way in: the Hub is a quiet link beside the API-key one (#5990), not a second big button.
		first := AuthPage(PageState{Local: true, HubSignin: st.HubSignin})
		assert.NotContains(t, first, `class="btn hub"`, "no second primary path on the first page")
		assert.NotContains(t, first, "or</div>")
		assert.Contains(t, first, `class="link" id="hub-`, "the Hub way is a link")
	}

	ready := AuthPage(PageState{PasswordSet: true, Local: true, HubSignin: "ready", HubURL: "https://app.coralhub.dev"})
	assert.Contains(t, ready, `<form method="post" action="/auth/hub/start" id="hub-start-form">`, "a plain form POST, no script needed")
	assert.Contains(t, ready, "Sign in with Coral Hub")
	assert.Contains(t, ready, "Gives full control of it", "the button says what it gives")
	assert.NotContains(t, ready, `id="panel-hublink"`)
	assert.Regexp(t, `id="hub-disabled" role="status" hidden`, ready, "the disabled note is there for ?hub=disabled, hidden until then")

	unpaired := AuthPage(PageState{PasswordSet: true, Local: true, HubSignin: "unpaired"})
	assert.Contains(t, unpaired, `id="hub-link-open"`)
	assert.Contains(t, unpaired, "Link this computer to Coral Hub and sign in")
	assert.NotContains(t, unpaired, "/auth/hub/start", "no sign-in action until the link finishes")
	assert.Contains(t, unpaired, "Coral Hub can then sign you in here with full control of this computer.", "the full-control sentence is in the link step")
	assert.Contains(t, unpaired, `<input type="checkbox" id="hub-enable">`, "the checkbox is unchecked, and nothing in the markup ticks it")
	assert.NotRegexp(t, `id="hub-enable"[^>]*checked`, unpaired)
	assert.Regexp(t, `id="hub-setup-field" hidden`, unpaired, "from this computer no setup code is asked for")
	assert.Contains(t, unpaired, "Not now, use a password", "always an exit to the password")
	remote := AuthPage(PageState{PasswordSet: true, HubSignin: "unpaired"})
	assert.NotRegexp(t, `id="hub-setup-field" hidden`, remote, "from another computer the setup code field shows")
	assert.Contains(t, remote, "coral setup-code")

	off := AuthPage(PageState{PasswordSet: true, Local: true, HubSignin: "disabled_in_hub", HubURL: "https://app.coralhub.dev"})
	assert.Contains(t, off, "Sign in with Coral Hub is off for this computer.")
	assert.Contains(t, off, `<a class="link" href="https://app.coralhub.dev" target="_blank" rel="noopener noreferrer">Open Coral Hub</a>`, "only a link to the Hub's own page")
	assert.NotRegexp(t, `id="hub-disabled" role="status" hidden`, off, "shown")
	for _, never := range []string{"/auth/hub/start", "hub-link-open", "panel-hublink", "Sign in with Coral Hub</button>"} {
		assert.NotContains(t, off, never, "no sign-in action when the Hub's switch is off: "+never)
	}
	noURL := AuthPage(PageState{PasswordSet: true, Local: true, HubSignin: "disabled_in_hub"})
	assert.NotContains(t, noURL, "<a class=\"link\" href=", "no link without a Hub address")
	hostile := AuthPage(PageState{PasswordSet: true, Local: true, HubSignin: "disabled_in_hub", HubURL: "javascript:alert(1)"})
	assert.NotContains(t, hostile, "javascript:", "html/template refuses a script URL in the link")

	// the page also works with no password set (the create form), with the same Hub block
	create := AuthPage(PageState{Local: true, HubSignin: "ready"})
	assert.Contains(t, create, `id="create-form"`)
	assert.Contains(t, create, `id="hub-start-form"`)
}

// #4360 (the #4356 first-run audit): the page's text follows the browser's text size (rem, no px font sizes), the browser finds its icon (no /favicon.ico 404),
// the first field of the sign-in and create pages has focus when the page opens, and a long label wraps inside its button.
func TestAuthPageFirstRunDetails(t *testing.T) {
	for name, s := range map[string]PageState{
		"signin": {PasswordSet: true, Local: true, Strict: true},
		"create": {Local: true},
		"setup":  {},
	} {
		page := AuthPage(s)
		assert.NotRegexp(t, `font-size:\s*[0-9.]+px`, page, "%s: text sized in px does not follow the browser's text size", name)
		assert.Contains(t, page, `<link rel="icon" type="image/png" href="/static/favicon.png">`, name)
		assert.Contains(t, page, "min-height: 2.75rem", "%s: a touch target is 44 px", name)
		assert.Contains(t, page, `.btn.wide, .btn.hub { white-space: normal;`, name)
	}
	// Exactly one field takes focus on load: the password on the sign-in page, the setup code on a remote create page, the password on a local one.
	focus := func(s PageState) string {
		page := AuthPage(s)
		var ids []string
		for _, m := range regexp.MustCompile(`<input[^>]*\bautofocus\b[^>]*>`).FindAllString(page, -1) {
			ids = append(ids, regexp.MustCompile(`id="([^"]+)"`).FindStringSubmatch(m)[1])
		}
		return strings.Join(ids, ",")
	}
	assert.Equal(t, "password", focus(PageState{PasswordSet: true, Local: true, Strict: true}))
	assert.Equal(t, "password", focus(PageState{Local: true}))
	assert.Equal(t, "setup-code", focus(PageState{}))
}
