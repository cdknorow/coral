package auth

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// #3939: the API-key page says the sign-in is a one-time step and where the key is, and points a person at the computer running Coral to
// coral open. It never prints a key itself.
func TestAuthPageSaysWhereTheKeyIs(t *testing.T) {
	p := authPageHTML
	assert.Contains(t, p, "one-time step", "explains the change")
	assert.Contains(t, p, "api_key", "names the key file")
	assert.Contains(t, p, "~/.coral/api_key", "and where it usually is")
	assert.Contains(t, p, "coral open", "and the easier way on that computer")
	assert.NotContains(t, p, `value="`, "the page carries no key value")
}
