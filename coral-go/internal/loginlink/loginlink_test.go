package loginlink

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOr(t *testing.T) {
	base := "http://localhost:8420"
	assert.Equal(t, base, Or(base, nil), "no minter: the plain URL")
	assert.Equal(t, base, Or(base, func() (string, error) { return "", errors.New("older coral") }), "a failure: the plain URL")
	link := base + "/?login_token=abc"
	assert.Equal(t, link, Or(base, func() (string, error) { return link, nil }))
}

func TestRedactAndHasToken(t *testing.T) {
	link := "http://localhost:8420/?view=team&login_token=s3cret"
	r := Redact(link)
	assert.NotContains(t, r, "s3cret")
	assert.Contains(t, r, "login_token=REDACTED")
	assert.Contains(t, r, "view=team")
	assert.Equal(t, "http://localhost:8420/", Redact("http://localhost:8420/"))
	assert.True(t, HasToken(link))
	assert.False(t, HasToken("http://localhost:8420/"))
}

// Only the Coral on this computer is ever asked (the command-line token proves the same user).
func TestFromCLIOnlyAsksThisComputer(t *testing.T) {
	for _, base := range []string{"http://example.com:8420", "http://192.168.1.5:8420", "ftp://localhost", "localhost:8420"} {
		_, err := FromCLI(context.Background(), base, t.TempDir())
		assert.ErrorIs(t, err, ErrNotLocal, base)
	}
	for _, base := range []string{"http://localhost:8420", "http://127.0.0.1:1", "https://[::1]:8420"} {
		assert.True(t, local(base), base)
	}
}

// #3532: a sign-in link can land on a dashboard page; only a page of this dashboard is
// accepted, and the token moves onto its query.
func TestOnPage(t *testing.T) {
	for _, ok := range []string{"/", "/chats", "/chats?q=x", "/team/@m2/qa/settings", "/board/%40odd", "/agents/6f9a3c1e-0000-4000-8000-000000000001/notes"} {
		if !ValidPage(ok) {
			t.Errorf("ValidPage(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", "chats", "//evil.example/x", "/\\evil.example", "https://evil.example/", "/chats#x",
		"/x\r\nSet-Cookie: a=b", "/chats?login_token=abc", "/a\x00b"} {
		if ValidPage(bad) {
			t.Errorf("ValidPage(%q) = true", bad)
		}
	}
	link := "http://127.0.0.1:8420/?login_token=t%2Bk"
	for page, want := range map[string]string{
		"/":                    link,
		"/team/@m2/qa":         "http://127.0.0.1:8420/team/@m2/qa?login_token=t%2Bk",
		"/board/%40odd":        "http://127.0.0.1:8420/board/%40odd?login_token=t%2Bk",
		"/chats?q=x":           "http://127.0.0.1:8420/chats?q=x&login_token=t%2Bk",
		"//evil.example/x":     link,
		"https://evil.example": link,
	} {
		if got := OnPage(link, page); got != want {
			t.Errorf("OnPage(%q) = %q, want %q", page, got, want)
		}
	}
	if got := OnPage("http://127.0.0.1:8420/", "/chats"); got != "http://127.0.0.1:8420/" {
		t.Errorf("a link without a token is unchanged, got %q", got)
	}
}

// #6100: the Coral app marks the sign-in link it opens; the path stays as it was, and a URL with no login token is left alone.
func TestMarkApp(t *testing.T) {
	for in, want := range map[string]string{
		"http://127.0.0.1:8420/?login_token=t%2Bk":              "http://127.0.0.1:8420/?login_token=t%2Bk&coral_client=app",
		"http://127.0.0.1:8420/team/@m2/qa?login_token=t&q=x":   "http://127.0.0.1:8420/team/@m2/qa?login_token=t&q=x&coral_client=app",
		"http://127.0.0.1:8420/board/%40odd?login_token=t":      "http://127.0.0.1:8420/board/%40odd?login_token=t&coral_client=app",
		"http://127.0.0.1:8420/?login_token=t&coral_client=app": "http://127.0.0.1:8420/?login_token=t&coral_client=app",
		"http://127.0.0.1:8420/":                                "http://127.0.0.1:8420/",
		"http://127.0.0.1:8420/chats?q=x":                       "http://127.0.0.1:8420/chats?q=x",
		"::not a url":                                           "::not a url",
	} {
		if got := MarkApp(in); got != want {
			t.Errorf("MarkApp(%q) = %q, want %q", in, got, want)
		}
	}
}
