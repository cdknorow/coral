package tunnel

import (
	"context"
	"regexp"
)

// hubUserValue is the form of a Coral Hub user id, as coral-hub sends it in X-Coral-Hub-User.
var hubUserValue = regexp.MustCompile(`^hub-user-[0-9a-f]{8}$`)

// ValidHubUser reports whether s is a Coral Hub user id (hub-user-<8 hex>).
func ValidHubUser(s string) bool { return len(s) <= 32 && hubUserValue.MatchString(s) }

type hubUserKey struct{}

// WithHubUser returns ctx carrying the Coral Hub user a request is for. Session.RoundTrip and
// Session.DialWS send it in the stream's Header.HubUser, so the connector can cap what one
// viewer holds open (#3642). Anything but a valid id is ignored.
func WithHubUser(ctx context.Context, user string) context.Context {
	if !ValidHubUser(user) {
		return ctx
	}
	return context.WithValue(ctx, hubUserKey{}, user)
}

func hubUserFrom(ctx context.Context) string {
	u, _ := ctx.Value(hubUserKey{}).(string)
	return u
}
