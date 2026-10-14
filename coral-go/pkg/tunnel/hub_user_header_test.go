package tunnel

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

// #3377: the Coral Hub user header reaches the local API (the server validates and only
// honours it on tunnel chat posts); other hub headers are still dropped.
func TestScrubForwardsTheHubUserHeader(t *testing.T) {
	in := http.Header{}
	in.Set(HubUserHeader, "hub-user-1234abcd")
	in.Set("X-Coral-Hub-Admin", "1")
	out := scrub(in)
	assert.Equal(t, "hub-user-1234abcd", out.Get(HubUserHeader))
	assert.Empty(t, out.Get("X-Coral-Hub-Admin"))
	assert.Equal(t, "1", out.Get(ViaHeader))
}
