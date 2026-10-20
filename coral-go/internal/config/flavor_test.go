//go:build !relaunchfixture

package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// #5452: a normal build carries no flavor: nothing in the version says relaunchfixture.
func TestADefaultBuildHasNoFlavor(t *testing.T) {
	assert.Equal(t, "", BuildFlavor)
	assert.Equal(t, "", FlavorNote())
	assert.False(t, strings.Contains(ReportedVersion(), "relaunchfixture"))
	old := Version
	t.Cleanup(func() { Version = old })
	Version = "1.4.134"
	assert.Equal(t, "1.4.134", ReportedVersion())
}
