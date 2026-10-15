package gitutil

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// #3601 (weak site W24): a base branch never reaches git as an option.
func TestValidRef(t *testing.T) {
	for _, ref := range []string{"main", "origin/main", "release-1.2", "feature/x_y", "0123abcd", "v1.4.43", "HEAD~1"} {
		assert.NoError(t, ValidRef(ref), ref)
	}
	for _, ref := range []string{"", "-x", "--upload-pack=touch /tmp/p", "--orphan", "-", "a b", "a\tb", "a\nb", "a\x00b", "a\x7fb", `a\b`, strings.Repeat("a", 256)} {
		assert.Error(t, ValidRef(ref), "%q", ref)
	}
}
