package tunnel

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #5508 (coral-hub's SPEC-5): the Hello advertises the list capability, versioned; a malformed or absent list supports nothing.
func TestHelloAdvertisesTheAllowlistCapabilityAndReadingIsStrict(t *testing.T) {
	raw, err := json.Marshal(Hello{ProtocolVersion: ProtocolVersion, Capabilities: []string{CapCrossTeamAllowlist}})
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"capabilities":["cross_team_allowlist/1"]`)
	var h Hello
	require.NoError(t, json.Unmarshal(raw, &h))
	assert.True(t, HasCapability(h.Capabilities, CapCrossTeamAllowlist))

	assert.False(t, HasCapability(nil, CapCrossTeamAllowlist), "absent: unsupported")
	assert.False(t, HasCapability([]string{"cross_team_allowlist/2"}, CapCrossTeamAllowlist), "another version")
	assert.False(t, HasCapability([]string{"cross_team_allowlist"}, CapCrossTeamAllowlist), "no version")
	assert.False(t, HasCapability([]string{"CROSS_TEAM_ALLOWLIST/1"}, CapCrossTeamAllowlist), "case matters")
	assert.False(t, HasCapability([]string{CapCrossTeamAllowlist, "bad one!"}, CapCrossTeamAllowlist), "any malformed entry voids the list")
	assert.False(t, HasCapability([]string{CapCrossTeamAllowlist, ""}, CapCrossTeamAllowlist))
	many := make([]string, 17)
	for i := range many {
		many[i] = "x/" + strings.Repeat("1", 1)
	}
	many[0] = CapCrossTeamAllowlist
	assert.False(t, HasCapability(many, CapCrossTeamAllowlist), "too many entries")
	assert.True(t, HasCapability([]string{"other/1", CapCrossTeamAllowlist}, CapCrossTeamAllowlist))
}
