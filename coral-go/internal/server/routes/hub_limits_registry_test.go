package routes

import (
	"testing"

	setreg "github.com/cdknorow/coral/internal/settings"
	"github.com/stretchr/testify/assert"
)

// The pool ranges the PUT validator checks are the ones the typed registry declares (and the reader holds a stored value to).
func TestHubLimitRangesMatchTheRegistry(t *testing.T) {
	for _, r := range hubLimitRanges {
		sp, ok := setreg.Lookup(r.key)
		assert.True(t, ok, r.key)
		assert.Equal(t, int64(r.min), sp.Min, r.key)
		assert.Equal(t, int64(r.max), sp.Max, r.key)
	}
}
