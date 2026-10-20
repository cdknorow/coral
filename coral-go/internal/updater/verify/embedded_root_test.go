package verify

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The root coral-update trusts is the one the tests verify the real v1.4.120 release against: the two copies are the same bytes, and it loads.
func TestTheEmbeddedTrustedRootIsTheTestedOne(t *testing.T) {
	tested, err := os.ReadFile("testdata/trusted_root.json")
	require.NoError(t, err)
	assert.Equal(t, string(tested), string(embeddedRoot))
	tm, err := EmbeddedTrust()
	require.NoError(t, err)
	assert.NotNil(t, tm)
}
