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
	for raw, want := range map[string]string{
		"https://hooks.slack.com/services/T0001/B0002/XyZsecretPATH": "https://hooks.slack.com/…PATH",
		"https://example.com/hook?token=abcdefghij1234":              "https://example.com/…1234",
		"https://example.com/short":                                  "https://example.com/…",
		"https://example.com":                                        "https://example.com",
		"not a url":                                                  "<url>",
	} {
		assert.Equal(t, want, MaskURL(raw), raw)
		if strings.Contains(raw, "secret") {
			assert.NotContains(t, MaskURL(raw), "secret")
		}
	}
}
