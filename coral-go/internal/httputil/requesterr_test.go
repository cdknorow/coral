package httputil

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRequestErrorNeverCarriesTheURL(t *testing.T) {
	secret := "https://hooks.slack.com/services/T0001/B0002/XyZsecretPATH?token=QsecretQUERY"
	err := &url.Error{Op: "Post", URL: secret, Err: errors.New("dial tcp 203.0.113.9:443: connect: connection refused")}
	got := RequestError(err)
	assert.Equal(t, "post: dial tcp 203.0.113.9:443: connect: connection refused", got)
	for _, part := range []string{"XyZsecretPATH", "QsecretQUERY", "T0001", "services"} {
		assert.NotContains(t, got, part)
	}
	// A URL inside the cause (a redirect, a wrapped message) is replaced too.
	got = RequestError(errors.New("bad redirect to " + secret + " refused"))
	assert.Equal(t, "bad redirect to <url> refused", got)
	assert.Equal(t, "", RequestError(nil))
	assert.LessOrEqual(t, len(RequestError(errors.New(strings.Repeat("x", 500)))), 200)
}

func TestMaskURL(t *testing.T) {
	a := MaskURL("https://hooks.slack.com/services/T0001/B0002/XyZsecretPATH")
	b := MaskURL("https://hooks.slack.com/services/T0001/B0002/OtherPATH")
	assert.Regexp(t, `^https://hooks\.slack\.com/…#[0-9a-f]{8}$`, a)
	assert.NotEqual(t, a, b, "two hooks to one host are told apart")
	assert.Equal(t, a, MaskURL("https://hooks.slack.com/services/T0001/B0002/XyZsecretPATH"), "stable")
	for _, part := range []string{"secret", "PATH", "T0001", "services"} {
		assert.NotContains(t, a, part, "no part of the path")
	}
	q := MaskURL("https://example.com/hook?token=abcdefghij1234")
	assert.NotContains(t, q, "1234")
	assert.NotContains(t, q, "token")
	assert.Equal(t, "https://example.com", MaskURL("https://example.com"))
	assert.Equal(t, "<url>", MaskURL("not a url"))
}
