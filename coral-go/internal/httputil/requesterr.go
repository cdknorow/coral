package httputil

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// A webhook URL usually is the secret (Slack, Discord and most generic hooks put the token in
// the path or query), and Go's *url.Error carries the whole request URL in its message, so a
// delivery error must never be stored, logged or shown as err.Error() (#3627 F3).

var urlInText = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s"'<>]+`)

// RedactURLs replaces every URL in s with "<url>".
func RedactURLs(s string) string { return urlInText.ReplaceAllString(s, "<url>") }

// RequestError describes a failed outbound request without its URL: the operation and the
// underlying cause (a *url.Error is unwrapped), any URL left in the text replaced, at most
// 200 bytes.
func RequestError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		msg = strings.ToLower(ue.Op) + ": " + ue.Err.Error()
	}
	msg = RedactURLs(msg)
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return msg
}

// MaskURL shows a URL as its scheme and host plus a short tail of the rest, so a webhook can be
// recognised in a list without its secret path or query: https://hooks.slack.com/…a1b2.
func MaskURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "<url>"
	}
	rest := strings.TrimPrefix(u.EscapedPath(), "/")
	if u.RawQuery != "" {
		rest += "?" + u.RawQuery
	}
	host := u.Scheme + "://" + u.Host
	if len(rest) <= 12 {
		if rest == "" {
			return host
		}
		return host + "/…"
	}
	return host + "/…" + rest[len(rest)-4:]
}
